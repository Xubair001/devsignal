package matching

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/store"
)

// MaxGaps bounds the list. A gap analysis of forty skills is not advice, it is a
// wall — and the tail is where extraction noise lives.
const MaxGaps = 12

// Gap is one skill the user's eligible roles ask for and they do not have.
//
// Counts, never a probability. We have no applicant counts, so there is no
// competitiveness figure to give — "23 of the roles you can take list this as
// required" is arithmetic over observed data; "learning this raises your chances
// 18%" is a claim we cannot support and the one thing blueprint §3 forbids most
// clearly.
type Gap struct {
	Slug        string
	DisplayName string
	RequiredBy  int64
	PreferredBy int64
	// InVocabulary is false when extraction invented the phrase rather than
	// matching our ontology. Those are excluded from advice: "learn
	// collaboration platforms" is not something anyone can act on.
	InVocabulary bool
}

// Strength is a skill they have that their eligible roles ask for.
type Strength struct {
	DisplayName string
	RequiredBy  int64
}

// GapReport answers "what am I missing", with its own denominator attached.
type GapReport struct {
	Gaps      []Gap
	Strengths []Strength
	// Eligible is how many roles passed this user's gate; WithSkills is how many
	// of those we could actually read. The GAP between them is the honesty
	// number: every count above is computed over WithSkills, and reporting them
	// without it would silently understate by however much of the corpus is
	// unenriched.
	Eligible   int64
	WithSkills int64
	// Excluded counts gaps dropped for not being in the vocabulary. Reported
	// rather than hidden, because a large number means the ontology is behind
	// rather than that the user has few gaps.
	Excluded int
}

// Coverage is the share of eligible roles whose skills we could read.
func (r GapReport) Coverage() float64 {
	if r.Eligible == 0 {
		return 0
	}
	return float64(r.WithSkills) / float64(r.Eligible)
}

// State is why a report is or is not worth showing.
type State string

const (
	// StateReady means the analysis rests on enough observed data.
	StateReady State = "ready"
	// StateStale means no eligibility has been computed for the CURRENT profile
	// version. Distinct from low coverage, and it has a different fix: the user
	// changed their profile and nothing has re-run the gate since.
	StateStale State = "stale"
	// StateThin means the gate ran but too few of those roles could be read.
	StateThin State = "insufficient_extraction"
)

// State classifies the report.
//
// The two empty cases are separated deliberately. They look identical — an empty
// list — and they have opposite fixes: one needs the feed re-run, the other needs
// extraction run over more of the corpus. Telling someone to do the wrong one is
// worse than telling them nothing, and an earlier version of this reported "run
// extraction" for a user whose only problem was a profile edit.
func (r GapReport) State() State {
	switch {
	case r.Eligible == 0:
		return StateStale
	case r.Coverage() < 0.60:
		return StateThin
	default:
		return StateReady
	}
}

// Readable reports whether the analysis is worth showing.
//
// The 60% bar is the same one the fit model uses for "Not enough information",
// and for the same reason: below it, the list says more about what we failed to
// read than about what the market wants. A gap report built on a fifth of the
// corpus would name whichever skills happened to be in the fifth.
func (r GapReport) Readable() bool { return r.State() == StateReady }

// SkillGaps computes the report.
//
// Deliberately NOT part of fit. A gap is not a factor and must never become one:
// scoring someone down for a skill they lack is already what the required-skills
// factor does, and doing it twice would double-count. This is a read surface over
// the same observed data, and it changes no ranking.
func (s *Service) SkillGaps(ctx context.Context, userID pgtype.UUID) (*GapReport, error) {
	cov, err := s.q.SkillGapCoverage(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("matching: gap coverage: %w", err)
	}
	rep := &GapReport{Eligible: cov.Eligible, WithSkills: cov.WithSkills}

	rows, err := s.q.SkillGapsForUser(ctx, store.SkillGapsForUserParams{
		UserID: userID,
		// Over-fetch, because the vocabulary filter below removes some.
		MaxRows: int32(MaxGaps * 4),
	})
	if err != nil {
		return nil, fmt.Errorf("matching: skill gaps: %w", err)
	}
	for _, r := range rows {
		if !r.InVocabulary {
			// An extraction-invented phrase is evidence about a posting, not
			// advice a person can act on.
			rep.Excluded++
			continue
		}
		if len(rep.Gaps) >= MaxGaps {
			continue
		}
		rep.Gaps = append(rep.Gaps, Gap{
			Slug: r.Slug, DisplayName: r.DisplayName,
			RequiredBy: r.RequiredBy, PreferredBy: r.PreferredBy,
			InVocabulary: true,
		})
	}

	strengths, err := s.q.SkillsUserHasInDemand(ctx, store.SkillsUserHasInDemandParams{
		UserID: userID, MaxRows: MaxGaps,
	})
	if err != nil {
		return nil, fmt.Errorf("matching: strengths: %w", err)
	}
	for _, st := range strengths {
		rep.Strengths = append(rep.Strengths, Strength{
			DisplayName: st.DisplayName, RequiredBy: st.RequiredBy,
		})
	}
	return rep, nil
}
