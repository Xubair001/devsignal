package ghostrisk

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time { return now.AddDate(0, 0, -d) }

func base() Signals {
	return Signals{
		FirstSeenAt:    daysAgo(3),
		LastVerifiedAt: now,
		HasApplyMethod: true,
	}
}

func TestFreshPostingIsNormal(t *testing.T) {
	got := Assess(base(), now)
	if got.Band != BandNormal {
		t.Errorf("band = %s, want normal (reasons: %v)", got.Band, got.Reasons)
	}
	if len(got.Reasons) != 0 {
		t.Errorf("a clean posting should have no reasons, got %v", got.Reasons)
	}
}

func TestRepeatedRefreshRaisesTheBand(t *testing.T) {
	// One refresh is ordinary housekeeping and must not flag anything.
	s := base()
	s.RepostCount = 1
	if got := Assess(s, now); got.Band != BandNormal {
		t.Errorf("a single refresh flagged as %s; that is ordinary", got.Band)
	}

	s.RepostCount = repostElevated
	if got := Assess(s, now); got.Band != BandElevated {
		t.Errorf("band = %s, want elevated at %d refreshes", got.Band, repostElevated)
	}

	s.RepostCount = repostHigh
	got := Assess(s, now)
	if got.Band != BandHigh {
		t.Errorf("band = %s, want high at %d refreshes", got.Band, repostHigh)
	}
	if !hasCode(got, "repeated_refresh") {
		t.Errorf("missing repeated_refresh reason: %v", got.Reasons)
	}
}

// Relative beats absolute: a long-open posting is unremarkable at a company that
// always takes a long time to hire.
func TestCompanyBaselineBeatsAbsoluteAge(t *testing.T) {
	s := base()
	s.FirstSeenAt = daysAgo(90)
	s.CompanyMedianDaysToClose = 95 // this company is simply slow

	if got := Assess(s, now); got.Band != BandNormal {
		t.Errorf("band = %s: 90 days is normal when the company median is 95 (%v)",
			got.Band, got.Reasons)
	}

	// Same age, a company that normally fills in a fortnight.
	s.CompanyMedianDaysToClose = 14
	got := Assess(s, now)
	if got.Band == BandNormal {
		t.Error("90 days should be flagged when the company median is 14")
	}
	if !hasCodePrefix(got, "open_") {
		t.Errorf("missing an age reason: %v", got.Reasons)
	}
}

// An unknown baseline must not manufacture suspicion; it falls back to absolutes.
func TestUnknownBaselineFallsBackToAbsoluteAge(t *testing.T) {
	s := base()
	s.CompanyMedianDaysToClose = 0

	s.FirstSeenAt = daysAgo(30)
	if got := Assess(s, now); got.Band != BandNormal {
		t.Errorf("30 days with no baseline = %s, want normal", got.Band)
	}
	s.FirstSeenAt = daysAgo(staleDays)
	if got := Assess(s, now); got.Band != BandElevated {
		t.Errorf("%d days with no baseline = %s, want elevated", staleDays, got.Band)
	}
	// Absolute age alone must NOT reach high: without a company baseline a long
	// hire is genuinely ambiguous, and wrongly flagging a real opening costs the
	// user an opportunity.
	s.FirstSeenAt = daysAgo(veryStaleDays)
	if got := Assess(s, now); got.Band != BandElevated {
		t.Errorf("%d days with no baseline = %s, want elevated (age alone is weak evidence)",
			veryStaleDays, got.Band)
	}

	// But a long-open posting that is ALSO being refreshed corroborates.
	s.RepostCount = repostElevated
	if got := Assess(s, now); got.Band != BandHigh {
		t.Errorf("age plus refreshes = %s, want high", got.Band)
	}
}

func TestNoApplyRouteIsASignal(t *testing.T) {
	s := base()
	s.HasApplyMethod = false
	got := Assess(s, now)
	if got.Band != BandElevated {
		t.Errorf("band = %s, want elevated when there is nowhere to apply", got.Band)
	}
	if !hasCode(got, "no_apply_route") {
		t.Errorf("missing no_apply_route: %v", got.Reasons)
	}
}

func TestSignalsCompound(t *testing.T) {
	s := base()
	s.RepostCount = repostElevated
	s.FirstSeenAt = daysAgo(veryStaleDays)
	s.HasApplyMethod = false
	got := Assess(s, now)
	if got.Band != BandHigh {
		t.Errorf("band = %s, want high when several signals stack", got.Band)
	}
	if len(got.Reasons) < 3 {
		t.Errorf("expected a reason per signal, got %v", got.Reasons)
	}
}

// The reasons are what the user reads, so they must be legible rather than
// carrying an unsubstituted format placeholder.
func TestReasonsAreRenderedText(t *testing.T) {
	s := base()
	s.RepostCount = 3
	s.FirstSeenAt = daysAgo(200)
	s.HasApplyMethod = false
	for _, r := range Assess(s, now).Reasons {
		if r.Code == "" || r.Detail == "" {
			t.Errorf("incomplete reason: %+v", r)
		}
		if strings.Contains(r.Detail, "%d") || strings.Contains(r.Detail, "%s") {
			t.Errorf("unsubstituted placeholder in %q", r.Detail)
		}
	}
}

// Pure and clock-injected: the same inputs must always give the same answer.
func TestAssessIsDeterministic(t *testing.T) {
	s := base()
	s.RepostCount = 2
	first := Assess(s, now)
	for i := 0; i < 3; i++ {
		again := Assess(s, now)
		if again.Band != first.Band || len(again.Reasons) != len(first.Reasons) {
			t.Fatal("Assess is not deterministic")
		}
	}
}

func TestDaysOpenNeverNegative(t *testing.T) {
	// Clock skew between us and the database must not produce a negative age.
	if got := DaysOpen(now.AddDate(0, 0, 5), now); got != 0 {
		t.Errorf("DaysOpen with a future first_seen = %d, want 0", got)
	}
	if got := DaysOpen(daysAgo(10), now); got != 10 {
		t.Errorf("DaysOpen = %d, want 10", got)
	}
}

func hasCode(a Assessment, code string) bool {
	for _, r := range a.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

func hasCodePrefix(a Assessment, prefix string) bool {
	for _, r := range a.Reasons {
		if strings.HasPrefix(r.Code, prefix) {
			return true
		}
	}
	return false
}
