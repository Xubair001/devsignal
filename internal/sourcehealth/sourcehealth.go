// Package sourcehealth decides whether a source has degraded.
//
// The failure this exists to catch is parser rot, and it is invisible to
// ordinary monitoring: source payloads drift, the parser keeps returning rows,
// and the fields go quietly empty. Fetches succeed, throughput looks normal, and
// match quality decays for weeks (blueprint §29).
//
// So the test is never an absolute threshold. It is a RELATIVE drop against this
// source's own recent past — a source that fell from 98% to 71% field
// completeness is broken even though 71% would pass any fixed floor.
package sourcehealth

import "fmt"

type Status string

const (
	// StatusHealthy — nothing to report.
	StatusHealthy Status = "healthy"
	// StatusDegraded — something dropped relative to this source's baseline.
	StatusDegraded Status = "degraded"
	// StatusUnknown — not enough data to judge. Deliberately distinct from
	// healthy: "we cannot tell" must never be reported as "fine".
	StatusUnknown Status = "unknown"
)

// Field names tracked for fill rate. Salary is deliberately absent from the
// alerting set: most sources never provide it, so its fill rate is legitimately
// near zero and would alert forever.
const (
	FieldCompany  = "company"
	FieldLocation = "location"
	FieldApplyURL = "apply_url"
	FieldLanguage = "language"
)

// alertingFields are the ones a drop in is evidence of rot.
var alertingFields = []string{FieldCompany, FieldLocation, FieldApplyURL, FieldLanguage}

// Metrics is one observation window for one source.
type Metrics struct {
	Postings int
	Usable   int
	Fill     map[string]int
}

// Yield is the proportion of postings that produced a usable record.
func (m Metrics) Yield() float64 {
	if m.Postings == 0 {
		return 0
	}
	return float64(m.Usable) / float64(m.Postings)
}

// FillRate is the proportion of postings that had this field populated.
func (m Metrics) FillRate(field string) float64 {
	if m.Postings == 0 {
		return 0
	}
	return float64(m.Fill[field]) / float64(m.Postings)
}

// Thresholds, stated rather than buried.
const (
	// MinSample is how many postings each side needs before a comparison means
	// anything. Below it a couple of odd rows swing every rate.
	MinSample = 20

	// Relative drops, as a fraction of the baseline. Yield is stricter than fill
	// because a yield drop means whole records are being lost.
	YieldDropLimit = 0.10
	FillDropLimit  = 0.20

	// A field that is essentially never populated is not "rotting" — it simply
	// is not provided. Ignore fields below this baseline rate.
	FillFloorToCare = 0.30

	// ConsecutiveToQuarantine is how many degraded evaluations in a row before a
	// source is taken offline. One bad poll must never quarantine: a transient
	// blip would silently cost corpus coverage.
	ConsecutiveToQuarantine = 3
)

type Reason struct {
	Code   string  `json:"code"`
	Field  string  `json:"field,omitempty"`
	Detail string  `json:"detail"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

type Verdict struct {
	Status  Status   `json:"status"`
	Reasons []Reason `json:"reasons"`
}

// Compare judges the current window against a baseline of this source's own
// recent history. Pure and deterministic.
func Compare(current, baseline Metrics) Verdict {
	if current.Postings < MinSample || baseline.Postings < MinSample {
		return Verdict{Status: StatusUnknown, Reasons: []Reason{{
			Code: "insufficient_sample",
			Detail: fmt.Sprintf("need %d postings on both sides, have current=%d baseline=%d",
				MinSample, current.Postings, baseline.Postings),
		}}}
	}

	var reasons []Reason

	if bY, cY := baseline.Yield(), current.Yield(); bY > 0 {
		if drop := (bY - cY) / bY; drop >= YieldDropLimit {
			reasons = append(reasons, Reason{
				Code: "yield_drop",
				Detail: fmt.Sprintf("usable-record rate fell %.0f%% relative to baseline",
					drop*100),
				Before: bY, After: cY,
			})
		}
	}

	for _, f := range alertingFields {
		bF := baseline.FillRate(f)
		// A field the source barely populates cannot rot.
		if bF < FillFloorToCare {
			continue
		}
		cF := current.FillRate(f)
		if drop := (bF - cF) / bF; drop >= FillDropLimit {
			reasons = append(reasons, Reason{
				Code:  "field_fill_drop",
				Field: f,
				Detail: fmt.Sprintf("%s populated on %.0f%% of postings, down from %.0f%%",
					f, cF*100, bF*100),
				Before: bF, After: cF,
			})
		}
	}

	if len(reasons) == 0 {
		return Verdict{Status: StatusHealthy}
	}
	return Verdict{Status: StatusDegraded, Reasons: reasons}
}

// ShouldQuarantine reports whether sustained degradation has crossed the line.
// Separated from Compare so the policy is testable without a database.
func ShouldQuarantine(consecutiveDegraded int) bool {
	return consecutiveDegraded >= ConsecutiveToQuarantine
}
