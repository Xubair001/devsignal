// Package slo holds the service level objectives and the error budget arithmetic.
//
// The blueprint's reason for this existing as code rather than a wiki page:
// collecting percentiles is not the same as having a target. A target has a number,
// a window, and a consequence when it is missed.
//
// The design decision that matters most here is what happens to an objective we
// cannot yet measure. Three of the twelve need data the system does not collect —
// liveness accuracy needs ground truth about whether a role is genuinely open,
// dedup precision needs labelled pairs, digest generation needs a digest. Those
// report as UNMEASURABLE with the reason attached, never as green.
//
// A dashboard that shows green for something nobody measured is worse than one
// with a gap in it: the gap prompts a question, and the false green ends the
// conversation. It is the same rule the product applies to users (blueprint §3),
// applied to ourselves.
package slo

import (
	"fmt"
	"math"
	"time"
)

// Kind describes how an objective is compared against its target.
type Kind string

const (
	// KindRatio is a success fraction, like availability. Higher is better and the
	// error budget is the permitted failure fraction.
	KindRatio Kind = "ratio"
	// KindLatency is a percentile bound in milliseconds. Lower is better.
	KindLatency Kind = "latency"
	// KindDuration is a percentile bound expressed as a duration, for freshness.
	KindDuration Kind = "duration"
	// KindCount is a hard ceiling on a count, like stranded records. The target is
	// the maximum tolerable.
	KindCount Kind = "count"
)

// Objective is one row of blueprint §28's SLO table, as data.
type Objective struct {
	ID          string
	Description string
	Kind        Kind
	// Target is the threshold: a fraction for KindRatio, milliseconds for
	// KindLatency, a duration for KindDuration, a count for KindCount.
	Target float64
	// Percentile the target applies to, for latency and duration objectives.
	Percentile int
	// Window the objective is measured over.
	Window time.Duration
	// Measurable records whether the system currently collects what this needs,
	// and if not, what is missing. An objective is not allowed to be silently
	// unmeasured.
	Measurable bool
	Blocker    string
}

// Standard windows. A month is 30 days rather than a calendar month so the budget
// arithmetic is stable — a 28-day February would silently shrink the budget.
const (
	WindowHour  = time.Hour
	WindowDay   = 24 * time.Hour
	WindowMonth = 30 * 24 * time.Hour
)

// Objective ids. Stable strings: they name alert rules and dashboard panels.
const (
	FeedLatencyCached  = "feed_latency_cached"
	FeedLatencyCold    = "feed_latency_cold"
	SearchLatency      = "search_latency"
	FreshnessTierA     = "freshness_tier_a"
	FreshnessTierB     = "freshness_tier_b"
	LivenessAccuracy   = "liveness_accuracy"
	ParseYield         = "parse_yield"
	DedupPrecision     = "dedup_precision"
	ExtractionValidity = "extraction_validity"
	DigestGeneration   = "digest_generation"
	PipelineBacklog    = "pipeline_backlog"
	APIAvailability    = "api_availability"
)

// Objectives is blueprint §28's table, verbatim in its numbers.
//
// The `Blocker` fields are the honest part. Each one names what is missing rather
// than leaving the objective looking healthy by default.
var Objectives = []Objective{
	{
		ID: FeedLatencyCached, Description: "Feed latency, cached",
		Kind: KindLatency, Target: 300, Percentile: 95, Window: WindowDay,
		Measurable: true,
	},
	{
		ID: FeedLatencyCold, Description: "Feed latency, cold",
		Kind: KindLatency, Target: 800, Percentile: 95, Window: WindowDay,
		Measurable: true,
	},
	{
		ID: SearchLatency, Description: "Search latency",
		Kind: KindLatency, Target: 500, Percentile: 95, Window: WindowDay,
		// There is no search endpoint yet: /opportunities is a keyset list, and
		// faceted search is unbuilt. Measuring list latency and calling it search
		// would be measuring the wrong thing to fill the row.
		Measurable: false,
		Blocker:    "no search endpoint exists yet; /opportunities is a keyset list",
	},
	{
		ID: FreshnessTierA, Description: "Freshness, Tier A (first seen → visible)",
		Kind: KindDuration, Target: float64(15 * time.Minute), Percentile: 95,
		Window: WindowDay, Measurable: true,
	},
	{
		ID: FreshnessTierB, Description: "Freshness, Tier B",
		Kind: KindDuration, Target: float64(2 * time.Hour), Percentile: 50,
		Window:     WindowDay,
		Measurable: false,
		Blocker:    "no Tier B source is registered; every source is Tier A",
	},
	{
		ID: LivenessAccuracy, Description: "Liveness accuracy of shown roles",
		Kind: KindRatio, Target: 0.97, Window: WindowMonth,
		// The product's central claim, and the one objective we cannot check
		// ourselves. Knowing whether a role is GENUINELY open needs a source of
		// truth we do not have: the employer's own answer. What we can measure is
		// how recently we verified, which is a different statement.
		Measurable: false,
		Blocker: "needs ground truth on whether a role is genuinely open; " +
			"we can measure how recently we checked, which is not the same claim",
	},
	{
		ID: ParseYield, Description: "Parse yield per source",
		Kind: KindRatio, Target: 0.98, Window: WindowDay, Measurable: true,
	},
	{
		ID: DedupPrecision, Description: "Dedup precision (false merges weighted heavier)",
		Kind: KindRatio, Target: 0.995, Window: WindowMonth,
		Measurable: false,
		Blocker: "needs labelled duplicate pairs; the merge_candidate queue " +
			"records withheld merges but nothing labels the merges that were made",
	},
	{
		ID: ExtractionValidity, Description: "Extraction validity against schema",
		Kind: KindRatio, Target: 0.99, Window: WindowDay, Measurable: true,
	},
	{
		ID: DigestGeneration, Description: "Digest generation inside a 30-minute window",
		Kind: KindDuration, Target: float64(30 * time.Minute), Percentile: 100,
		Window:     WindowDay,
		Measurable: false,
		Blocker:    "the digest is step 18 and is not built",
	},
	{
		ID: PipelineBacklog, Description: "No record in a non-terminal state over 1 hour",
		Kind: KindCount, Target: 0, Window: WindowHour, Measurable: true,
	},
	{
		ID: APIAvailability, Description: "API availability",
		Kind: KindRatio, Target: 0.995, Window: WindowMonth, Measurable: true,
	},
}

// ByID looks an objective up.
func ByID(id string) (Objective, bool) {
	for _, o := range Objectives {
		if o.ID == id {
			return o, true
		}
	}
	return Objective{}, false
}

// Status is the verdict on one objective.
type Status string

const (
	StatusMet Status = "met"
	// StatusAtRisk means the objective is met but the error budget is burning
	// faster than the window allows. This is the state worth alerting on: by the
	// time an objective is breached the users already had the bad month.
	StatusAtRisk   Status = "at_risk"
	StatusBreached Status = "breached"
	// StatusUnmeasurable is a first-class outcome, not an error. See the package
	// comment.
	StatusUnmeasurable Status = "unmeasurable"
	// StatusNoData means measurable in principle but nothing was observed in the
	// window. Distinct from unmeasurable: a quiet hour is not a missing capability.
	StatusNoData Status = "no_data"
)

// Result is one objective evaluated against observed data.
type Result struct {
	Objective Objective
	Status    Status
	// Observed is the measured value in the objective's own units, or nil when
	// there is nothing to report.
	Observed *float64
	// Sample is how many events the measurement is based on. A ratio from three
	// requests is not evidence, and the reader needs to see that.
	Sample int64
	// BudgetRemaining is the fraction of the error budget left, for ratio
	// objectives. 1.0 is untouched, 0 is exhausted, negative is breached.
	BudgetRemaining *float64
	// BurnRate is how fast the budget is being consumed relative to the rate that
	// would exactly exhaust it over the window. Above 1 means the objective will
	// be missed if it continues.
	BurnRate *float64
	// Detail explains the result in a sentence, especially when it is not "met".
	Detail string
}

// ErrorBudget is the permitted failure fraction for a ratio objective.
//
// For a 99.5% target the budget is 0.5% of requests. Expressed as a fraction
// rather than a count because the count depends on traffic, and an objective
// whose strictness varies with traffic is not a target.
func ErrorBudget(target float64) float64 {
	return 1 - target
}

// BudgetRemaining is the fraction of the error budget still unspent.
//
//	1.0  nothing has failed
//	0.0  exactly the budget has been spent; the objective is met but only just
//	<0   the objective is breached
//
// Returns 1 for a zero budget (a 100% target), because there is no budget to
// spend and any failure breaches immediately — which the caller detects from the
// observed ratio rather than from a division by zero here.
func BudgetRemaining(target, observed float64) float64 {
	budget := ErrorBudget(target)
	if budget <= 0 {
		if observed >= 1 {
			return 1
		}
		return -1
	}
	failed := 1 - observed
	return (budget - failed) / budget
}

// BurnRate is how fast the budget is being consumed relative to the rate that
// would exhaust it exactly at the end of the window.
//
//	1.0  on track to spend the whole budget precisely as the window closes
//	2.0  spending twice as fast; the budget is gone at the halfway point
//	0    nothing failing
//
// elapsed is how much of the window the measurement covers. Measuring one hour of
// a 30-day objective and comparing the raw failure fraction against the whole
// month's budget would call every brief blip a breach, which is how alerts get
// muted.
func BurnRate(target, observed float64, elapsed, window time.Duration) float64 {
	budget := ErrorBudget(target)
	if budget <= 0 || elapsed <= 0 || window <= 0 {
		return 0
	}
	failed := math.Max(0, 1-observed)
	// The fraction of the window this measurement covers.
	coverage := float64(elapsed) / float64(window)
	if coverage <= 0 {
		return 0
	}
	// Spending `failed` of the budget over `coverage` of the window.
	return (failed / budget) / coverage
}

// Burn-rate alert thresholds, following the multi-window approach in Google's SRE
// workbook. Two windows rather than one because they catch different failures:
//
//   - A fast burn is an outage happening now. 14.4x over an hour consumes 2% of a
//     30-day budget, which is worth waking someone for.
//   - A slow burn is a degradation nobody notices. 6x over six hours consumes 5%,
//     which will exhaust the month if it continues but does not need a page at 3am.
//
// Single-window alerting forces a choice between missing slow burns and paging on
// every blip; these are the standard numbers for avoiding both.
const (
	FastBurnRate   = 14.4
	FastBurnWindow = time.Hour

	SlowBurnRate   = 6.0
	SlowBurnWindow = 6 * time.Hour
)

// Severity is what an operator should do about a burn rate.
type Severity string

const (
	SeverityNone Severity = "none"
	// SeverityPage is an outage in progress.
	SeverityPage Severity = "page"
	// SeverityTicket is a degradation that will exhaust the budget if ignored but
	// does not need anyone woken up.
	SeverityTicket Severity = "ticket"
)

// Alert classifies a burn rate over a window.
//
// Takes the window so the same rate means different things: 14.4x sustained for
// an hour is an outage, while 14.4x for ten seconds is a deploy.
func Alert(burnRate float64, over time.Duration) Severity {
	switch {
	case burnRate >= FastBurnRate && over >= FastBurnWindow:
		return SeverityPage
	case burnRate >= SlowBurnRate && over >= SlowBurnWindow:
		return SeverityTicket
	default:
		return SeverityNone
	}
}

// EvaluateRatio turns a measured success ratio into a Result.
func EvaluateRatio(o Objective, successes, total int64, elapsed time.Duration) Result {
	r := Result{Objective: o, Sample: total}
	if !o.Measurable {
		r.Status = StatusUnmeasurable
		r.Detail = o.Blocker
		return r
	}
	if total == 0 {
		r.Status = StatusNoData
		r.Detail = "nothing observed in the window"
		return r
	}

	observed := float64(successes) / float64(total)
	remaining := BudgetRemaining(o.Target, observed)
	burn := BurnRate(o.Target, observed, elapsed, o.Window)
	r.Observed, r.BudgetRemaining, r.BurnRate = &observed, &remaining, &burn

	switch {
	case observed < o.Target && remaining < 0:
		r.Status = StatusBreached
		r.Detail = fmt.Sprintf("%.3f%% against a %.3f%% target; the error budget is spent",
			observed*100, o.Target*100)
	case burn > 1:
		// Still inside the target, but consuming budget faster than the window
		// allows. This is the state worth acting on: by the time an objective is
		// breached the users already had the bad month.
		r.Status = StatusAtRisk
		r.Detail = fmt.Sprintf("%.3f%% observed, burning budget at %.1fx", observed*100, burn)
	default:
		r.Status = StatusMet
		r.Detail = fmt.Sprintf("%.3f%% against a %.3f%% target", observed*100, o.Target*100)
	}
	return r
}

// EvaluateLatency turns an observed percentile in milliseconds into a Result.
//
// Latency objectives have no error budget in the ratio sense: a p95 either clears
// the bound or it does not. Expressing "p95 under 300ms" as a budget would require
// deciding what fraction of days may miss it, which the blueprint does not state
// and which would be invented here.
func EvaluateLatency(o Objective, observedMillis float64, sample int64) Result {
	r := Result{Objective: o, Sample: sample}
	if !o.Measurable {
		r.Status = StatusUnmeasurable
		r.Detail = o.Blocker
		return r
	}
	if sample == 0 {
		r.Status = StatusNoData
		r.Detail = "no requests observed in the window"
		return r
	}
	r.Observed = &observedMillis
	if observedMillis <= o.Target {
		r.Status = StatusMet
		r.Detail = fmt.Sprintf("p%d %.0fms against a %.0fms target",
			o.Percentile, observedMillis, o.Target)
	} else {
		r.Status = StatusBreached
		r.Detail = fmt.Sprintf("p%d %.0fms exceeds the %.0fms target",
			o.Percentile, observedMillis, o.Target)
	}
	return r
}

// EvaluateDuration is EvaluateLatency for objectives expressed as durations.
func EvaluateDuration(o Objective, observed time.Duration, sample int64) Result {
	r := Result{Objective: o, Sample: sample}
	if !o.Measurable {
		r.Status = StatusUnmeasurable
		r.Detail = o.Blocker
		return r
	}
	if sample == 0 {
		r.Status = StatusNoData
		r.Detail = "nothing observed in the window"
		return r
	}
	v := float64(observed)
	r.Observed = &v
	target := time.Duration(o.Target)
	if observed <= target {
		r.Status = StatusMet
		r.Detail = fmt.Sprintf("p%d %s against a %s target",
			o.Percentile, observed.Round(time.Second), target)
	} else {
		r.Status = StatusBreached
		r.Detail = fmt.Sprintf("p%d %s exceeds the %s target",
			o.Percentile, observed.Round(time.Second), target)
	}
	return r
}

// EvaluateCount checks a ceiling.
func EvaluateCount(o Objective, observed int64) Result {
	r := Result{Objective: o, Sample: observed}
	if !o.Measurable {
		r.Status = StatusUnmeasurable
		r.Detail = o.Blocker
		return r
	}
	v := float64(observed)
	r.Observed = &v
	if v <= o.Target {
		r.Status = StatusMet
		r.Detail = fmt.Sprintf("%d, at or under the ceiling of %.0f", observed, o.Target)
	} else {
		r.Status = StatusBreached
		r.Detail = fmt.Sprintf("%d exceeds the ceiling of %.0f", observed, o.Target)
	}
	return r
}

// Report is a whole evaluation.
type Report struct {
	Results    []Result
	MeasuredAt time.Time
}

// Breached lists the objectives currently missed.
func (r Report) Breached() []Result { return r.withStatus(StatusBreached) }

// AtRisk lists the objectives burning budget too fast.
func (r Report) AtRisk() []Result { return r.withStatus(StatusAtRisk) }

// Unmeasurable lists the objectives the system cannot yet check, which is the
// part of the report most worth reading.
func (r Report) Unmeasurable() []Result { return r.withStatus(StatusUnmeasurable) }

func (r Report) withStatus(s Status) []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Status == s {
			out = append(out, res)
		}
	}
	return out
}

// Healthy reports whether nothing is breached. Deliberately does NOT consider
// unmeasurable objectives a failure — they are a known gap, and conflating the
// two would make the signal useless.
func (r Report) Healthy() bool { return len(r.Breached()) == 0 }
