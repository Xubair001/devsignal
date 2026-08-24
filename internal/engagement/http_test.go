package engagement

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/store"
)

// The DTO is the enforcement point for blueprint §3, and a DTO only enforces
// anything while someone is checking. These walk the serialized JSON rather than
// the Go types, because what reaches a client is what matters.

// A bare percentage implying a probability is the one display the blueprint names
// as never allowed. No field may look like one.
func TestFeedJSONExposesNoPercentage(t *testing.T) {
	body := marshalFeedItem(t)
	for _, banned := range []string{
		"percent", "percentage", "match_percent", "score_pct", "probability",
		"confidence_pct", "match_score",
	} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Errorf("feed JSON contains %q; a percentage implies a calibration we have not measured", banned)
		}
	}
}

// Priority is volatile by design. Exposing it invites a client to render it, and
// then a "match" changes overnight because a posting aged.
func TestFeedJSONNeverExposesPriority(t *testing.T) {
	body := strings.ToLower(marshalFeedItem(t))
	if strings.Contains(body, "priority") {
		t.Error("feed JSON exposes priority; it orders the feed and is never a match")
	}
	// Belt and braces: the Go type must not carry it either.
	if _, found := fieldByJSONTag(reflect.TypeOf(FeedItem{}), "priority"); found {
		t.Error("FeedItem has a priority field")
	}
}

// Internal scoring machinery must not leak. A client that can see the raw factor
// weights will eventually reimplement the model and disagree with us about it.
//
// Two things are deliberately NOT on this list. versions.embedding is the version
// string, which a client needs to tell a stale explanation from a current one. And
// "vector" appears as a retrieval CHANNEL NAME in channels, which is a value
// rather than a leaked field — the vector itself would serialize as
// "embedding":[ and that is what is checked.
func TestFeedJSONHidesInternalScoringFields(t *testing.T) {
	body := strings.ToLower(marshalFeedItem(t))
	for _, banned := range []string{
		`"weight"`, `"value"`, "simhash", "content_hash", "block_key",
		"ghost_risk_score", "embedding_dim", `"embedding":[`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("feed JSON leaks internal field %s", banned)
		}
	}
}

// The distinction the whole explanation rests on: a factor with no data must read
// differently from one that scored zero.
func TestFactorViewDistinguishesUnscoredFromZero(t *testing.T) {
	fit := matching.Fit{
		Score: 10, MaxPossible: 45, WeightsVersion: "w2",
		Factors: []matching.FactorScore{
			{Factor: matching.FactorRequiredSkills, Available: false, Reason: "not extracted yet"},
			{Factor: matching.FactorDomain, Available: true, Value: 0, Contribution: 0,
				MaxContribution: 10, Reason: "marketing, not one of your target families"},
		},
	}
	view := toFitView(fit, 3)

	var unscored, zeroed FactorView
	for _, f := range view.Factors {
		switch f.Factor {
		case matching.FactorRequiredSkills:
			unscored = f
		case matching.FactorDomain:
			zeroed = f
		}
	}
	if unscored.Scored {
		t.Error("a factor with no data was marked scored")
	}
	if unscored.MaxPoints != 0 {
		t.Errorf("an unscored factor advertised %v achievable points", unscored.MaxPoints)
	}
	if !zeroed.Scored {
		t.Error("a factor that genuinely scored zero was marked unscored")
	}
	if zeroed.MaxPoints == 0 {
		t.Error("a scored factor must advertise what it could have earned, or +0 of 0 is meaningless")
	}
}

// The band is the headline the user reads; it must always be present, including
// when there is not enough information for a verdict.
func TestFitViewAlwaysCarriesABand(t *testing.T) {
	for _, f := range []matching.Fit{
		{Score: 90, MaxPossible: 100},
		{Score: 15, MaxPossible: 15}, // thin evidence
		{},                           // nothing observable
	} {
		if got := toFitView(f, 1).Band; got == "" {
			t.Errorf("fit %+v produced no band", f)
		}
	}
	if toFitView(matching.Fit{}, 1).Band != string(matching.BandInsufficient) {
		t.Error("an unscoreable posting must say so rather than reading as a weak match")
	}
}

// Versions are what let a client tell a stale explanation from a current one.
func TestFitViewCarriesEveryVersion(t *testing.T) {
	v := toFitView(matching.Fit{WeightsVersion: "w2"}, 7).Versions
	if v.Weights == "" || v.Embedding == "" || v.Profile != 7 {
		t.Errorf("versions incomplete: %+v", v)
	}
}

// ------------------------------------------------------------- dismiss reasons

// A free-text reason would look like signal in a count while teaching nothing, so
// the set is closed. This asserts it stays closed.
func TestDismissReasonsAreAClosedSet(t *testing.T) {
	if len(DismissReasons) != 6 {
		t.Errorf("%d dismiss reasons; the blueprint names 6", len(DismissReasons))
	}
	for _, r := range DismissReasons {
		if r == "" || strings.ContainsAny(r, " ") {
			t.Errorf("reason %q is not a stable identifier", r)
		}
	}
	// The specific corrections must come before the catch-alls, so the useful
	// answers are the easy ones to pick.
	notInterested := slices.Index(DismissReasons, ReasonNotInterested)
	for _, specific := range []string{
		ReasonWrongStack, ReasonWrongLevel, ReasonWrongLocation, ReasonCompTooLow,
	} {
		if slices.Index(DismissReasons, specific) > notInterested {
			t.Errorf("%q is offered after the catch-all; the specific reasons must come first", specific)
		}
	}
}

// Every reason must have a human label, or a client invents its own wording and
// two clients end up disagreeing about what a label means.
func TestEveryDismissReasonHasALabel(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.dismissReasons(rec, nil)

	var out struct {
		Reasons []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"reasons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Reasons) != len(DismissReasons) {
		t.Fatalf("%d reasons exposed, %d defined", len(out.Reasons), len(DismissReasons))
	}
	for _, r := range out.Reasons {
		if r.Label == "" {
			t.Errorf("reason %q has no label", r.Value)
		}
	}
}

// ------------------------------------------------------------- feed sizing

func TestFeedSizeDefaultsToTheProductPromise(t *testing.T) {
	if clampSize("") != defaultFeedSize {
		t.Errorf("default feed size = %d, want %d", clampSize(""), defaultFeedSize)
	}
	if defaultFeedSize != 7 {
		t.Errorf("default feed size is %d; the product promises 7 a day and Precision@7 measures it",
			defaultFeedSize)
	}
}

func TestFeedSizeIsBounded(t *testing.T) {
	if got := clampSize("100000"); got != maxFeedSize {
		t.Errorf("clampSize(100000) = %d, want %d", got, maxFeedSize)
	}
	for _, bad := range []string{"0", "-5", "abc", "1e9"} {
		if got := clampSize(bad); got != defaultFeedSize {
			t.Errorf("clampSize(%q) = %d, want the default", bad, got)
		}
	}
}

// ------------------------------------------------------------- helpers

func marshalFeedItem(t *testing.T) string {
	t.Helper()
	item := toFeedItem(matching.Match{
		Opportunity: store.Opportunity{TitleRaw: "Senior Backend Engineer"},
		Fit: matching.Fit{
			Score: 72, MaxPossible: 90, WeightsVersion: "w2",
			Factors: []matching.FactorScore{{
				Factor: matching.FactorDomain, Available: true, Value: 1,
				Contribution: 10, MaxContribution: 10,
			}},
		},
		Priority: 78.5,
		Channels: []string{"vector", "keyword"},
	}, map[string]State{}, 3)

	b, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fieldByJSONTag(typ reflect.Type, tag string) (reflect.StructField, bool) {
	for i := range typ.NumField() {
		f := typ.Field(i)
		if strings.Split(f.Tag.Get("json"), ",")[0] == tag {
			return f, true
		}
	}
	return reflect.StructField{}, false
}
