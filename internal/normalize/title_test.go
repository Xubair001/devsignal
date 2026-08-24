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
