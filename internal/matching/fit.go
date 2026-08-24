package matching

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/Xubair001/devsignal/internal/normalize"
)

// WeightsVersion identifies this set of factors and weights.
//
// It is part of the fit_score primary key, so bumping it is what invalidates
// cached scores. Change a weight without bumping this and users keep seeing the
// old number — the failure is silent, which is why it is called out here rather
// than in a comment somewhere further down.
const WeightsVersion = "w1"

// Factor names. Displayed to users and stored in the breakdown, so they are part
// of the contract.
const (
	FactorRequiredSkills  = "required_skills"
	FactorSemantic        = "semantic"
	FactorSeniority       = "seniority"
	FactorPreferredSkills = "preferred_skills"
	FactorDomain          = "domain"
	FactorCompensation    = "compensation"
)

// Weights are the blueprint §19 weights. They must sum to 1.
//
// Linear and monotone, deliberately. A multiplied form collapses the usable
// range — six bounded factors at 0.9 give 0.53, so an excellent match reports as
// 53/100 — and no factor in a product has a fixed contribution, which means the
// per-factor breakdown could not be derived from it at all. Keeping the blend
// linear is what makes the displayed explanation faithful arithmetic rather than
// a plausible story told after the fact.
var Weights = map[string]float64{
	FactorRequiredSkills:  0.35,
	FactorSemantic:        0.20,
	FactorSeniority:       0.15,
	FactorPreferredSkills: 0.10,
	FactorDomain:          0.10,
	FactorCompensation:    0.10,
}

// factorOrder fixes the display and storage order. Map iteration order would
// make the stored breakdown differ run to run for an identical score.
var factorOrder = []string{
	FactorRequiredSkills,
	FactorSemantic,
	FactorSeniority,
	FactorPreferredSkills,
	FactorDomain,
	FactorCompensation,
}

// weightSum is asserted at init rather than documented, because a weight set that
// does not sum to 1 produces scores on a different scale than everything already
// cached, and nothing else would notice.
func init() {
	var total float64
	for _, w := range Weights {
		total += w
	}
	if math.Abs(total-1.0) > 1e-9 {
		panic(fmt.Sprintf("matching: weights sum to %v, must be 1", total))
	}
	if len(factorOrder) != len(Weights) {
		panic("matching: factorOrder and Weights disagree on the factor set")
	}
	for _, name := range factorOrder {
		if _, ok := Weights[name]; !ok {
			panic("matching: factorOrder names an unknown factor " + name)
		}
	}
}

// FactorScore is one term of the sum, kept as the arithmetic that produced it.
type FactorScore struct {
	Factor string `json:"factor"`
	// Weight is the configured weight for this factor, or 0 when unavailable.
	Weight float64 `json:"weight"`
	// Value is f_i in [0,1].
	Value float64 `json:"value"`
	// Contribution is Weight * Value * 100, the points this factor put on the
	// board. "+29 of 35 from required skills" comes from here.
	Contribution float64 `json:"contribution"`
	// MaxContribution is Weight * 100: the most this factor could have added.
	MaxContribution float64 `json:"max_contribution"`
	// Available is false when there was no observable data for this factor. It
	// then contributes nothing AND removes its weight from the achievable
	// maximum, rather than scoring 0 (which would punish the posting for our
	// missing extraction) or 0.5 (which would reward missing data).
	Available bool `json:"available"`
	// Reason explains an unavailable factor, or qualifies a low value.
	Reason string `json:"reason,omitempty"`
}

// Fit is the stage-1 result.
type Fit struct {
	// Score is the points actually earned, 0-100. Shown as a BAND plus the
	// breakdown, never as a bare percentage — it is not a probability, and
	// presenting it as one is the invented-signal failure hard rule 3 exists to
	// prevent.
	Score   int           `json:"score"`
	Factors []FactorScore `json:"factors"`
	// MaxPossible is the points that were achievable given what could be observed.
	// Below 100 whenever a factor had no data.
	//
	// This pair — earned out of achievable — replaced an earlier design that
	// renormalized the remaining weights up to 1. Renormalizing created a perverse
	// incentive that two tests caught immediately: a posting nothing could be
	// extracted from, whose single legible factor happened to match, scored 100
	// out of 100 and displayed as a Strong fit on the strength of seniority alone;
	// and a user with no skills listed scored HIGHER than the same user after
	// adding one skill that matched half the requirements. Any scheme that
	// redistributes missing weight has that property, because dropping a factor
	// that would have scored low raises the total. Reporting "72 of a possible 90,
	// pay was not disclosed" removes it and is what a person would say.
	MaxPossible int `json:"max_possible"`
	// WeightsVersion is carried so a stored explanation can be reproduced.
	WeightsVersion string `json:"weights_version"`
}

// Coverage is the fraction of the model that could be evaluated, 0-1. It is the
// confidence attached to the score, and it gates the band.
func (f Fit) Coverage() float64 {
	return float64(f.MaxPossible) / 100
}

// Ratio is the earned fraction of what was achievable. This, not the raw score,
// is what the band reads: a score of 72 out of 90 is a better match than 72 out
// of 100, and the user should be told the better thing.
func (f Fit) Ratio() float64 {
	if f.MaxPossible <= 0 {
		return 0
	}
	return float64(f.Score) / float64(f.MaxPossible)
}

// Band is the pre-calibration display. A percentile replaces it only once
// expected calibration error has been measured on held-out outcome data
// (blueprint §20); until then a number implying a probability is not something we
// have earned the right to show.
type Band string

const (
	BandStrong  Band = "Strong fit"
	BandWorth   Band = "Worth a look"
	BandStretch Band = "Stretch"
	// BandInsufficient is shown when too little of the model could be evaluated
	// for any verdict to mean anything. It is a distinct band rather than
	// "Stretch", because "we could not read enough about this role" and "you are
	// a weak match for it" are different statements and only one of them is about
	// the user.
	BandInsufficient Band = "Not enough information"
)

// Band thresholds, applied to the earned RATIO rather than the raw score, so an
// undisclosed salary does not cap the verdict.
//
// Round numbers on purpose: they are a presentation choice made before any
// outcome data exists, and picking 0.714 would imply a calibration we have not
// done.
const (
	bandStrongRatio = 0.70
	bandWorthRatio  = 0.45

	// minEvidence is how much of the model must be evaluable before a verdict is
	// shown at all. 0.60 admits the common case the blueprint calls out — pay
	// undisclosed (0.10) and no preferred skills listed (0.10) still leaves 0.80 —
	// while refusing to call something a Strong fit on one or two legible factors.
	minEvidence = 0.60
)

// Band maps the result to what the user is shown.
func (f Fit) Band() Band {
	if f.Coverage() < minEvidence {
		return BandInsufficient
	}
	switch r := f.Ratio(); {
	case r >= bandStrongRatio:
		return BandStrong
	case r >= bandWorthRatio:
		return BandWorth
	default:
		return BandStretch
	}
}

// Summary is the headline: what was earned out of what was achievable.
//
// Separate from Explain so a caller can render one list of factor statements
// without a summary line mixed into it — they are different kinds of sentence and
// a UI wants them in different places.
func (f Fit) Summary() string {
	if f.MaxPossible <= 0 {
		return "not enough information about this role to score it"
	}
	if f.MaxPossible < 100 {
		return fmt.Sprintf("%d of a possible %d points (some factors could not be scored)",
			f.Score, f.MaxPossible)
	}
	return fmt.Sprintf("%d of 100 points", f.Score)
}

// Explain renders the breakdown as the lines a user reads, strongest first.
//
// Only factors that actually contributed are listed as contributions; the
// unavailable ones are reported separately, because "we could not read the
// required skills for this posting" and "you match none of them" look identical
// in a bare list and mean opposite things.
func (f Fit) Explain() []string {
	contributing := make([]FactorScore, 0, len(f.Factors))
	var missing []FactorScore
	for _, fs := range f.Factors {
		if fs.Available {
			contributing = append(contributing, fs)
		} else {
			missing = append(missing, fs)
		}
	}
	sort.SliceStable(contributing, func(i, j int) bool {
		return contributing[i].Contribution > contributing[j].Contribution
	})

	out := make([]string, 0, len(f.Factors))
	for _, fs := range contributing {
		line := fmt.Sprintf("%+.0f of %.0f from %s",
			fs.Contribution, fs.MaxContribution, label(fs.Factor))
		if fs.Reason != "" {
			line += " (" + fs.Reason + ")"
		}
		out = append(out, line)
	}
	for _, fs := range missing {
		out = append(out, fmt.Sprintf("%s not scored: %s", label(fs.Factor), fs.Reason))
	}
	return out
}

func label(factor string) string {
	return strings.ReplaceAll(factor, "_", " ")
}

// ComputeFit runs stage 1.
//
// Pure: no clock, no database, no network. Given the same profile, candidate and
// versions it returns the same number forever, which is the property that makes
// caching safe and the eval harness meaningful.
func ComputeFit(p Profile, c Candidate) Fit {
	raw := []FactorScore{
		requiredSkills(p, c),
		semantic(c),
		seniority(p, c),
		preferredSkills(p, c),
		domain(p, c),
		compensation(p, c),
	}

	// An unavailable factor contributes nothing and removes its weight from the
	// achievable maximum. It is emphatically NOT given a neutral 0.5, which would
	// reward a posting for the data we failed to extract from it, and NOT
	// redistributed over the others, which would mean removing information raises
	// the score. See the note on Fit.MaxPossible.
	out := make([]FactorScore, 0, len(raw))
	var score, maxScore float64

	for _, fs := range raw {
		if !fs.Available {
			fs.Weight = 0
			fs.Contribution = 0
			fs.MaxContribution = 0
			out = append(out, fs)
			continue
		}
		w := Weights[fs.Factor]
		fs.Weight = w
		fs.MaxContribution = w * 100
		fs.Contribution = w * fs.Value * 100
		score += fs.Contribution
		maxScore += fs.MaxContribution
		out = append(out, fs)
	}

	// Order for storage and display stability.
	slices.SortStableFunc(out, func(a, b FactorScore) int {
		return slices.Index(factorOrder, a.Factor) - slices.Index(factorOrder, b.Factor)
	})

	return Fit{
		Score:          int(math.Round(clamp(score, 0, 100))),
		MaxPossible:    int(math.Round(clamp(maxScore, 0, 100))),
		Factors:        out,
		WeightsVersion: WeightsVersion,
	}
}

// requiredSkills is coverage of the posting's must-haves by the user's skills.
//
// The denominator is the posting's requirements, not the user's skills: a user
// who knows fifty things and matches all five requirements is a full match, and
// dividing by their skill count would punish breadth.
func requiredSkills(p Profile, c Candidate) FactorScore {
	fs := FactorScore{Factor: FactorRequiredSkills}
	if len(c.RequiredSkills) == 0 {
		fs.Reason = "this posting's required skills have not been extracted yet"
		return fs
	}
	if len(p.Skills) == 0 {
		// The user's side is empty, which is about the profile rather than the
		// posting. Scoring it 0 would be honest but useless; it is more useful to
		// say the profile is incomplete.
		fs.Reason = "add skills to your profile to score this"
		return fs
	}
	matched := countOverlap(c.RequiredSkills, p.Skills)
	fs.Available = true
	fs.Value = float64(matched) / float64(len(c.RequiredSkills))
	fs.Reason = fmt.Sprintf("%d of %d required skills", matched, len(c.RequiredSkills))
	return fs
}

func preferredSkills(p Profile, c Candidate) FactorScore {
	fs := FactorScore{Factor: FactorPreferredSkills}
	if len(c.PreferredSkills) == 0 {
		fs.Reason = "this posting lists no preferred skills"
		return fs
	}
	if len(p.Skills) == 0 {
		fs.Reason = "add skills to your profile to score this"
		return fs
	}
	matched := countOverlap(c.PreferredSkills, p.Skills)
	fs.Available = true
	fs.Value = float64(matched) / float64(len(c.PreferredSkills))
	fs.Reason = fmt.Sprintf("%d of %d preferred skills", matched, len(c.PreferredSkills))
	return fs
}

// semantic rescales cosine similarity into [0,1].
//
// Cosine runs [-1,1] but on normalized text embeddings negative values mean
// "unrelated", not "opposite" — there is no such thing as an anti-job. So the
// floor is 0 and the useful range is compressed into the top: with the local
// lexical embedder, a reworded duplicate measured 0.68, a different engineering
// role 0.15, and an unrelated posting -0.00. Mapping 0.7 to a full 1.0 reflects
// that measurement rather than the theoretical range.
const semanticFullMatch = 0.7

func semantic(c Candidate) FactorScore {
	fs := FactorScore{Factor: FactorSemantic}
	if c.SemanticSimilarity == nil {
		fs.Reason = "no embedding available for this posting or your profile"
		return fs
	}
	fs.Available = true
	fs.Value = clamp(*c.SemanticSimilarity/semanticFullMatch, 0, 1)
	return fs
}

// seniority is closeness on the ordinal ladder.
//
// Asymmetric on purpose. A role one rung BELOW the user is a mild mismatch — they
// can do the work, it may pay less. A role one rung ABOVE is a stretch that may
// be worth applying to. Two rungs above is usually a waste of an application, and
// two below usually a waste of the user's career. Symmetric distance would rank
// those identically.
func seniority(p Profile, c Candidate) FactorScore {
	fs := FactorScore{Factor: FactorSeniority}

	want := p.Profile.SeniorityOrdinal
	have := c.Opportunity.SeniorityOrdinal
	if want == nil {
		fs.Reason = "set your seniority to score this"
		return fs
	}
	// Off-ladder ordinals are unknown, not clamped: the opportunity column permits
	// 0-9 and comparing 9 against a 1-6 profile scale is a category error.
	if have == nil || normalize.SeniorityLabel(have) == nil {
		fs.Reason = "this posting does not state a seniority level"
		return fs
	}

	diff := int(*have) - int(*want) // positive = posting is more senior
	fs.Available = true
	switch {
	case diff == 0:
		fs.Value = 1.0
	case diff == 1:
		fs.Value = 0.75 // a stretch worth seeing
		fs.Reason = "one level above you"
	case diff == -1:
		fs.Value = 0.6 // you can do it; it may pay less
		fs.Reason = "one level below you"
	case diff >= 2:
		fs.Value = 0.25
		fs.Reason = fmt.Sprintf("%d levels above you", diff)
	default:
		fs.Value = 0.2
		fs.Reason = fmt.Sprintf("%d levels below you", -diff)
	}
	return fs
}

// domain is role-family alignment.
//
// Binary rather than graded, because we have no measured similarity between
// families. Inventing one — deciding backend is 0.6 similar to platform — would
// be exactly the unobservable signal hard rule 3 forbids. A family edge table
// belongs in the skill ontology once it is built from data.
func domain(p Profile, c Candidate) FactorScore {
	fs := FactorScore{Factor: FactorDomain}

	targets := p.Profile.TargetRoleFamilies
	family := c.Opportunity.RoleFamily
	if len(targets) == 0 {
		fs.Reason = "set target role families to score this"
		return fs
	}
	if family == nil || *family == "" {
		fs.Reason = "this posting's role family could not be determined"
		return fs
	}
	fs.Available = true
	if containsFold(targets, *family) {
		fs.Value = 1.0
		fs.Reason = *family + " matches what you are targeting"
	} else {
		fs.Value = 0
		fs.Reason = fmt.Sprintf("%s, not one of your target families", *family)
	}
	return fs
}

// compensation is overlap of the disclosed band with the user's expectation.
//
// Unavailable whenever pay was not disclosed, or disclosed as an ESTIMATE, or in a
// currency and period we cannot compare without an fx rate we did not record.
// Never imputed: showing a guessed salary as the employer's is the specific
// dishonesty blueprint §3 names, and scoring against a guess is the same lie one
// step removed.
func compensation(p Profile, c Candidate) FactorScore {
	fs := FactorScore{Factor: FactorCompensation}
	o := c.Opportunity

	if o.SalaryIsEstimated {
		fs.Reason = "the pay shown for this role is an estimate, not the employer's figure"
		return fs
	}
	if o.SalaryMinMinor == nil && o.SalaryMaxMinor == nil {
		fs.Reason = "this employer did not disclose pay"
		return fs
	}
	floor := p.Profile.MinSalaryMinor
	if floor == nil || *floor <= 0 {
		fs.Reason = "set a minimum salary to score this"
		return fs
	}
	if !comparableCurrency(p.Profile, o) {
		fs.Reason = "the disclosed pay is in a different currency or period than your expectation"
		return fs
	}

	top := salaryCeiling(o)
	bottom := o.SalaryMinMinor
	if bottom == nil {
		bottom = top
	}

	fs.Available = true
	switch {
	case *bottom >= *floor:
		// The whole disclosed band clears the user's floor.
		fs.Value = 1.0
		fs.Reason = "the whole disclosed range meets your minimum"
	case *top < *floor:
		// Eligibility already excludes this when the floor is hard; reaching here
		// means it was not, so it scores zero rather than being hidden.
		fs.Value = 0
		fs.Reason = "the disclosed range is below your minimum"
	default:
		// Partial overlap: the fraction of the band that clears the floor.
		span := float64(*top - *bottom)
		if span <= 0 {
			fs.Value = 0
		} else {
			fs.Value = float64(*top-*floor) / span
		}
		fs.Reason = "part of the disclosed range meets your minimum"
	}
	return fs
}

// countOverlap counts how many of want appear in have.
func countOverlap(want, have []string) int {
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	var n int
	for _, w := range want {
		if _, ok := set[w]; ok {
			n++
		}
	}
	return n
}

func clamp(v, lo, hi float64) float64 {
	return math.Min(math.Max(v, lo), hi)
}
