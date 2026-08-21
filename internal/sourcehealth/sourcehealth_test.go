package sourcehealth

import "testing"

func metrics(postings, usable int, fill map[string]int) Metrics {
	if fill == nil {
		fill = map[string]int{}
	}
	return Metrics{Postings: postings, Usable: usable, Fill: fill}
}

// A full-fill baseline for a healthy source.
func full(n int) Metrics {
	return metrics(n, n, map[string]int{
		FieldCompany: n, FieldLocation: n, FieldApplyURL: n, FieldLanguage: n,
	})
}

func TestSteadySourceIsHealthy(t *testing.T) {
	v := Compare(full(200), full(200))
	if v.Status != StatusHealthy {
		t.Errorf("status = %s, want healthy (%v)", v.Status, v.Reasons)
	}
	if len(v.Reasons) != 0 {
		t.Errorf("healthy source reported reasons: %v", v.Reasons)
	}
}

// The case the whole package exists for: nothing errors, every row still
// arrives, but a field quietly stops being populated.
func TestFieldFillDropIsCaught(t *testing.T) {
	baseline := full(200)
	// 98% -> 71% on location. Nothing failed; the parser just stopped finding it.
	current := metrics(200, 200, map[string]int{
		FieldCompany: 200, FieldLocation: 142, FieldApplyURL: 200, FieldLanguage: 200,
	})
	v := Compare(current, baseline)
	if v.Status != StatusDegraded {
		t.Fatalf("status = %s, want degraded: a 29%% fill drop is parser rot", v.Status)
	}
	var found bool
	for _, r := range v.Reasons {
		if r.Code == "field_fill_drop" && r.Field == FieldLocation {
			found = true
			if r.Before <= r.After {
				t.Errorf("reason should show a decline, got before=%.2f after=%.2f", r.Before, r.After)
			}
		}
	}
	if !found {
		t.Errorf("no location fill-drop reason in %v", v.Reasons)
	}
}

// An absolute floor would pass this: 71% completeness looks fine in isolation.
// Only the relative comparison sees the breakage.
func TestAbsoluteFloorWouldMissWhatRelativeCatches(t *testing.T) {
	baseline := full(200)
	current := metrics(200, 200, map[string]int{
		FieldCompany: 200, FieldLocation: 142, FieldApplyURL: 200, FieldLanguage: 200,
	})
	if got := current.FillRate(FieldLocation); got < 0.7 {
		t.Fatalf("fixture wrong: fill rate %.2f", got)
	}
	if Compare(current, baseline).Status != StatusDegraded {
		t.Error("relative comparison failed to catch a drop an absolute floor would miss")
	}
}

func TestYieldDropIsCaught(t *testing.T) {
	baseline := full(200)
	current := metrics(200, 150, map[string]int{
		FieldCompany: 150, FieldLocation: 150, FieldApplyURL: 150, FieldLanguage: 150,
	})
	v := Compare(current, baseline)
	if v.Status != StatusDegraded {
		t.Fatalf("status = %s, want degraded", v.Status)
	}
	var found bool
	for _, r := range v.Reasons {
		if r.Code == "yield_drop" {
			found = true
		}
	}
	if !found {
		t.Errorf("no yield_drop reason in %v", v.Reasons)
	}
}

// A field the source never provides cannot rot. Salary is the real case: most
// boards omit it entirely, and alerting on that would fire forever.
func TestFieldTheSourceNeverProvidesDoesNotAlert(t *testing.T) {
	baseline := metrics(200, 200, map[string]int{
		FieldCompany: 200, FieldLocation: 200, FieldApplyURL: 200, FieldLanguage: 2,
	})
	current := metrics(200, 200, map[string]int{
		FieldCompany: 200, FieldLocation: 200, FieldApplyURL: 200, FieldLanguage: 0,
	})
	if v := Compare(current, baseline); v.Status != StatusHealthy {
		t.Errorf("status = %s: a field at a 1%% baseline must not alert (%v)", v.Status, v.Reasons)
	}
}

// "We cannot tell" must never be reported as "fine".
func TestSmallSampleIsUnknownNotHealthy(t *testing.T) {
	for _, tc := range []struct{ cur, base int }{{5, 200}, {200, 5}, {1, 1}} {
		v := Compare(full(tc.cur), full(tc.base))
		if v.Status != StatusUnknown {
			t.Errorf("current=%d baseline=%d: status = %s, want unknown", tc.cur, tc.base, v.Status)
		}
		if len(v.Reasons) == 0 || v.Reasons[0].Code != "insufficient_sample" {
			t.Errorf("expected an insufficient_sample reason, got %v", v.Reasons)
		}
	}
}

// A small wobble is noise, not a regression. Alerting on it trains people to
// ignore the alert.
func TestSmallFluctuationIsNotDegraded(t *testing.T) {
	baseline := full(500)
	current := metrics(500, 495, map[string]int{
		FieldCompany: 500, FieldLocation: 490, FieldApplyURL: 500, FieldLanguage: 500,
	})
	if v := Compare(current, baseline); v.Status != StatusHealthy {
		t.Errorf("status = %s, want healthy for a 1-2%% wobble (%v)", v.Status, v.Reasons)
	}
}

// An improving source must never be flagged.
func TestImprovementIsNotDegraded(t *testing.T) {
	baseline := metrics(200, 150, map[string]int{
		FieldCompany: 150, FieldLocation: 100, FieldApplyURL: 150, FieldLanguage: 150,
	})
	if v := Compare(full(200), baseline); v.Status != StatusHealthy {
		t.Errorf("status = %s, want healthy when everything improved (%v)", v.Status, v.Reasons)
	}
}

// One bad evaluation must not take a source offline: a transient blip would
// silently cost corpus coverage.
func TestQuarantineRequiresSustainedDegradation(t *testing.T) {
	for i := 0; i < ConsecutiveToQuarantine; i++ {
		if ShouldQuarantine(i) {
			t.Errorf("quarantined after %d degraded evaluations, threshold is %d",
				i, ConsecutiveToQuarantine)
		}
	}
	if !ShouldQuarantine(ConsecutiveToQuarantine) {
		t.Errorf("did not quarantine at %d degraded evaluations", ConsecutiveToQuarantine)
	}
}

func TestRatesHandleZeroPostings(t *testing.T) {
	m := metrics(0, 0, nil)
	if m.Yield() != 0 || m.FillRate(FieldCompany) != 0 {
		t.Error("zero postings should give zero rates, not a divide-by-zero")
	}
}

func TestCompareIsDeterministic(t *testing.T) {
	base := full(200)
	cur := metrics(200, 160, map[string]int{
		FieldCompany: 200, FieldLocation: 120, FieldApplyURL: 200, FieldLanguage: 200,
	})
	first := Compare(cur, base)
	for i := 0; i < 3; i++ {
		again := Compare(cur, base)
		if again.Status != first.Status || len(again.Reasons) != len(first.Reasons) {
			t.Fatal("Compare is not deterministic")
		}
	}
}
