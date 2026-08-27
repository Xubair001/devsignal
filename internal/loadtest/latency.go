// Package loadtest drives the real API under load and checks the result against
// the service level objectives.
//
// Blueprint §35 step 21 calls this "a test that can fail", which is the whole
// point: a load test that reports numbers without comparing them to a target is a
// benchmark, and a benchmark never tells you to stop.
//
// Latency is measured CLIENT-SIDE, from just before the request to just after the
// body is read. That is deliberate and it is where a user-facing latency objective
// belongs: server-side handler time excludes connection setup, request parsing,
// response serialization and the body write, all of which the user waits for. It
// also means this needs no metrics backend to produce a percentile.
package loadtest

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Sample is one observed request.
type Sample struct {
	Duration time.Duration
	Status   int
	// Err is set when the request never completed. Distinct from a 5xx: a
	// connection refused is not a slow response, and averaging it in as a duration
	// would flatter the percentile.
	Err error
}

// OK reports whether this counts as a successful request for the availability
// objective.
//
// 4xx counts as SUCCESS. A 400 is the API correctly rejecting a malformed request
// and a 404 is a posting that does not exist; neither is the service failing. Only
// 5xx and transport errors are. Counting 4xx as failure would let a client with a
// bug consume our error budget.
func (s Sample) OK() bool { return s.Err == nil && s.Status < 500 }

// Recorder accumulates samples.
//
// Keeps every duration rather than a streaming digest. At the volumes a load test
// produces — hundreds of thousands at most — an exact percentile costs a few
// megabytes and removes a whole class of doubt about whether the number is real.
// A t-digest would be the right call at production scale and the wrong one here,
// where the question is whether the SLO is met and an approximation invites
// arguing with the answer.
type Recorder struct {
	samples []Sample
	// cap bounds memory if someone runs this for an hour. Reaching it is reported
	// rather than silently truncating, because a percentile over a truncated
	// prefix is a percentile of the warm-up.
	cap      int
	overflow int
}

// DefaultSampleCap is roughly 200k samples, about 5 MB.
const DefaultSampleCap = 200_000

// NewRecorder builds one.
func NewRecorder() *Recorder {
	return &Recorder{samples: make([]Sample, 0, 4096), cap: DefaultSampleCap}
}

// Add records a sample.
func (r *Recorder) Add(s Sample) {
	if len(r.samples) >= r.cap {
		r.overflow++
		return
	}
	r.samples = append(r.samples, s)
}

// Len is how many samples were kept.
func (r *Recorder) Len() int { return len(r.samples) }

// Overflow is how many were dropped past the cap.
func (r *Recorder) Overflow() int { return r.overflow }

// Stats summarizes a run.
type Stats struct {
	Total      int
	Successes  int
	ServerErrs int
	ClientErrs int
	Transport  int
	// Percentiles in milliseconds, over SUCCESSFUL requests only.
	P50, P95, P99 float64
	Min, Max      float64
	// Availability is successes over total, the availability SLI.
	Availability float64
	Overflow     int
	// StatusCounts is the distribution, for reading what actually happened rather
	// than guessing from a ratio.
	StatusCounts map[int]int
}

// Summarize computes the statistics.
//
// Percentiles over successful requests only. A 500 that returns in 2ms would
// otherwise pull the p95 DOWN — a service failing fast would look fast, which is
// the opposite of what the objective is asking.
func (r *Recorder) Summarize() Stats {
	st := Stats{Total: len(r.samples), Overflow: r.overflow, StatusCounts: map[int]int{}}

	ok := make([]float64, 0, len(r.samples))
	for _, s := range r.samples {
		switch {
		case s.Err != nil:
			st.Transport++
		case s.Status >= 500:
			st.ServerErrs++
		case s.Status >= 400:
			st.ClientErrs++
		}
		if s.Err == nil {
			st.StatusCounts[s.Status]++
		}
		if s.OK() {
			st.Successes++
			ok = append(ok, float64(s.Duration.Microseconds())/1000.0)
		}
	}
	if st.Total > 0 {
		st.Availability = float64(st.Successes) / float64(st.Total)
	}
	if len(ok) == 0 {
		return st
	}

	sort.Float64s(ok)
	st.Min, st.Max = ok[0], ok[len(ok)-1]
	st.P50 = percentile(ok, 50)
	st.P95 = percentile(ok, 95)
	st.P99 = percentile(ok, 99)
	return st
}

// percentile returns the nearest-rank percentile of a sorted slice.
//
// Nearest-rank rather than interpolated: an SLO asks "was the 95th percentile
// under 300ms", and the honest answer is a value that was actually observed. An
// interpolated 299.7ms that no request took invites arguing about the method
// instead of the result.
func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	// Ceil of p% of n, one-indexed, then converted to a zero-indexed position.
	rank := int(math.Ceil(float64(p) / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// String renders the statistics for a terminal.
func (s Stats) String() string {
	out := fmt.Sprintf(
		"  requests %d  ok %d  5xx %d  4xx %d  transport %d  availability %.3f%%\n"+
			"  p50 %.0fms  p95 %.0fms  p99 %.0fms  min %.0fms  max %.0fms",
		s.Total, s.Successes, s.ServerErrs, s.ClientErrs, s.Transport,
		s.Availability*100, s.P50, s.P95, s.P99, s.Min, s.Max)
	if s.Overflow > 0 {
		out += fmt.Sprintf("\n  WARNING: %d samples dropped past the cap; "+
			"the percentiles cover only the first %d", s.Overflow, s.Total)
	}
	return out
}
