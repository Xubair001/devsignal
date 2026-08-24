package eval

import (
	"math"
	"sort"
)

// The four metrics, and why each one is here rather than a more familiar
// alternative:
//
//   - NDCG@10 rewards putting the most relevant postings highest and discounts
//     by rank, which is what a feed does. Graded, so it uses all four relevance
//     levels rather than collapsing them to relevant/not.
//   - Precision@7 is measured at 7 because that is the number the product
//     promises daily. Measuring at 10 or 20 would flatter a list nobody reads
//     that far down.
//   - Coverage is the metric teams forget and it bounds everything downstream: a
//     perfect scorer cannot rank a posting retrieval never returned. It is also
//     the least circular of the four, because it asks a recall question the
//     scorer cannot influence.
//   - Eligibility false positives must be exactly 0. A hard gate that admits an
//     ineligible role is a correctness bug, not a metric to trade off.

// Cut-offs, named so a change to them is visible in a diff rather than buried in
// a call site.
const (
	NDCGCutoff      = 10
	PrecisionCutoff = 7
	// relevantThreshold is the relevance level at which a posting counts as a hit
	// for Precision@7. 2 = "good": marginal matches are not what the product
	// promises when it shows someone seven roles.
	relevantThreshold = 2
)

// Ranked is one scored posting in the order the system produced.
type Ranked struct {
	// Key is the stable (ats_type, ats_job_id) identity.
	Key string
	// Eligible reports whether the gate admitted it. Ineligible postings must not
	// appear in a ranked list at all; carrying the flag lets the harness detect
	// it if one does.
	Eligible bool
}

// RelevanceMap is judgement lookup for one persona.
type RelevanceMap map[string]int

// NDCG computes normalized discounted cumulative gain at k.
//
// Gain is 2^rel - 1, the standard graded form, so the distance between "excellent"
// and "good" is larger than between "good" and "marginal" — which matches how a
// user experiences a feed.
//
// Unjudged postings count as relevance 0. That is the conventional choice and it
// has a known bias: a system that surfaces a genuinely great posting nobody
// labelled is punished for it. It is recorded here rather than hidden because it
// is the main reason a rising NDCG on a fixed label set does not prove the product
// improved.
func NDCG(ranked []Ranked, rel RelevanceMap, k int) float64 {
	if k <= 0 || len(ranked) == 0 {
		return 0
	}
	dcg := 0.0
	for i, r := range ranked {
		if i >= k {
			break
		}
		dcg += gain(rel[r.Key]) / math.Log2(float64(i+2))
	}

	ideal := idealDCG(rel, k)
	if ideal == 0 {
		// No judged-relevant postings for this persona: NDCG is undefined rather
		// than 0. Returning 0 would drag the mean down for a persona the label set
		// simply says nothing about, so the caller skips these.
		return math.NaN()
	}
	return dcg / ideal
}

// idealDCG is the DCG of the best possible ordering of the judged set.
func idealDCG(rel RelevanceMap, k int) float64 {
	grades := make([]int, 0, len(rel))
	for _, g := range rel {
		if g > 0 {
			grades = append(grades, g)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))

	ideal := 0.0
	for i, g := range grades {
		if i >= k {
			break
		}
		ideal += gain(g) / math.Log2(float64(i+2))
	}
	return ideal
}

func gain(relevance int) float64 {
	if relevance <= 0 {
		return 0
	}
	return math.Pow(2, float64(relevance)) - 1
}

// PrecisionAtK is the fraction of the top k that are judged relevant at or above
// relevantThreshold.
//
// Divided by k, not by len(ranked). A system that returns three postings and gets
// all three right has not delivered on a promise of seven, and dividing by the
// smaller number would hide that.
func PrecisionAtK(ranked []Ranked, rel RelevanceMap, k int) float64 {
	if k <= 0 {
		return 0
	}
	hits := 0
	for i, r := range ranked {
		if i >= k {
			break
		}
		if rel[r.Key] >= relevantThreshold {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// Coverage is the fraction of judged-relevant postings that retrieval returned at
// all, regardless of where they ranked.
//
// retrieved is every key stage 1 produced, not just the top k: the question is
// whether the scorer ever had the chance to rank it.
func Coverage(retrieved map[string]bool, rel RelevanceMap) (found, total int) {
	for key, grade := range rel {
		if grade < relevantThreshold {
			continue
		}
		total++
		if retrieved[key] {
			found++
		}
	}
	return found, total
}

// Metrics is one harness run.
type Metrics struct {
	NDCG10     float64
	Precision7 float64
	// Coverage is judged-relevant postings retrieval returned, over all such
	// postings.
	Coverage        float64
	CoverageFound   int
	CoverageTotal   int
	EligibilityFP   int
	PersonasScored  int
	PersonasSkipped int
	JudgementsUsed  int
	// PerPersona is kept so a regression can be attributed rather than merely
	// observed. A mean that moved is not actionable; knowing which persona moved
	// is.
	PerPersona []PersonaMetrics
}

// PersonaMetrics is one persona's contribution.
type PersonaMetrics struct {
	PersonaID     string
	NDCG10        float64
	Precision7    float64
	CoverageFound int
	CoverageTotal int
	EligibilityFP int
	Returned      int
	Skipped       bool
}

// Aggregate combines per-persona results into the run's metrics.
//
// Unweighted mean over personas, not over judgements. A persona with 40 labels
// must not count five times a persona with 8: personas represent users, and users
// are what the product serves.
func Aggregate(per []PersonaMetrics) Metrics {
	m := Metrics{PerPersona: per}
	var ndcgSum, precSum float64
	for _, p := range per {
		m.EligibilityFP += p.EligibilityFP
		m.CoverageFound += p.CoverageFound
		m.CoverageTotal += p.CoverageTotal
		if p.Skipped {
			m.PersonasSkipped++
			continue
		}
		m.PersonasScored++
		ndcgSum += p.NDCG10
		precSum += p.Precision7
	}
	if m.PersonasScored > 0 {
		m.NDCG10 = ndcgSum / float64(m.PersonasScored)
		m.Precision7 = precSum / float64(m.PersonasScored)
	}
	if m.CoverageTotal > 0 {
		m.Coverage = float64(m.CoverageFound) / float64(m.CoverageTotal)
	}
	return m
}

// RegressionTolerance is how far NDCG@10 may fall before CI fails.
//
// The label set is small and the corpus is a few hundred postings, so a metric
// that moves by less than this is noise from tie-breaking and rounding rather
// than a real change. Set it to 0 and every unrelated refactor turns the gate
// red, which trains people to ignore it — a gate nobody trusts is worse than none.
const RegressionTolerance = 0.01

// Regressed reports whether a run has fallen below its baseline beyond noise.
//
// Eligibility false positives are checked separately and absolutely: there is no
// tolerance for a hard gate admitting an ineligible role.
func (m Metrics) Regressed(b Baseline) (bool, string) {
	if m.EligibilityFP > 0 {
		return true, "the eligibility gate admitted ineligible postings; that is a correctness bug, not a metric"
	}
	if m.NDCG10 < b.NDCG10-RegressionTolerance {
		return true, "NDCG@10 regressed beyond tolerance"
	}
	return false, ""
}
