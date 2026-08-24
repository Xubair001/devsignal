package eval

import (
	"math"
	"testing"
)

func ranked(keys ...string) []Ranked {
	out := make([]Ranked, 0, len(keys))
	for _, k := range keys {
		out = append(out, Ranked{Key: k, Eligible: true})
	}
	return out
}

// The property the whole gate rests on: a perfect ordering scores 1, and any
// worse ordering scores less. If NDCG does not have that shape, a regression is
// undetectable.
func TestNDCGIsOneForThePerfectOrdering(t *testing.T) {
	rel := RelevanceMap{"a": 3, "b": 2, "c": 1, "d": 0}

	perfect := NDCG(ranked("a", "b", "c", "d"), rel, 10)
	if math.Abs(perfect-1.0) > 1e-9 {
		t.Errorf("perfect ordering scored %.6f, want 1", perfect)
	}

	reversed := NDCG(ranked("d", "c", "b", "a"), rel, 10)
	if reversed >= perfect {
		t.Errorf("reversed ordering scored %.4f, not below perfect %.4f", reversed, perfect)
	}
}

// Rank position has to matter, or the metric cannot tell a good feed from a bag
// of the same postings.
func TestNDCGDiscountsByRank(t *testing.T) {
	rel := RelevanceMap{"good": 3, "x": 0, "y": 0, "z": 0}
	first := NDCG(ranked("good", "x", "y", "z"), rel, 10)
	last := NDCG(ranked("x", "y", "z", "good"), rel, 10)
	if first <= last {
		t.Errorf("relevant-first %.4f did not beat relevant-last %.4f", first, last)
	}
}

// Graded gain: moving an excellent posting up must be worth more than moving a
// marginal one up by the same distance.
func TestNDCGRewardsHigherGradesMore(t *testing.T) {
	rel := RelevanceMap{"excellent": 3, "marginal": 1}
	excellentFirst := NDCG(ranked("excellent", "marginal"), rel, 10)
	marginalFirst := NDCG(ranked("marginal", "excellent"), rel, 10)
	if excellentFirst <= marginalFirst {
		t.Errorf("excellent-first %.4f did not beat marginal-first %.4f",
			excellentFirst, marginalFirst)
	}
}

// A persona with nothing judged relevant has an undefined NDCG. Returning 0 would
// silently drag the mean down for a persona the label set says nothing about.
func TestNDCGIsUndefinedWithNoRelevantJudgements(t *testing.T) {
	got := NDCG(ranked("a", "b"), RelevanceMap{"a": 0, "b": 0}, 10)
	if !math.IsNaN(got) {
		t.Errorf("NDCG with no relevant judgements = %v, want NaN so the caller can skip it", got)
	}
}

// The cutoff must actually bind: relevance below it cannot rescue the score.
func TestNDCGRespectsTheCutoff(t *testing.T) {
	rel := RelevanceMap{"deep": 3}
	keys := make([]string, 0, 12)
	for range 11 {
		keys = append(keys, "filler")
	}
	keys = append(keys, "deep")

	if got := NDCG(ranked(keys...), rel, 10); got != 0 {
		t.Errorf("a relevant posting at rank 12 scored %.4f at k=10, want 0", got)
	}
}

// Precision divides by k, not by the number returned. A system that returns three
// postings and gets all three right has not delivered on a promise of seven.
func TestPrecisionDividesByKNotByWhatWasReturned(t *testing.T) {
	rel := RelevanceMap{"a": 3, "b": 3, "c": 3}
	got := PrecisionAtK(ranked("a", "b", "c"), rel, 7)
	if math.Abs(got-3.0/7.0) > 1e-9 {
		t.Errorf("precision = %.4f, want 3/7 — dividing by 3 would hide a short feed", got)
	}
}

// Marginal matches are not what the product promises when it shows seven roles.
func TestPrecisionCountsOnlyGoodAndAbove(t *testing.T) {
	rel := RelevanceMap{"marginal": 1, "good": 2, "excellent": 3}
	got := PrecisionAtK(ranked("marginal", "good", "excellent"), rel, 3)
	if math.Abs(got-2.0/3.0) > 1e-9 {
		t.Errorf("precision = %.4f, want 2/3 (marginal must not count)", got)
	}
}

// Coverage asks a recall question about retrieval, so only judged-relevant
// postings form the denominator — an irrelevant posting retrieval skipped is not
// a miss.
func TestCoverageCountsOnlyRelevantPostings(t *testing.T) {
	rel := RelevanceMap{"hit": 3, "miss": 2, "ignored": 0}
	found, total := Coverage(map[string]bool{"hit": true}, rel)
	if total != 2 {
		t.Errorf("denominator = %d, want 2 (the irrelevant posting must not count)", total)
	}
	if found != 1 {
		t.Errorf("found = %d, want 1", found)
	}
}

// The mean is over personas, not judgements: a persona with 40 labels must not
// outvote one with 8, because personas represent users.
func TestAggregateAveragesOverPersonasNotJudgements(t *testing.T) {
	m := Aggregate([]PersonaMetrics{
		{PersonaID: "many", NDCG10: 1.0, Precision7: 1.0, CoverageFound: 40, CoverageTotal: 40},
		{PersonaID: "few", NDCG10: 0.0, Precision7: 0.0, CoverageFound: 0, CoverageTotal: 8},
	})
	if math.Abs(m.NDCG10-0.5) > 1e-9 {
		t.Errorf("mean NDCG = %.4f, want 0.5 — an unweighted mean over personas", m.NDCG10)
	}
	// Coverage IS pooled, because it is a count of postings rather than a per-user
	// quality score.
	if math.Abs(m.Coverage-40.0/48.0) > 1e-9 {
		t.Errorf("coverage = %.4f, want 40/48 pooled", m.Coverage)
	}
}

func TestAggregateSkipsUndefinedPersonas(t *testing.T) {
	m := Aggregate([]PersonaMetrics{
		{PersonaID: "scored", NDCG10: 0.8, Precision7: 0.5},
		{PersonaID: "undefined", Skipped: true},
	})
	if m.PersonasScored != 1 || m.PersonasSkipped != 1 {
		t.Errorf("scored=%d skipped=%d, want 1 and 1", m.PersonasScored, m.PersonasSkipped)
	}
	if math.Abs(m.NDCG10-0.8) > 1e-9 {
		t.Errorf("mean NDCG = %.4f; a skipped persona must not drag it down", m.NDCG10)
	}
}

// An eligibility false positive is a correctness bug, so it fails the gate
// regardless of how good the ranking looks.
func TestEligibilityFalsePositiveFailsRegardlessOfRanking(t *testing.T) {
	base := Baseline{NDCG10: 0.5}
	perfectButLeaky := Metrics{NDCG10: 1.0, EligibilityFP: 1}
	bad, why := perfectButLeaky.Regressed(base)
	if !bad {
		t.Error("an eligibility false positive did not fail the gate")
	}
	if why == "" {
		t.Error("a failing gate must say why")
	}
}

// The tolerance exists so unrelated refactors do not turn the gate red; a gate
// nobody trusts is worse than none. But it must still catch a real drop.
func TestRegressionToleranceCatchesRealDropsAndIgnoresNoise(t *testing.T) {
	base := Baseline{NDCG10: 0.700}

	noise := Metrics{NDCG10: 0.700 - RegressionTolerance/2}
	if bad, _ := noise.Regressed(base); bad {
		t.Error("a sub-tolerance movement failed the gate")
	}

	real := Metrics{NDCG10: 0.700 - RegressionTolerance*2}
	if bad, _ := real.Regressed(base); !bad {
		t.Error("a drop beyond tolerance did not fail the gate")
	}

	improvement := Metrics{NDCG10: 0.85}
	if bad, _ := improvement.Regressed(base); bad {
		t.Error("an improvement failed the gate")
	}
}

// The cutoffs are product promises, not tuning knobs: 7 is what the daily digest
// shows. A silent change to them would make every historical number
// incomparable.
func TestCutoffsMatchTheProductPromise(t *testing.T) {
	if PrecisionCutoff != 7 {
		t.Errorf("PrecisionCutoff = %d; the product promises 7 a day", PrecisionCutoff)
	}
	if NDCGCutoff != 10 {
		t.Errorf("NDCGCutoff = %d, want 10", NDCGCutoff)
	}
}
