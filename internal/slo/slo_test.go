package slo

import (
	"math"
	"testing"
	"time"
)

// The blueprint's table is the contract. If a target drifts, every historical
// measurement becomes incomparable, so the numbers are asserted rather than
// trusted to survive editing.
func TestObjectivesMatchTheBlueprint(t *testing.T) {
	want := map[string]float64{
		FeedLatencyCached:  300,
		FeedLatencyCold:    800,
		SearchLatency:      500,
		LivenessAccuracy:   0.97,
		ParseYield:         0.98,
		DedupPrecision:     0.995,
		ExtractionValidity: 0.99,
		PipelineBacklog:    0,
		APIAvailability:    0.995,
		FreshnessTierA:     float64(15 * time.Minute),
		FreshnessTierB:     float64(2 * time.Hour),
		DigestGeneration:   float64(30 * time.Minute),
	}
	if len(Objectives) != len(want) {
		t.Fatalf("%d objectives, want %d — blueprint §28 lists twelve",
			len(Objectives), len(want))
	}
	for id, target := range want {
		o, ok := ByID(id)
		if !ok {
			t.Errorf("objective %s missing", id)
			continue
		}
		if o.Target != target {
			t.Errorf("%s target = %v, want %v", id, o.Target, target)
		}
		if o.Description == "" {
			t.Errorf("%s has no description", id)
		}
		if o.Window == 0 {
			t.Errorf("%s has no window; a target without one is not a target", id)
		}
	}
}

// An objective that cannot be measured must say what is missing. Silence would
// leave it looking healthy by default, which is the failure this whole package is
// arranged to avoid.
func TestUnmeasurableObjectivesNameTheirBlocker(t *testing.T) {
	var unmeasurable int
	for _, o := range Objectives {
		if o.Measurable {
			if o.Blocker != "" {
				t.Errorf("%s is measurable but names a blocker", o.ID)
			}
			continue
		}
		unmeasurable++
		if o.Blocker == "" {
			t.Errorf("%s is unmeasurable with no reason given", o.ID)
		}
	}
	if unmeasurable == 0 {
		t.Error("no objective is marked unmeasurable; at least liveness accuracy, " +
			"dedup precision and the digest cannot be measured yet")
	}
}

// Liveness accuracy is the product's central claim and the one thing we cannot
// verify ourselves. If this ever flips to measurable, someone has either found a
// source of truth or started guessing, and the second is worth a failing test.
func TestLivenessAccuracyStaysUnmeasurableUntilGroundTruthExists(t *testing.T) {
	o, ok := ByID(LivenessAccuracy)
	if !ok {
		t.Fatal("liveness accuracy objective missing")
	}
	if o.Measurable {
		t.Error("liveness ACCURACY is marked measurable; knowing whether a role is " +
			"genuinely open needs the employer's answer. Verification RECENCY is a " +
			"different claim and is reported separately.")
	}
}

// ------------------------------------------------------------- error budget

func TestErrorBudgetIsThePermittedFailureFraction(t *testing.T) {
	for _, tc := range []struct{ target, want float64 }{
		{0.995, 0.005}, {0.99, 0.01}, {0.97, 0.03}, {1.0, 0},
	} {
		if got := ErrorBudget(tc.target); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("ErrorBudget(%v) = %v, want %v", tc.target, got, tc.want)
		}
	}
}

func TestBudgetRemaining(t *testing.T) {
	const target = 0.995 // 0.5% budget

	for _, tc := range []struct {
		name     string
		observed float64
		want     float64
	}{
		{"nothing failed", 1.0, 1.0},
		{"half the budget spent", 0.9975, 0.5},
		{"exactly the budget spent", 0.995, 0.0},
		{"budget overspent", 0.99, -1.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := BudgetRemaining(target, tc.observed)
			if math.Abs(got-tc.want) > 1e-6 {
				t.Errorf("observed %v gave %v, want %v", tc.observed, got, tc.want)
			}
		})
	}
}

// A 100% target has no budget to spend, so the arithmetic must not divide by zero.
func TestBudgetRemainingHandlesAZeroBudget(t *testing.T) {
	if got := BudgetRemaining(1.0, 1.0); got != 1 {
		t.Errorf("a perfect record against a 100%% target gave %v, want 1", got)
	}
	if got := BudgetRemaining(1.0, 0.999); got >= 0 {
		t.Errorf("any failure against a 100%% target must be breached, got %v", got)
	}
}

// Burn rate is the whole reason multi-window alerting works. Comparing a raw
// failure fraction over one hour against a month's budget would call every brief
// blip a breach, which is how alerts get muted.
func TestBurnRateScalesWithTheMeasurementWindow(t *testing.T) {
	const target = 0.999 // 0.1% budget

	// Spending exactly the whole budget over the whole window is 1x.
	if got := BurnRate(target, 0.999, WindowMonth, WindowMonth); math.Abs(got-1) > 1e-6 {
		t.Errorf("full budget over the full window = %v, want 1", got)
	}

	// The same failure fraction over a tenth of the window is ten times the rate.
	tenth := WindowMonth / 10
	if got := BurnRate(target, 0.999, tenth, WindowMonth); math.Abs(got-10) > 1e-6 {
		t.Errorf("full budget in a tenth of the window = %v, want 10", got)
	}

	// Nothing failing is never burning.
	if got := BurnRate(target, 1.0, WindowHour, WindowMonth); got != 0 {
		t.Errorf("no failures gave a burn rate of %v", got)
	}
}

func TestBurnRateIsSafeOnDegenerateInputs(t *testing.T) {
	for _, tc := range []struct {
		name             string
		target, observed float64
		elapsed, window  time.Duration
	}{
		{"zero budget", 1.0, 0.5, time.Hour, WindowMonth},
		{"zero elapsed", 0.99, 0.5, 0, WindowMonth},
		{"zero window", 0.99, 0.5, time.Hour, 0},
	} {
		if got := BurnRate(tc.target, tc.observed, tc.elapsed, tc.window); got != 0 {
			t.Errorf("%s gave %v, want 0 rather than an infinity", tc.name, got)
		}
	}
}

// Observed above target must never produce a negative burn rate, which would read
// as "earning budget back".
func TestBurnRateNeverGoesNegative(t *testing.T) {
	if got := BurnRate(0.99, 1.0, time.Hour, WindowMonth); got < 0 {
		t.Errorf("burn rate %v is negative", got)
	}
}

// ------------------------------------------------------------- alerting

// Two windows because they catch different failures: a fast burn is an outage
// happening now, a slow burn is a degradation nobody notices.
func TestAlertSeverityUsesBothWindows(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate float64
		over time.Duration
		want Severity
	}{
		{"outage now", FastBurnRate, FastBurnWindow, SeverityPage},
		{"worse than the fast threshold", 50, FastBurnWindow, SeverityPage},
		{"fast rate but too brief to be an outage", FastBurnRate, time.Minute, SeverityNone},
		{"slow degradation", SlowBurnRate, SlowBurnWindow, SeverityTicket},
		{"slow rate over a short window", SlowBurnRate, time.Hour, SeverityNone},
		{"healthy", 0.5, SlowBurnWindow, SeverityNone},
	} {
		if got := Alert(tc.rate, tc.over); got != tc.want {
			t.Errorf("%s: %.1fx over %s gave %q, want %q", tc.name, tc.rate, tc.over, got, tc.want)
		}
	}
}

// A fast burn must page, not merely ticket: the thresholds are only useful if the
// more urgent one wins.
func TestFastBurnOutranksSlowBurn(t *testing.T) {
	if FastBurnRate <= SlowBurnRate {
		t.Fatal("the fast threshold must be higher than the slow one")
	}
	if got := Alert(FastBurnRate, SlowBurnWindow); got != SeverityPage {
		t.Errorf("a fast burn over a long window gave %q, want %q", got, SeverityPage)
	}
}

// ------------------------------------------------------------- evaluation

func TestEvaluateRatio(t *testing.T) {
	o := Objective{ID: "t", Kind: KindRatio, Target: 0.99, Window: WindowDay, Measurable: true}

	met := EvaluateRatio(o, 1000, 1000, WindowDay)
	if met.Status != StatusMet {
		t.Errorf("a perfect record gave %q: %s", met.Status, met.Detail)
	}

	breached := EvaluateRatio(o, 900, 1000, WindowDay)
	if breached.Status != StatusBreached {
		t.Errorf("90%% against a 99%% target gave %q", breached.Status)
	}
	if breached.BudgetRemaining == nil || *breached.BudgetRemaining >= 0 {
		t.Error("a breach must report a spent budget")
	}

	// Inside the target but burning too fast is the state worth acting on: by the
	// time an objective is breached, the users already had the bad month.
	atRisk := EvaluateRatio(o, 995, 1000, WindowDay/10)
	if atRisk.Status != StatusAtRisk {
		t.Errorf("burning budget at speed inside the target gave %q: %s",
			atRisk.Status, atRisk.Detail)
	}
}

// A ratio from three requests is not evidence, and the reader has to see that.
func TestEvaluateReportsItsSampleSize(t *testing.T) {
	o := Objective{ID: "t", Kind: KindRatio, Target: 0.99, Window: WindowDay, Measurable: true}
	r := EvaluateRatio(o, 3, 3, WindowDay)
	if r.Sample != 3 {
		t.Errorf("sample = %d, want 3", r.Sample)
	}
}

// No data and unmeasurable are different states needing different responses: a
// quiet hour is not a missing capability.
func TestNoDataIsDistinctFromUnmeasurable(t *testing.T) {
	measurable := Objective{ID: "t", Kind: KindRatio, Target: 0.99, Window: WindowDay, Measurable: true}
	if got := EvaluateRatio(measurable, 0, 0, WindowDay).Status; got != StatusNoData {
		t.Errorf("an empty window gave %q, want %q", got, StatusNoData)
	}

	blocked := Objective{ID: "t", Kind: KindRatio, Target: 0.99, Window: WindowDay,
		Measurable: false, Blocker: "needs ground truth"}
	r := EvaluateRatio(blocked, 100, 100, WindowDay)
	if r.Status != StatusUnmeasurable {
		t.Errorf("an unmeasurable objective gave %q", r.Status)
	}
	if r.Detail != "needs ground truth" {
		t.Errorf("detail = %q, want the blocker", r.Detail)
	}
	// Critically: a perfect input must NOT make an unmeasurable objective green.
	if r.Observed != nil {
		t.Error("an unmeasurable objective reported an observed value")
	}
}

func TestEvaluateLatencyAndDuration(t *testing.T) {
	lat := Objective{ID: "l", Kind: KindLatency, Target: 300, Percentile: 95,
		Window: WindowDay, Measurable: true}
	if got := EvaluateLatency(lat, 250, 1000).Status; got != StatusMet {
		t.Errorf("250ms against 300ms gave %q", got)
	}
	if got := EvaluateLatency(lat, 400, 1000).Status; got != StatusBreached {
		t.Errorf("400ms against 300ms gave %q", got)
	}
	if got := EvaluateLatency(lat, 0, 0).Status; got != StatusNoData {
		t.Errorf("no requests gave %q, want %q", got, StatusNoData)
	}

	dur := Objective{ID: "d", Kind: KindDuration, Target: float64(15 * time.Minute),
		Percentile: 95, Window: WindowDay, Measurable: true}
	if got := EvaluateDuration(dur, 5*time.Minute, 100).Status; got != StatusMet {
		t.Errorf("5m against 15m gave %q", got)
	}
	if got := EvaluateDuration(dur, time.Hour, 100).Status; got != StatusBreached {
		t.Errorf("1h against 15m gave %q", got)
	}
}

// The backlog ceiling is zero: one stranded record past the threshold is a breach,
// because the sweeper exists precisely so that cannot happen.
func TestEvaluateCountCeiling(t *testing.T) {
	o := Objective{ID: "b", Kind: KindCount, Target: 0, Window: WindowHour, Measurable: true}
	if got := EvaluateCount(o, 0).Status; got != StatusMet {
		t.Errorf("zero stranded gave %q", got)
	}
	if got := EvaluateCount(o, 1).Status; got != StatusBreached {
		t.Errorf("one stranded record gave %q, want a breach", got)
	}
}

// ------------------------------------------------------------- report

func TestReportHealthIgnoresUnmeasurableObjectives(t *testing.T) {
	r := Report{Results: []Result{
		{Objective: Objective{ID: "a"}, Status: StatusMet},
		{Objective: Objective{ID: "b"}, Status: StatusUnmeasurable},
		{Objective: Objective{ID: "c"}, Status: StatusNoData},
	}}
	if !r.Healthy() {
		t.Error("an unmeasurable objective made the report unhealthy; a known gap " +
			"is not a failure, and conflating them makes the signal useless")
	}
	if len(r.Unmeasurable()) != 1 {
		t.Errorf("%d unmeasurable, want 1", len(r.Unmeasurable()))
	}

	r.Results = append(r.Results, Result{Objective: Objective{ID: "d"}, Status: StatusBreached})
	if r.Healthy() {
		t.Error("a breach did not make the report unhealthy")
	}
	if len(r.Breached()) != 1 {
		t.Errorf("%d breached, want 1", len(r.Breached()))
	}
}

func TestReportSeparatesAtRiskFromBreached(t *testing.T) {
	r := Report{Results: []Result{
		{Objective: Objective{ID: "a"}, Status: StatusAtRisk},
		{Objective: Objective{ID: "b"}, Status: StatusBreached},
	}}
	if len(r.AtRisk()) != 1 || len(r.Breached()) != 1 {
		t.Errorf("at risk %d, breached %d; want one of each", len(r.AtRisk()), len(r.Breached()))
	}
	// At risk is not yet a breach, so it must not make the report unhealthy —
	// otherwise there is no signal left to escalate.
	r.Results = r.Results[:1]
	if !r.Healthy() {
		t.Error("an at-risk objective was treated as a breach")
	}
}
