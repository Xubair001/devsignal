package normalize

import (
	"reflect"
	"testing"
)

// none is what str() reports for a nil pointer: an explicit "the source did not
// say", which is a distinct assertion from any real value.
const none = "<nil>"

const bangalore = "Bangalore"

func str(p *string) string {
	if p == nil {
		return none
	}
	return *p
}

func lvl(p *int16) int {
	if p == nil {
		return -1
	}
	return int(*p)
}

// Cases taken verbatim from a real GitLab Greenhouse board.
func TestParseTitleRealWorld(t *testing.T) {
	cases := []struct {
		raw        string
		family     string
		seniority  int
		management bool
	}{
		{"Senior Backend Engineer - Monitoring and Anomaly Detection", FamilyBackend, int(SenioritySenior), false},
		{"Senior Backend Engineer (Ruby), Plan: Portfolio Experience", FamilyBackend, int(SenioritySenior), false},
		{"Intermediate Backend Engineer, Platform Readiness", FamilyBackend, int(SeniorityMid), false},
		{"Staff Frontend Engineer", FamilyFrontend, int(SeniorityStaff), false},
		{"Principal Engineer, Data Platform", FamilyData, int(SeniorityPrincipal), false},
		{"Senior Threat Intelligence Engineer", FamilySecurity, int(SenioritySenior), false},
		{"Site Reliability Engineer", FamilyPlatform, -1, false},
		{"Machine Learning Engineer", FamilyMl, -1, false},
		{"Engineering Manager, Growth", FamilyEngineering, -1, true},
		{"Director of Engineering", FamilyEngineering, -1, true},
		{"Strategic Account Executive - Turkey", FamilySales, -1, false},
		{"Ecosystem Sales Manager, Carahsoft (Washington DC)", FamilySales, -1, true},
		{"Customer Success Architect, CEUR", FamilySupport, -1, false},
		// "Team Member Relations" is GitLab's in-house phrase for employee
		// relations. A generic ruleset should NOT learn one company's jargon, so
		// nil is the correct, honest answer here (blueprint §3).
		{"Senior Team Member Relations Partner", none, int(SenioritySenior), false},
		{"Forward Deployed Engineer - META", FamilyEngineering, -1, false},
		{"Backend Engineering Intern", FamilyBackend, int(SeniorityIntern), false},
		{"", none, -1, false},
	}
	for _, c := range cases {
		got := ParseTitle(c.raw)
		if str(got.RoleFamily) != c.family {
			t.Errorf("%q: family = %s, want %s", c.raw, str(got.RoleFamily), c.family)
		}
		if lvl(got.Seniority) != c.seniority {
			t.Errorf("%q: seniority = %d, want %d", c.raw, lvl(got.Seniority), c.seniority)
		}
		if got.IsManagement != c.management {
			t.Errorf("%q: management = %v, want %v", c.raw, got.IsManagement, c.management)
		}
	}
}

// Management is a separate ladder. An Engineering Manager must NOT be given an
// IC seniority just because the word "manager" outranks "senior" in prose.
func TestManagementIsNotAnICRung(t *testing.T) {
	m := ParseTitle("Engineering Manager, Growth")
	if m.Seniority != nil {
		t.Errorf("manager got IC seniority %d; the ladders must stay separate", *m.Seniority)
	}
	if !m.IsManagement {
		t.Error("manager not flagged as management")
	}
	// And a senior IC must not be flagged as management.
	ic := ParseTitle("Senior Backend Engineer")
	if ic.IsManagement {
		t.Error("senior IC wrongly flagged as management")
	}
}

// "Tech Lead" is normally an IC role; treating it as people management would
// mis-sort an entire career track.
func TestTechLeadIsNotManagement(t *testing.T) {
	if got := ParseTitle("Tech Lead, Payments"); got.IsManagement {
		t.Error("Tech Lead flagged as management")
	}
}

func TestMostSpecificFamilyWins(t *testing.T) {
	// Contains "engineer" too, but ml is the specific answer.
	if got := ParseTitle("Machine Learning Engineer"); str(got.RoleFamily) != FamilyMl {
		t.Errorf("family = %s, want %s", str(got.RoleFamily), FamilyMl)
	}
	// "senior staff" must resolve to staff, the more specific rung.
	if got := ParseTitle("Senior Staff Engineer"); lvl(got.Seniority) != int(SeniorityStaff) {
		t.Errorf("seniority = %d, want staff(%d)", lvl(got.Seniority), SeniorityStaff)
	}
}

func TestParseLocationRealWorld(t *testing.T) {
	cases := []struct {
		raw     string
		mode    string
		country string
		city    string
		scope   []string
	}{
		{"Remote, United States", WorkRemote, "US", none, []string{"US"}},
		{"Remote, US", WorkRemote, "US", none, []string{"US"}},
		{"Bangalore, India", WorkOnsite, "IN", bangalore, []string{"IN"}},
		{"Remote, Canada; Remote, United States", WorkRemote, none, none, []string{"CA", "US"}},
		{"Remote, Poland; Remote, United Kingdom", WorkRemote, none, none, []string{"GB", "PL"}},
		{"Remote, United Arab Emirates", WorkRemote, "AE", none, []string{"AE"}},
		{"Remote", WorkRemote, none, none, nil},
		{"Remote, Bangalore", WorkRemote, none, bangalore, nil},
		{"Hybrid - Berlin", WorkHybrid, none, "Berlin", nil},
		{"", "", none, none, nil},
	}
	for _, c := range cases {
		got := ParseLocation(c.raw)
		if got.WorkMode != c.mode {
			t.Errorf("%q: mode = %q, want %q", c.raw, got.WorkMode, c.mode)
		}
		if str(got.Country) != c.country {
			t.Errorf("%q: country = %s, want %s", c.raw, str(got.Country), c.country)
		}
		if str(got.City) != c.city {
			t.Errorf("%q: city = %s, want %s", c.raw, str(got.City), c.city)
		}
		if !reflect.DeepEqual(got.GeoScope, c.scope) {
			t.Errorf("%q: scope = %v, want %v", c.raw, got.GeoScope, c.scope)
		}
	}
}

// The distinction that matters most in remote hiring: "remote, US only" is not
// the same opportunity as "remote, Canada or US". Collapsing multi-country to a
// single country would be a confident lie.
func TestMultiCountryDoesNotCollapse(t *testing.T) {
	single := ParseLocation("Remote, United States")
	multi := ParseLocation("Remote, Canada; Remote, United States")

	if single.Country == nil || *single.Country != "US" {
		t.Fatalf("single-country: got %v", str(single.Country))
	}
	if multi.Country != nil {
		t.Fatalf("multi-country collapsed to %q; must stay nil", *multi.Country)
	}
	if len(multi.GeoScope) != 2 {
		t.Fatalf("multi-country scope = %v, want two entries", multi.GeoScope)
	}
}

// A city must never be used to infer a country. "Remote, Indiana" resolving to
// India is the classic substring-matching failure this guards against.
func TestCityDoesNotInferCountry(t *testing.T) {
	got := ParseLocation("Remote, Bangalore")
	if got.Country != nil {
		t.Errorf("inferred country %q from a city name", *got.Country)
	}
	if str(got.City) != bangalore {
		t.Errorf("city = %s, want Bangalore", str(got.City))
	}
	if got := ParseLocation("Remote, Indiana"); got.Country != nil {
		t.Errorf("Indiana resolved to country %q", *got.Country)
	}
}

// Idempotence is the invariant that makes re-normalization safe: running the
// ruleset over already-normalized output must change nothing.
func TestNormalizationIsIdempotent(t *testing.T) {
	titles := []string{
		"Senior Backend Engineer - Monitoring and Anomaly Detection",
		"Intermediate   Backend  Engineer,  Platform Readiness",
		"Engineering Manager, Growth",
		"Staff  Frontend   Engineer",
		"",
	}
	for _, raw := range titles {
		once := ParseTitle(raw)
		twice := ParseTitle(once.Normalized)
		if once.Normalized != twice.Normalized {
			t.Errorf("title %q: not idempotent: %q -> %q", raw, once.Normalized, twice.Normalized)
		}
		if lvl(once.Seniority) != lvl(twice.Seniority) ||
			str(once.RoleFamily) != str(twice.RoleFamily) ||
			once.IsManagement != twice.IsManagement {
			t.Errorf("title %q: derived fields changed on re-parse", raw)
		}
	}

	locs := []string{"Remote, United States", "Bangalore, India", "Remote", "Remote, Canada; Remote, US", ""}
	for _, raw := range locs {
		a := ParseLocation(raw)
		b := ParseLocation(raw)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("location %q: not deterministic", raw)
		}
	}
}

func TestWhitespaceAndDashesNormalize(t *testing.T) {
	a := ParseTitle("Senior  Backend   Engineer — Monitoring")
	b := ParseTitle("Senior Backend Engineer - Monitoring")
	if a.Normalized != b.Normalized {
		t.Errorf("em dash and spacing not normalized: %q vs %q", a.Normalized, b.Normalized)
	}
}
