package matching

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/store"
)

// ---------------------------------------------------------------- builders

// Skill ids in these fixtures stand in for real ontology ids; only identity
// matters to the coverage arithmetic.
const (
	skillGo       = "go"
	skillPostgres = "postgres"
	skillK8s      = "kubernetes"
	skillKafka    = "kafka"
	skillTF       = "terraform"
)

func prof(mut ...func(*Profile)) Profile {
	sen := int16(4) // senior
	cur, per := "EUR", "year"
	p := Profile{
		Profile: store.Profile{
			SeniorityOrdinal:   &sen,
			TargetRoleFamilies: []string{"backend"},
			SalaryCurrency:     &cur,
			SalaryPeriod:       &per,
		},
		Skills: []string{skillGo, skillPostgres, skillK8s},
	}
	for _, m := range mut {
		m(&p)
	}
	return p
}

func cand(mut ...func(*Candidate)) Candidate {
	sen := int16(4)
	fam := "backend"
	cur, per := "EUR", "year"
	sim := 0.7
	c := Candidate{
		Opportunity: store.Opportunity{
			SeniorityOrdinal: &sen,
			RoleFamily:       &fam,
			SalaryCurrency:   &cur,
			SalaryPeriod:     &per,
		},
		RequiredSkills:     []string{skillGo, skillPostgres},
		PreferredSkills:    []string{skillK8s},
		SemanticSimilarity: &sim,
	}
	for _, m := range mut {
		m(&c)
	}
	return c
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }
func i16(v int16) *int16     { return &v }
func str(v string) *string   { return &v }

// ---------------------------------------------------------------- invariants

// The weight set defines the scale every cached score sits on. init() panics if
// it does not sum to 1; this asserts the property directly so the reason is
// visible in test output rather than a startup crash.
func TestWeightsSumToOne(t *testing.T) {
	var total float64
	for _, w := range Weights {
		total += w
	}
	if math.Abs(total-1.0) > 1e-9 {
		t.Fatalf("weights sum to %v, want 1", total)
	}
}

// The explanation is only faithful if it adds up to the score shown. This is the
// property that makes the breakdown arithmetic rather than a story.
func TestBreakdownSumsToTheScore(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Profile
		c    Candidate
	}{
		{"everything available", prof(func(p *Profile) {
			p.Profile.MinSalaryMinor = i64(80000_00)
		}), cand(func(c *Candidate) {
			c.Opportunity.SalaryMinMinor = i64(90000_00)
			c.Opportunity.SalaryMaxMinor = i64(120000_00)
		})},
		{"no salary disclosed", prof(), cand()},
		{"no skills extracted", prof(), cand(func(c *Candidate) {
			c.RequiredSkills = nil
			c.PreferredSkills = nil
		})},
		{"partial match", prof(func(p *Profile) {
			p.Skills = []string{skillGo}
		}), cand()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ComputeFit(tc.p, tc.c)
			var sum float64
			for _, fs := range f.Factors {
				sum += fs.Contribution
			}
			if math.Abs(sum-float64(f.Score)) > 0.5 {
				t.Errorf("contributions sum to %.2f but score is %d", sum, f.Score)
			}
		})
	}
}

// The achievable maximum must equal the weight of what could actually be
// evaluated, so "+29 of 35" is measured against a bar that exists.
func TestMaxPossibleReflectsOnlyObservableFactors(t *testing.T) {
	f := ComputeFit(prof(), cand(func(c *Candidate) {
		c.RequiredSkills = nil     // 0.35 unobservable
		c.SemanticSimilarity = nil // 0.20 unobservable
	}))
	var maxTotal float64
	for _, fs := range f.Factors {
		maxTotal += fs.MaxContribution
	}
	if math.Abs(maxTotal-float64(f.MaxPossible)) > 0.5 {
		t.Errorf("factor maxima sum to %.2f but MaxPossible is %d", maxTotal, f.MaxPossible)
	}
	// compensation is also undisclosed in the default candidate, so 0.35+0.20+0.10
	// is unobservable and 0.35 remains.
	if f.MaxPossible != 35 {
		t.Errorf("MaxPossible = %d, want 35 (seniority 15 + preferred 10 + domain 10)",
			f.MaxPossible)
	}
	if f.Coverage() >= minEvidence {
		t.Errorf("coverage %.2f should be below the evidence floor", f.Coverage())
	}
	if f.Band() != BandInsufficient {
		t.Errorf("band = %q on %.0f%% coverage, want %q",
			f.Band(), f.Coverage()*100, BandInsufficient)
	}
}

// A full model must still be able to reach a perfect score, or the scale is wrong.
func TestAFullyObservedPerfectMatchReachesOneHundred(t *testing.T) {
	f := ComputeFit(
		prof(func(p *Profile) { p.Profile.MinSalaryMinor = i64(80000_00) }),
		cand(func(c *Candidate) {
			c.Opportunity.SalaryMinMinor = i64(90000_00)
			c.Opportunity.SalaryMaxMinor = i64(120000_00)
		}))
	if f.MaxPossible != 100 {
		t.Errorf("MaxPossible = %d with every factor observable, want 100", f.MaxPossible)
	}
	if f.Score != 100 {
		t.Errorf("score = %d for a perfect match on every factor, want 100", f.Score)
	}
	if f.Band() != BandStrong {
		t.Errorf("band = %q, want %q", f.Band(), BandStrong)
	}
}

// The blueprint's named case: undisclosed pay must not stop a strong verdict.
// That was the whole point of renormalizing, and it has to survive its removal.
func TestUndisclosedSalaryStillAllowsAStrongFit(t *testing.T) {
	f := ComputeFit(prof(), cand()) // default candidate discloses no salary
	if f.MaxPossible != 90 {
		t.Fatalf("MaxPossible = %d with pay undisclosed, want 90", f.MaxPossible)
	}
	if f.Band() != BandStrong {
		t.Errorf("band = %q for a perfect match with pay undisclosed, want %q (ratio %.2f)",
			f.Band(), BandStrong, f.Ratio())
	}
}

// A missing factor must not be scored as a zero (punishing the posting for our
// failure to extract it) nor as a neutral 0.5 (rewarding missing data).
func TestUnavailableFactorIsExcludedNotZeroedOrNeutral(t *testing.T) {
	full := ComputeFit(prof(), cand())

	// Same pairing, but the posting's required skills were never extracted.
	noSkills := ComputeFit(prof(), cand(func(c *Candidate) {
		c.RequiredSkills = nil
	}))

	var rs FactorScore
	for _, fs := range noSkills.Factors {
		if fs.Factor == FactorRequiredSkills {
			rs = fs
		}
	}
	if rs.Available {
		t.Fatal("required skills reported available with nothing extracted")
	}
	if rs.Contribution != 0 || rs.MaxContribution != 0 {
		t.Errorf("unavailable factor still contributed %.2f of %.2f",
			rs.Contribution, rs.MaxContribution)
	}
	if rs.Reason == "" {
		t.Error("an unavailable factor must say why")
	}
	// Removing a factor the posting matched fully must LOWER the score, never
	// raise it. The reverse was true under the old renormalizing design.
	if noSkills.Score >= full.Score {
		t.Errorf("dropping a fully-matched factor did not lower the score: %d vs %d",
			noSkills.Score, full.Score)
	}
}

// The specific trap: a posting we could extract nothing from must not outscore
// one we read fully and found a partial match in.
func TestUnreadablePostingDoesNotOutscoreAPartialMatch(t *testing.T) {
	partial := ComputeFit(prof(func(p *Profile) {
		p.Skills = []string{skillGo} // matches 1 of 2 required
	}), cand())

	unreadable := ComputeFit(prof(), cand(func(c *Candidate) {
		c.RequiredSkills = nil
		c.PreferredSkills = nil
		c.Opportunity.RoleFamily = nil
		c.SemanticSimilarity = nil
	}))

	if unreadable.Score > partial.Score {
		t.Errorf("a posting with nothing extracted scored %d, above a real partial match at %d",
			unreadable.Score, partial.Score)
	}
}

// Nothing observable at all means we know nothing. Zero with reasons is honest;
// any positive number would be invented.
func TestNothingObservableScoresZeroWithReasons(t *testing.T) {
	f := ComputeFit(Profile{}, Candidate{})
	if f.Score != 0 {
		t.Errorf("score = %d with no observable data, want 0", f.Score)
	}
	for _, fs := range f.Factors {
		if fs.Available {
			t.Errorf("factor %s claimed availability from nothing", fs.Factor)
		}
		if fs.Reason == "" {
			t.Errorf("factor %s gave no reason for being unscored", fs.Factor)
		}
	}
	if len(f.Explain()) != len(factorOrder) {
		t.Errorf("explanation lines = %d, want one per factor: %v", len(f.Explain()), f.Explain())
	}
	if f.MaxPossible != 0 {
		t.Errorf("MaxPossible = %d with nothing observable, want 0", f.MaxPossible)
	}
	if f.Band() != BandInsufficient {
		t.Errorf("band = %q, want %q", f.Band(), BandInsufficient)
	}
}

// Reproducibility is the whole basis for caching a score.
func TestFitIsReproducible(t *testing.T) {
	p, c := prof(), cand()
	first := ComputeFit(p, c)
	for range 50 {
		again := ComputeFit(p, c)
		if again.Score != first.Score {
			t.Fatalf("score varies between runs: %d then %d", first.Score, again.Score)
		}
		for i := range first.Factors {
			if again.Factors[i] != first.Factors[i] {
				t.Fatalf("factor %s varies between runs", first.Factors[i].Factor)
			}
		}
	}
}

// The stored breakdown must be in a fixed order, or an identical score produces a
// different JSON payload run to run.
func TestFactorOrderIsStable(t *testing.T) {
	f := ComputeFit(prof(), cand())
	for i, name := range factorOrder {
		if f.Factors[i].Factor != name {
			t.Errorf("position %d is %s, want %s", i, f.Factors[i].Factor, name)
		}
	}
}

// Monotonicity: matching strictly more required skills can never lower the score.
// A model that violates this cannot be explained honestly, because the breakdown
// would show a factor going up while the total went down.
func TestMoreMatchedSkillsNeverLowersTheScore(t *testing.T) {
	c := cand(func(c *Candidate) {
		c.RequiredSkills = []string{skillGo, skillPostgres, skillKafka, skillTF}
	})
	var prev int
	for n := range 5 {
		skills := []string{skillGo, skillPostgres, skillKafka, skillTF}[:n]
		f := ComputeFit(prof(func(p *Profile) { p.Skills = skills }), c)
		if f.Score < prev {
			t.Errorf("matching %d skills scored %d, below %d for fewer", n, f.Score, prev)
		}
		prev = f.Score
	}
}

// ---------------------------------------------------------------- factors

func TestSeniorityIsAsymmetric(t *testing.T) {
	// A role one level above is a stretch worth seeing; one level below is a
	// mild mismatch. Symmetric distance would rank them the same.
	above := ComputeFit(prof(), cand(func(c *Candidate) { c.Opportunity.SeniorityOrdinal = i16(5) }))
	below := ComputeFit(prof(), cand(func(c *Candidate) { c.Opportunity.SeniorityOrdinal = i16(3) }))
	if above.Score <= below.Score {
		t.Errorf("one level above scored %d, not above one level below at %d",
			above.Score, below.Score)
	}
	exact := ComputeFit(prof(), cand())
	if exact.Score <= above.Score {
		t.Errorf("an exact level match (%d) must beat a stretch (%d)", exact.Score, above.Score)
	}
}

// The opportunity column permits 0-9 while the profile ladder is 1-6. Comparing
// across them is a category error, so an off-ladder value is unknown.
func TestOffLadderSeniorityIsUnavailableNotADistance(t *testing.T) {
	f := ComputeFit(prof(), cand(func(c *Candidate) { c.Opportunity.SeniorityOrdinal = i16(9) }))
	for _, fs := range f.Factors {
		if fs.Factor == FactorSeniority && fs.Available {
			t.Errorf("seniority 9 was scored as a distance (value %.2f) against a 1-6 profile scale",
				fs.Value)
		}
	}
}

// Never impute a salary, and never score against an imputed one.
func TestEstimatedSalaryIsNotScored(t *testing.T) {
	f := ComputeFit(
		prof(func(p *Profile) { p.Profile.MinSalaryMinor = i64(80000_00) }),
		cand(func(c *Candidate) {
			c.Opportunity.SalaryMinMinor = i64(90000_00)
			c.Opportunity.SalaryIsEstimated = true
		}))
	for _, fs := range f.Factors {
		if fs.Factor == FactorCompensation && fs.Available {
			t.Error("an estimated salary was scored as if the employer had disclosed it")
		}
	}
}

// Comparing 60000 EUR/year against a 5000 USD/month floor is arithmetic on unlike
// things. Without a recorded fx rate the honest answer is not to score it.
func TestMismatchedCurrencyIsNotScored(t *testing.T) {
	f := ComputeFit(
		prof(func(p *Profile) {
			p.Profile.MinSalaryMinor = i64(80000_00)
			p.Profile.SalaryCurrency = str("USD")
		}),
		cand(func(c *Candidate) {
			c.Opportunity.SalaryMinMinor = i64(90000_00)
			c.Opportunity.SalaryCurrency = str("EUR")
		}))
	for _, fs := range f.Factors {
		if fs.Factor == FactorCompensation && fs.Available {
			t.Error("salaries in different currencies were compared without an fx rate")
		}
	}
}

func TestCompensationOverlap(t *testing.T) {
	score := func(min, max, floor int64) FactorScore {
		f := ComputeFit(
			prof(func(p *Profile) { p.Profile.MinSalaryMinor = i64(floor) }),
			cand(func(c *Candidate) {
				c.Opportunity.SalaryMinMinor = i64(min)
				c.Opportunity.SalaryMaxMinor = i64(max)
			}))
		for _, fs := range f.Factors {
			if fs.Factor == FactorCompensation {
				return fs
			}
		}
		t.Fatal("no compensation factor")
		return FactorScore{}
	}
	if fs := score(90000, 120000, 80000); fs.Value != 1.0 {
		t.Errorf("band entirely above the floor scored %.2f, want 1.0", fs.Value)
	}
	if fs := score(50000, 60000, 80000); fs.Value != 0 {
		t.Errorf("band entirely below the floor scored %.2f, want 0", fs.Value)
	}
	fs := score(70000, 90000, 80000)
	if fs.Value <= 0 || fs.Value >= 1 {
		t.Errorf("partial overlap scored %.2f, want strictly between 0 and 1", fs.Value)
	}
}

// Semantic similarity is rescaled against measured behaviour, not the theoretical
// [-1,1] range. Negative means unrelated, not opposite — there is no anti-job.
func TestSemanticRescaling(t *testing.T) {
	valueFor := func(sim float64) float64 {
		f := ComputeFit(prof(), cand(func(c *Candidate) { c.SemanticSimilarity = f64(sim) }))
		for _, fs := range f.Factors {
			if fs.Factor == FactorSemantic {
				return fs.Value
			}
		}
		return -1
	}
	if v := valueFor(-0.3); v != 0 {
		t.Errorf("negative similarity gave %.2f, want 0", v)
	}
	if v := valueFor(semanticFullMatch); v != 1 {
		t.Errorf("full-match similarity gave %.2f, want 1", v)
	}
	if v := valueFor(1.0); v != 1 {
		t.Errorf("similarity above the full-match point gave %.2f, want it clamped to 1", v)
	}
	if valueFor(0.35) <= 0 || valueFor(0.35) >= 1 {
		t.Error("mid similarity must land strictly between 0 and 1")
	}
}

// Role families have no measured similarity, so grading them would invent a
// signal. Binary until an ontology built from data says otherwise.
func TestDomainIsBinaryNotGuessed(t *testing.T) {
	match := ComputeFit(prof(), cand())
	miss := ComputeFit(prof(), cand(func(c *Candidate) { c.Opportunity.RoleFamily = str("marketing") }))
	for _, f := range []Fit{match, miss} {
		for _, fs := range f.Factors {
			if fs.Factor == FactorDomain && fs.Available && fs.Value != 0 && fs.Value != 1 {
				t.Errorf("domain scored %.2f; only 0 or 1 is derivable from observed data", fs.Value)
			}
		}
	}
}

// ---------------------------------------------------------------- display

func TestBandsCoverTheRange(t *testing.T) {
	for _, tc := range []struct {
		score int
		want  Band
	}{{100, BandStrong}, {70, BandStrong}, {69, BandWorth}, {45, BandWorth},
		{44, BandStretch}, {0, BandStretch}} {
		got := Fit{Score: tc.score, MaxPossible: 100}.Band()
		if got != tc.want {
			t.Errorf("score %d gave band %q, want %q", tc.score, got, tc.want)
		}
	}
}

// The band reads the RATIO, so the same raw score means different things
// depending on how much could be evaluated. 45 of 90 is a better match than 45
// of 100 and must not be reported as worse.
func TestBandReadsTheRatioNotTheRawScore(t *testing.T) {
	full := Fit{Score: 63, MaxPossible: 100}   // ratio 0.63
	partial := Fit{Score: 63, MaxPossible: 80} // ratio 0.79
	if full.Band() != BandWorth {
		t.Errorf("63 of 100 gave %q, want %q", full.Band(), BandWorth)
	}
	if partial.Band() != BandStrong {
		t.Errorf("63 of 80 gave %q, want %q", partial.Band(), BandStrong)
	}
}

// Thin evidence cannot produce a verdict, however well the legible parts matched.
func TestThinEvidenceCannotBeAStrongFit(t *testing.T) {
	perfectButBlind := Fit{Score: 15, MaxPossible: 15} // ratio 1.0, coverage 0.15
	if perfectButBlind.Band() != BandInsufficient {
		t.Errorf("a perfect match on 15%% of the model gave %q, want %q",
			perfectButBlind.Band(), BandInsufficient)
	}
}

// An unavailable factor and a zero-scoring one must read differently: "we could
// not read the required skills" and "you match none of them" mean opposite things.
func TestExplanationSeparatesUnavailableFromZero(t *testing.T) {
	unreadable := ComputeFit(prof(), cand(func(c *Candidate) { c.RequiredSkills = nil }))
	var found bool
	for _, line := range unreadable.Explain() {
		if line == "required skills not scored: this posting's required skills have not been extracted yet" {
			found = true
		}
	}
	if !found {
		t.Errorf("unavailable factor not reported as unscored: %v", unreadable.Explain())
	}

	zero := ComputeFit(prof(func(p *Profile) { p.Skills = []string{"cobol"} }), cand())
	for _, line := range zero.Explain() {
		if line == "required skills not scored: this posting's required skills have not been extracted yet" {
			t.Error("a genuine zero was reported as unavailable")
		}
	}
}

func TestExplanationIsOrderedByContribution(t *testing.T) {
	f := ComputeFit(prof(), cand())
	lines := f.Explain()
	if len(lines) < 2 {
		t.Fatal("expected several explanation lines")
	}
	// Explain returns factor statements only; the headline lives in Summary.
	var best FactorScore
	for _, fs := range f.Factors {
		if fs.Available && fs.Contribution > best.Contribution {
			best = fs
		}
	}
	if !strings.Contains(lines[0], label(best.Factor)) {
		t.Errorf("first explanation line %q does not name the largest contributor %q",
			lines[0], label(best.Factor))
	}
}

// ---------------------------------------------------------------- the split

// The rule with no exception: fit must not move because time passed.
func TestFitDoesNotDependOnTime(t *testing.T) {
	p, c := prof(), cand()
	// The candidate carries timestamps; none of them may reach the score.
	c.Opportunity.FirstSeenAt = tstamp(time.Now().Add(-90 * 24 * time.Hour))
	old := ComputeFit(p, c)

	c.Opportunity.FirstSeenAt = tstamp(time.Now())
	fresh := ComputeFit(p, c)

	if old.Score != fresh.Score {
		t.Fatalf("fit changed with the posting's age: %d vs %d — recency belongs in priority",
			old.Score, fresh.Score)
	}
}

// Priority is where time is allowed, and it must never be mistaken for a match.
func TestPriorityRewardsFreshnessWithoutTouchingFit(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fit := 60

	fresh := Priority(fit, PrioritySignals{FirstSeenAt: now}, now)
	old := Priority(fit, PrioritySignals{FirstSeenAt: now.Add(-30 * 24 * time.Hour)}, now)

	if fresh <= old {
		t.Errorf("fresh posting priority %.2f not above stale %.2f", fresh, old)
	}
	if old < float64(fit)-0.001 {
		t.Errorf("a stale posting fell below its own fit score (%.2f < %d)", old, fit)
	}
	// The bonus must be a tie-breaker, not a promoter: a weak-but-new posting
	// must not overtake a strong-but-older one.
	weakNew := Priority(40, PrioritySignals{FirstSeenAt: now}, now)
	strongOld := Priority(70, PrioritySignals{FirstSeenAt: now.Add(-14 * 24 * time.Hour)}, now)
	if weakNew >= strongOld {
		t.Errorf("freshness promoted a weak match (%.2f) over a strong one (%.2f)",
			weakNew, strongOld)
	}
}

func TestPrioritySaturationSinksButNeverHides(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	prev := math.Inf(1)
	for n := range 6 {
		p := Priority(80, PrioritySignals{FirstSeenAt: now, TimesShownAndIgnored: n}, now)
		if p > prev {
			t.Errorf("%d ignored impressions raised priority", n)
		}
		if p <= 0 {
			t.Errorf("%d ignored impressions drove priority to %.2f; it must sink, not vanish", n, p)
		}
		prev = p
	}
	// The penalty is capped, so it stops after saturationFull.
	a := Priority(80, PrioritySignals{FirstSeenAt: now, TimesShownAndIgnored: saturationFull}, now)
	b := Priority(80, PrioritySignals{FirstSeenAt: now, TimesShownAndIgnored: saturationFull * 10}, now)
	if a != b {
		t.Errorf("the saturation penalty is not capped: %.2f then %.2f", a, b)
	}
}

// Closing-soon credit only exists when the EMPLOYER stated a date. An inferred
// deadline would be manufactured urgency.
func TestClosingSoonOnlyAppliesToAStatedDate(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	none := Priority(60, PrioritySignals{FirstSeenAt: now}, now)

	soon := now.Add(24 * time.Hour)
	stated := Priority(60, PrioritySignals{FirstSeenAt: now, ClosesAt: &soon}, now)
	if stated <= none {
		t.Error("a stated imminent deadline gave no credit")
	}

	far := now.Add(90 * 24 * time.Hour)
	distant := Priority(60, PrioritySignals{FirstSeenAt: now, ClosesAt: &far}, now)
	if distant != none {
		t.Errorf("a distant deadline changed priority (%.2f vs %.2f)", distant, none)
	}

	past := now.Add(-time.Hour)
	closed := Priority(60, PrioritySignals{FirstSeenAt: now, ClosesAt: &past}, now)
	if closed != none {
		t.Errorf("an elapsed deadline gave credit (%.2f vs %.2f)", closed, none)
	}
}

func TestPriorityStaysInRange(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	soon := now.Add(time.Hour)
	hi := Priority(100, PrioritySignals{FirstSeenAt: now, ClosesAt: &soon}, now)
	if hi > 100 {
		t.Errorf("priority %.2f exceeded 100", hi)
	}
	lo := Priority(0, PrioritySignals{FirstSeenAt: now.Add(-time.Hour * 24 * 365),
		TimesShownAndIgnored: 99}, now)
	if lo < 0 {
		t.Errorf("priority %.2f fell below 0", lo)
	}
}

func tstamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
