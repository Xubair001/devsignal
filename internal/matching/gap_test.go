package matching

import "testing"

// TestStateSeparatesTheTwoEmptyCases.
//
// They look identical to a caller — an empty gap list — and they have OPPOSITE
// fixes: one needs the eligibility gate re-run, the other needs extraction run
// over more of the corpus. An earlier version reported "run extraction" for a
// user whose only problem was a profile edit, which is worse advice than none.
func TestStateSeparatesTheTwoEmptyCases(t *testing.T) {
	cases := []struct {
		name  string
		rep   GapReport
		want  State
		ready bool
	}{
		{
			name: "no eligibility for the current profile version",
			rep:  GapReport{Eligible: 0, WithSkills: 0},
			want: StateStale,
		},
		{
			name: "the gate ran but almost nothing could be read",
			rep:  GapReport{Eligible: 100, WithSkills: 20},
			want: StateThin,
		},
		{
			name: "just under the bar",
			rep:  GapReport{Eligible: 100, WithSkills: 59},
			want: StateThin,
		},
		{
			name:  "exactly at the bar",
			rep:   GapReport{Eligible: 100, WithSkills: 60},
			want:  StateReady,
			ready: true,
		},
		{
			name:  "everything read",
			rep:   GapReport{Eligible: 168, WithSkills: 164},
			want:  StateReady,
			ready: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.rep.State(); got != c.want {
				t.Errorf("State() = %q, want %q", got, c.want)
			}
			if got := c.rep.Readable(); got != c.ready {
				t.Errorf("Readable() = %v, want %v", got, c.ready)
			}
		})
	}
}

// TestCoverageBarMatchesTheFitModel.
//
// The 60% bar is the same one fit uses before it will call something anything
// other than "Not enough information", and it has to stay the same: two
// different thresholds for "we could not observe enough" would mean the gap page
// makes a confident claim on evidence the feed already refused to score.
func TestCoverageBarMatchesTheFitModel(t *testing.T) {
	// minEvidence is fit.go's constant. If either moves, this fails.
	atBar := GapReport{Eligible: 1000, WithSkills: int64(minEvidence * 1000)}
	if atBar.State() != StateReady {
		t.Errorf("the gap bar is stricter than the fit model's %.2f coverage floor",
			minEvidence)
	}
	justUnder := GapReport{Eligible: 1000, WithSkills: int64(minEvidence*1000) - 1}
	if justUnder.State() != StateThin {
		t.Errorf("the gap bar is looser than the fit model's %.2f coverage floor",
			minEvidence)
	}
}

// TestZeroEligibleIsStaleNotDivideByZero.
func TestZeroEligibleIsStaleNotDivideByZero(t *testing.T) {
	r := GapReport{}
	if got := r.Coverage(); got != 0 {
		t.Errorf("Coverage() with no eligible roles = %v, want 0", got)
	}
	if r.State() != StateStale {
		t.Errorf("State() = %q, want %q", r.State(), StateStale)
	}
}
