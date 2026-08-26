package loadtest

import (
	"errors"
	"math"
	"testing"
	"time"
)

func ms(v int) time.Duration { return time.Duration(v) * time.Millisecond }

// The percentile decides pass or fail against the objective, so it is the one
// piece of arithmetic here that must be exactly right.
func TestPercentileIsNearestRank(t *testing.T) {
	// 1..100ms, so the answer is checkable by hand.
	vals := make([]float64, 100)
	for i := range vals {
		vals[i] = float64(i + 1)
	}
	for _, tc := range []struct {
		p    int
		want float64
	}{
		{50, 50}, {95, 95}, {99, 99}, {100, 100}, {1, 1},
	} {
		if got := percentile(vals, tc.p); got != tc.want {
			t.Errorf("p%d = %v, want %v", tc.p, got, tc.want)
		}
	}
}

// Nearest rank, not interpolated: an SLO asks whether the 95th percentile was
// under a bound, and the honest answer is a value some request actually took.
func TestPercentileReturnsAnObservedValue(t *testing.T) {
	vals := []float64{10, 20, 30}
	got := percentile(vals, 95)
	for _, v := range vals {
		if got == v {
			return
		}
	}
	t.Errorf("p95 = %v, which no request took; interpolation invites arguing with the result", got)
}

func TestPercentileHandlesDegenerateInput(t *testing.T) {
	if got := percentile(nil, 95); got != 0 {
		t.Errorf("empty input gave %v", got)
	}
	if got := percentile([]float64{42}, 95); got != 42 {
		t.Errorf("single sample gave %v, want 42", got)
	}
}

// A 500 that returns in 2ms must not pull the p95 down. A service failing fast
// would otherwise look fast, which is the opposite of what the objective asks.
func TestFastFailuresDoNotFlatterThePercentile(t *testing.T) {
	r := NewRecorder()
	// Twenty slow successes.
	for range 20 {
		r.Add(Sample{Duration: ms(500), Status: 200})
	}
	// Eighty instant 500s.
	for range 80 {
		r.Add(Sample{Duration: ms(1), Status: 500})
	}

	st := r.Summarize()
	if st.P95 != 500 {
		t.Errorf("p95 = %.0fms; fast failures were counted into the latency percentile", st.P95)
	}
	if st.ServerErrs != 80 {
		t.Errorf("server errors = %d, want 80", st.ServerErrs)
	}
	if st.Availability >= 0.5 {
		t.Errorf("availability = %.3f with 80%% 5xx", st.Availability)
	}
}

// 4xx is the API correctly rejecting a bad request, not the service failing.
// Counting it against availability would let a buggy client spend our budget.
func TestClientErrorsCountAsAvailable(t *testing.T) {
	r := NewRecorder()
	for range 50 {
		r.Add(Sample{Duration: ms(10), Status: 200})
	}
	for range 50 {
		r.Add(Sample{Duration: ms(10), Status: 404})
	}
	st := r.Summarize()
	if st.Availability != 1.0 {
		t.Errorf("availability = %.3f with 4xx only; a 404 is not the service failing",
			st.Availability)
	}
	if st.ClientErrs != 50 {
		t.Errorf("client errors = %d, want 50", st.ClientErrs)
	}
}

// A transport failure is not a slow response. Averaging it in as a duration would
// flatter the percentile with a request that never completed.
func TestTransportErrorsAreCountedSeparately(t *testing.T) {
	r := NewRecorder()
	r.Add(Sample{Duration: ms(5), Status: 200})
	r.Add(Sample{Duration: ms(30_000), Err: errors.New("connection refused")})

	st := r.Summarize()
	if st.Transport != 1 {
		t.Errorf("transport errors = %d, want 1", st.Transport)
	}
	if st.Successes != 1 {
		t.Errorf("successes = %d, want 1", st.Successes)
	}
	// The 30-second failure must not appear in the latency numbers.
	if st.Max != 5 {
		t.Errorf("max = %.0fms; a failed request leaked into the durations", st.Max)
	}
	if st.Availability != 0.5 {
		t.Errorf("availability = %.3f, want 0.5", st.Availability)
	}
}

// Truncation must be reported: a percentile over the first N samples is a
// percentile of the warm-up.
func TestOverflowIsReportedNotSilent(t *testing.T) {
	r := NewRecorder()
	r.cap = 10
	for range 25 {
		r.Add(Sample{Duration: ms(1), Status: 200})
	}
	st := r.Summarize()
	if st.Total != 10 {
		t.Errorf("kept %d samples, want the cap of 10", st.Total)
	}
	if st.Overflow != 15 {
		t.Errorf("overflow = %d, want 15", st.Overflow)
	}
	if !contains(st.String(), "WARNING") {
		t.Error("a truncated run did not warn; the percentile covers only the prefix")
	}
}

func TestEmptyRunReportsNothingRatherThanZeroLatency(t *testing.T) {
	st := NewRecorder().Summarize()
	if st.Total != 0 || st.P95 != 0 || st.Availability != 0 {
		t.Errorf("an empty run reported %+v", st)
	}
}

func TestConfigDefaultsToTheProductPromise(t *testing.T) {
	c := Config{}.WithDefaults()
	if c.FeedSize != 7 {
		t.Errorf("feed size = %d; the product promises 7 a day", c.FeedSize)
	}
	if c.Users < 2 {
		t.Errorf("users = %d; one user measures the score cache rather than the feed", c.Users)
	}
	if c.Concurrency < 1 || c.Duration <= 0 {
		t.Errorf("bad defaults: %+v", c)
	}
}

func TestExplicitConfigSurvivesDefaulting(t *testing.T) {
	c := Config{Users: 3, Concurrency: 2, Duration: time.Second, FeedSize: 20}.WithDefaults()
	if c.Users != 3 || c.Concurrency != 2 || c.Duration != time.Second || c.FeedSize != 20 {
		t.Errorf("defaults overwrote explicit values: %+v", c)
	}
}

func TestAvailabilityMath(t *testing.T) {
	r := NewRecorder()
	for range 995 {
		r.Add(Sample{Duration: ms(10), Status: 200})
	}
	for range 5 {
		r.Add(Sample{Duration: ms(10), Status: 503})
	}
	if got := r.Summarize().Availability; math.Abs(got-0.995) > 1e-9 {
		t.Errorf("availability = %v, want 0.995", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
