package normalize

import (
	"strings"
	"testing"
)

// The ladder was written out in three places and one copy was off by one, so a
// "mid" profile embedded as "senior". One definition now, and this asserts the
// label, the reverse mapping and the embedding vocabulary all agree on it.
func TestSeniorityLadderIsConsistentInBothDirections(t *testing.T) {
	for ord, wantLabel := range map[int16]string{
		SeniorityIntern: labelIntern, SeniorityJunior: labelJunior, SeniorityMid: labelMid,
		SenioritySenior: labelSenior, SeniorityStaff: labelStaff,
		SeniorityPrincipal: labelPrincipal,
	} {
		o := ord
		got := SeniorityLabel(&o)
		if got == nil || *got != wantLabel {
			t.Errorf("ordinal %d label = %v, want %q", ord, got, wantLabel)
			continue
		}
		back := SeniorityOrdinal(got)
		if back == nil || *back != ord {
			t.Errorf("label %q maps back to %v, want %d", wantLabel, back, ord)
		}
		// The embedding vocabulary must contain the label itself, or the vector
		// and the displayed rung describe different things.
		if terms := SeniorityTerms(&o); !strings.Contains(terms, wantLabel) {
			t.Errorf("ordinal %d terms %q do not contain the label %q", ord, terms, wantLabel)
		}
	}
}

// The opportunity column permits 0-9 for historical reasons. A rung off the
// ladder is unknown, not clamped: comparing 9 against a 1-6 profile scale is a
// category error, not a distance.
func TestOrdinalsOffTheLadderAreUnknown(t *testing.T) {
	for _, ord := range []int16{-1, 0, 7, 9, 99} {
		o := ord
		if l := SeniorityLabel(&o); l != nil {
			t.Errorf("ordinal %d produced label %q; it is not on the ladder", ord, *l)
		}
		if terms := SeniorityTerms(&o); terms != "" {
			t.Errorf("ordinal %d produced terms %q", ord, terms)
		}
	}
	if SeniorityLabel(nil) != nil || SeniorityTerms(nil) != "" {
		t.Error("a nil ordinal must stay unknown")
	}
}

// Real titles from the recorded corpus that bare "manager" misfiled as people
// leadership. These are the highest-value cases in this file: each one is a title
// that actually exists on a live board, and each was mis-sorted onto the wrong
// career track until the needle was tightened.
func TestCommercialManagerTitlesAreNotPeopleLeadership(t *testing.T) {
	for _, title := range []string{
		"Business Development Manager",
		"Business Development Manager APAC",
		"Customer Success Manager",
		"Customer Success Manager- Public Sector",
		"Business Growth / Senior Account Manager (Acquiring)",
		"Card Schemes Manager - Mexico",
		"Product Manager",
		"Staff Lifecycle Marketing Manager",
		"Event Manager (Community focused)",
		"Senior Product Manager, Developer Experience",
		"Program Manager, Field Operations",
	} {
		if got := ParseTitle(title); got.IsManagement {
			t.Errorf("%q classified as people leadership; it is an individual contributor role", title)
		}
	}
}

// The titles that genuinely are people leadership must still be caught, or the
// tightening has traded one misclassification for another.
// A real title used in more than one test, named so a typo cannot make two tests
// silently disagree about the same input.
const engManagerGrowth = "Engineering Manager, Growth"

func TestGenuineManagementTitlesAreStillCaught(t *testing.T) {
	for _, title := range []string{
		engManagerGrowth,
		"Engineering Manager, Data Foundations",
		"Senior Engineering Manager, Non-Linear Product",
		"Director of Engineering, Security Factory",
		"Director, Engineering, Platform Operations & Productivity",
		"Area Vice President - Financial Services",
		"Head of Infrastructure",
		"Chief Technology Officer",
	} {
		if got := ParseTitle(title); !got.IsManagement {
			t.Errorf("%q not classified as people leadership", title)
		}
	}
}
