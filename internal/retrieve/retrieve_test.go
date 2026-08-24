package retrieve

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/store"
)

func uid(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Bytes[0] = b
	u.Valid = true
	return u
}

// The single most dangerous default in this package: a nil slice reaches Postgres
// as NULL, cardinality(NULL) is NULL, and a NULL predicate drops every row. A
// user who set no country filter would get an empty feed.
func TestUnsetFiltersBecomeEmptyNotNil(t *testing.T) {
	c := Criteria{}.withDefaults()

	if c.Countries == nil {
		t.Error("Countries stayed nil; cardinality(NULL) would drop every posting")
	}
	if c.EmploymentTypes == nil {
		t.Error("EmploymentTypes stayed nil")
	}
	if c.Languages == nil {
		t.Error("Languages stayed nil")
	}
	if c.MaxCandidates != DefaultMaxCandidates {
		t.Errorf("MaxCandidates = %d, want the blueprint cap %d", c.MaxCandidates, DefaultMaxCandidates)
	}
	if c.MaxStaleness != DefaultMaxStaleness {
		t.Errorf("MaxStaleness = %v, want %v", c.MaxStaleness, DefaultMaxStaleness)
	}
}

func TestExplicitLimitsSurviveDefaulting(t *testing.T) {
	c := Criteria{MaxCandidates: 25, MaxStaleness: time.Hour}.withDefaults()
	if c.MaxCandidates != 25 || c.MaxStaleness != time.Hour {
		t.Errorf("defaults overwrote explicit values: %d, %v", c.MaxCandidates, c.MaxStaleness)
	}
}

func TestCriteriaFromProfileMapsEveryHardPredicate(t *testing.T) {
	wm := "remote"
	p := store.Profile{
		TargetCountries:       []string{"DE", "NL"},
		WorkModePreference:    &wm,
		TargetEmploymentTypes: []string{"full_time"},
		Languages:             []string{"en"},
		TargetRoleFamilies:    []string{"backend", "platform"},
	}
	c := CriteriaFromProfile(p)

	if len(c.Countries) != 2 || c.Countries[0] != "DE" {
		t.Errorf("countries = %v", c.Countries)
	}
	if c.WorkMode == nil || *c.WorkMode != "remote" {
		t.Errorf("work mode = %v", c.WorkMode)
	}
	if len(c.EmploymentTypes) != 1 || c.EmploymentTypes[0] != "full_time" {
		t.Errorf("employment types = %v", c.EmploymentTypes)
	}
	if len(c.Languages) != 1 {
		t.Errorf("languages = %v", c.Languages)
	}
	// OR, not AND: someone targeting two families wants postings from either.
	if c.Terms != "backend OR platform" {
		t.Errorf("terms = %q, want OR between role families", c.Terms)
	}
}

// A profile with no role families must not produce a query that matches
// everything. Empty terms disable the channel instead.
func TestNoRoleFamiliesYieldsNoKeywordTerms(t *testing.T) {
	if terms := CriteriaFromProfile(store.Profile{}).Terms; terms != "" {
		t.Errorf("terms = %q, want empty so the keyword channel is skipped", terms)
	}
}

// Agreement between two channels that fail differently is the strongest signal
// retrieval has before anything is scored, so those candidates must survive a cap.
func TestMultiChannelCandidatesSurviveTheCap(t *testing.T) {
	merged := map[string]*Candidate{}
	// Three vector-only hits, then one the keyword channel also found.
	for i := byte(1); i <= 3; i++ {
		add(merged, ChannelVector, Candidate{ID: uid(i), Distance: 0.1})
	}
	add(merged, ChannelVector, Candidate{ID: uid(9), Distance: 0.9})
	add(merged, ChannelKeyword, Candidate{ID: uid(9), Rank: 0.5})

	out, truncated := capped(merged, 1)
	if !truncated {
		t.Error("capping 4 candidates to 1 should report truncation")
	}
	if len(out) != 1 {
		t.Fatalf("got %d candidates, want 1", len(out))
	}
	if out[0].ID != uid(9) {
		t.Error("the candidate both channels found was dropped in favour of a single-channel hit")
	}
	if !out[0].FoundBy(ChannelVector) || !out[0].FoundBy(ChannelKeyword) {
		t.Errorf("provenance lost: channels = %v", out[0].Channels)
	}
}

// Distance and rank are not comparable and must not overwrite each other when
// both channels find the same posting.
func TestMergeKeepsBothChannelMeasures(t *testing.T) {
	merged := map[string]*Candidate{}
	add(merged, ChannelVector, Candidate{ID: uid(1), Distance: 0.25})
	add(merged, ChannelKeyword, Candidate{ID: uid(1), Rank: 0.75})

	c := merged[uid(1).String()]
	if c.Distance != 0.25 {
		t.Errorf("distance = %v, want 0.25 preserved from the vector channel", c.Distance)
	}
	if c.Rank != 0.75 {
		t.Errorf("rank = %v, want 0.75 preserved from the keyword channel", c.Rank)
	}
}

// The eval harness compares candidate sets across runs, so the same union must
// flatten to the same order every time. Map iteration order would defeat that.
func TestCappedOrderIsDeterministic(t *testing.T) {
	build := func() map[string]*Candidate {
		m := map[string]*Candidate{}
		for i := byte(1); i <= 20; i++ {
			add(m, ChannelVector, Candidate{ID: uid(i)})
		}
		return m
	}
	first, _ := capped(build(), 10)
	for range 20 {
		next, _ := capped(build(), 10)
		for i := range first {
			if first[i].ID != next[i].ID {
				t.Fatalf("candidate order varies between runs at index %d", i)
			}
		}
	}
}

func TestUnderfilledDistinguishesExhaustionFromLoss(t *testing.T) {
	// Asked 200, got 8, but only 8 postings were eligible: exhausted, not lost.
	exhausted := ChannelCoverage{Requested: 200, Returned: 8, UniverseIsEligibleSet: true}
	if exhausted.Underfilled(8) {
		t.Error("a channel that returned every eligible posting is not underfilled")
	}
	// Asked 200, got 8, while 500 were eligible: candidates were lost.
	if !exhausted.Underfilled(500) {
		t.Error("returning 8 of 500 eligible must be reported as underfilled")
	}
	// An errored channel is a failure, reported separately, not an under-return.
	failed := ChannelCoverage{
		Requested: 200, Returned: 0, Err: errChannelFailed, UniverseIsEligibleSet: true,
	}
	if failed.Underfilled(500) {
		t.Error("an errored channel must not also be reported as underfilled")
	}
}

var errChannelFailed = errors.New("channel failed")

func TestCoverageRatioIsSafeOnAnEmptyCorpus(t *testing.T) {
	if r := (Result{}).CoverageRatio(); r != 0 {
		t.Errorf("coverage on an empty corpus = %v, want 0 and no division by zero", r)
	}
	r := Result{Candidates: make([]Candidate, 5), Eligible: 20}
	if got := r.CoverageRatio(); got != 0.25 {
		t.Errorf("coverage = %v, want 0.25", got)
	}
}

func TestPerChannelOverfetchExceedsTheCap(t *testing.T) {
	// If a channel were limited to exactly the cap, whichever ran first could
	// fill it and starve the other channel entirely.
	if perChannelMultiplier < 2 {
		t.Fatal("per-channel fetch must exceed the union cap or one channel can starve the other")
	}
}

// Against one company's board most candidates come from both channels, so
// channel count alone leaves the set tied and the cap would keep whichever
// postings had the lowest UUIDs. Distance has to break the tie.
func TestCapKeepsNearestAmongEquallySourcedCandidates(t *testing.T) {
	merged := map[string]*Candidate{}
	// uid(9) is the furthest but has the lowest-sorting ID of the three.
	for _, c := range []struct {
		id   byte
		dist float64
	}{{9, 0.90}, {20, 0.10}, {30, 0.50}} {
		add(merged, ChannelVector, Candidate{ID: uid(c.id), Distance: c.dist})
		add(merged, ChannelKeyword, Candidate{ID: uid(c.id), Rank: 0.5})
	}

	out, _ := capped(merged, 1)
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
	if out[0].ID != uid(20) {
		t.Errorf("cap kept distance %v; want the nearest candidate (0.10)", out[0].Distance)
	}
}

// A keyword-only candidate has no distance at all. Treating its zero as "closest
// possible" would let it outrank every vector hit.
func TestKeywordOnlyCandidateDoesNotWinOnAZeroDistance(t *testing.T) {
	merged := map[string]*Candidate{}
	add(merged, ChannelKeyword, Candidate{ID: uid(1), Rank: 0.9}) // no distance
	add(merged, ChannelVector, Candidate{ID: uid(2), Distance: 0.4})

	out, _ := capped(merged, 1)
	if out[0].ID != uid(2) {
		t.Error("a keyword-only candidate with no distance displaced a real vector hit")
	}
}

// The keyword channel's universe is the eligible postings whose title matches,
// which nothing counts. Comparing its return against the full eligible set
// reported a fault on every healthy run against a real board.
func TestKeywordChannelIsNotJudgedAgainstTheEligibleCount(t *testing.T) {
	// 34 of 1000 requested, 199 eligible: the real numbers that produced the
	// spurious warning.
	kw := ChannelCoverage{
		Channel: ChannelKeyword, Requested: 1000, Returned: 34,
		UniverseIsEligibleSet: false,
	}
	if kw.Underfilled(199) {
		t.Error("the keyword channel was reported as underfilled for exhausting its own matches")
	}
	// The vector channel, with the same numbers, genuinely is underfilled.
	vec := ChannelCoverage{
		Channel: ChannelVector, Requested: 1000, Returned: 34,
		UniverseIsEligibleSet: true,
	}
	if !vec.Underfilled(199) {
		t.Error("the vector channel returning 34 of 199 eligible must be flagged")
	}
}
