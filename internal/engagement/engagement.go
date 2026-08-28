// Package engagement is the append-only record of what users did with what we
// showed them.
//
// It is three things at once, and keeping them in one log is the point:
//
//   - The product's save and apply state.
//   - The evaluation set that replaces the rubric labels in internal/eval. These
//     are the real labels, because they record what a person actually did rather
//     than what a rule predicted they would want.
//   - The ranking decision record. Every row carries the score and factor
//     breakdown AS SHOWN plus every version behind it, because blueprint §32
//     requires answering "why was this ranked here, for this user, on that date"
//     after the fact — and a score recomputed later under different weights is
//     not an answer to that question.
//
// Nothing here updates or deletes. Un-saving appends an 'unsaved' row; a decision
// record that can be edited is not a record, and a save the user took back is a
// different label from one they kept.
package engagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/store"
)

// Event types. Part of the API contract, not debug strings.
const (
	EventShown     = "shown"
	EventOpened    = "opened"
	EventSaved     = "saved"
	EventUnsaved   = "unsaved"
	EventApplied   = "applied"
	EventDismissed = "dismissed"
)

// Dismiss reasons.
//
// A closed set, and each one names a factor rather than a mood. "Wrong level" and
// "comp too low" are corrections the scorer can learn from; "did not like it"
// would not be. That is why there is no free-text option: an unusable label is
// worse than none, because it looks like signal in a count.
const (
	ReasonWrongStack     = "wrong_stack"
	ReasonWrongLevel     = "wrong_level"
	ReasonWrongLocation  = "wrong_location"
	ReasonCompTooLow     = "comp_too_low"
	ReasonNotInterested  = "not_interested"
	ReasonAlreadyApplied = "already_applied"
)

// DismissReasons is the accepted set, in the order a UI should offer them:
// specific corrections first, the two catch-alls last, so the useful answers are
// the easy ones to pick.
var DismissReasons = []string{
	ReasonWrongStack, ReasonWrongLevel, ReasonWrongLocation, ReasonCompTooLow,
	ReasonAlreadyApplied, ReasonNotInterested,
}

// ErrUnknownReason means the caller sent a dismiss reason outside the set.
var ErrUnknownReason = errors.New("engagement: unknown dismiss reason")

// ErrReasonRequired means a dismissal arrived without one.
//
// Required rather than optional because a dismissal without a reason is the one
// event that teaches nothing, and the whole purpose of this log is to learn from
// the negatives.
var ErrReasonRequired = errors.New("engagement: dismissals require a reason")

// ErrNotFound means the opportunity does not exist or is not visible.
var ErrNotFound = errors.New("engagement: opportunity not found")

// Service records engagement.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	log  *slog.Logger
}

// New builds the service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), log: log}
}

// Decision is the ranking context that was on screen when the user acted.
//
// Optional: an action taken from a search result or a direct link has no ranking
// behind it, and recording zeros would fabricate one. Nil means "not from a
// ranked surface", which is a real and different state.
type Decision struct {
	FitScore           int
	MaxPossible        int
	Factors            []matching.FactorScore
	WeightsVersion     string
	EmbeddingVersion   string
	ProfileVersion     int32
	OpportunityVersion int32
}

// State is the current engagement state of one posting for one user.
type State struct {
	Saved     bool
	Applied   bool
	Dismissed bool
	AppliedAt *time.Time
}

// Save records a save.
func (s *Service) Save(ctx context.Context, userID, oppID pgtype.UUID, d *Decision) error {
	return s.record(ctx, userID, oppID, EventSaved, nil, d)
}

// Unsave appends an 'unsaved' event rather than deleting the save.
func (s *Service) Unsave(ctx context.Context, userID, oppID pgtype.UUID) error {
	return s.record(ctx, userID, oppID, EventUnsaved, nil, nil)
}

// Apply records that the user applied.
//
// Our record of their claim, not a verified fact: we cannot see the employer's
// side. Named accordingly everywhere it surfaces.
func (s *Service) Apply(ctx context.Context, userID, oppID pgtype.UUID, d *Decision) error {
	return s.record(ctx, userID, oppID, EventApplied, nil, d)
}

// Open records that the user opened the posting.
func (s *Service) Open(ctx context.Context, userID, oppID pgtype.UUID, d *Decision) error {
	return s.record(ctx, userID, oppID, EventOpened, nil, d)
}

// Dismiss records a dismissal and its reason.
func (s *Service) Dismiss(
	ctx context.Context, userID, oppID pgtype.UUID, reason string, d *Decision,
) error {
	if reason == "" {
		return ErrReasonRequired
	}
	if !slices.Contains(DismissReasons, reason) {
		return fmt.Errorf("%w: %q", ErrUnknownReason, reason)
	}
	return s.record(ctx, userID, oppID, EventDismissed, &reason, d)
}

func (s *Service) record(
	ctx context.Context, userID, oppID pgtype.UUID, event string,
	reason *string, d *Decision,
) error {
	p := store.RecordEngagementParams{
		UserID: userID, OpportunityID: oppID, EventType: event, DismissReason: reason,
	}
	if d != nil {
		score, maxPossible := int16(d.FitScore), int16(d.MaxPossible)
		p.FitScoreAtEvent = &score
		p.MaxPossibleAtEvent = &maxPossible
		p.WeightsVersion = strPtr(d.WeightsVersion)
		p.EmbeddingVersion = strPtr(d.EmbeddingVersion)
		p.ProfileVersion = &d.ProfileVersion
		p.OpportunityVersion = &d.OpportunityVersion
		if len(d.Factors) > 0 {
			b, err := json.Marshal(d.Factors)
			if err != nil {
				// The event matters more than the breakdown. Losing the decision
				// record is bad; losing the user's action is worse.
				s.log.Error("encoding factor breakdown", "err", err)
			} else {
				p.FactorBreakdown = b
			}
		}
	}

	if _, err := s.q.RecordEngagement(ctx, p); err != nil {
		if isForeignKeyViolation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("engagement: recording %s: %w", event, err)
	}
	// user_id and opportunity_id only. Never the title: what a person is applying
	// for is exactly the kind of thing that must not sit in a log (hard rule 13).
	s.log.Info("engagement recorded",
		"user_id", userID.String(), "opportunity_id", oppID.String(), "event", event)
	return nil
}

// RecordShown appends one 'shown' event per posting in a rendered feed.
//
// pgx CopyFrom rather than a statement per posting: a feed of 50 would otherwise
// be 50 round trips on the path a user is waiting on. Failure is logged, not
// returned — a missing impression costs some saturation accuracy, and refusing to
// serve a feed because we could not log it would be the wrong trade.
func (s *Service) RecordShown(
	ctx context.Context, userID pgtype.UUID, matches []matching.Match, profileVersion int32,
) {
	if len(matches) == 0 {
		return
	}
	rows := make([][]any, 0, len(matches))
	for _, m := range matches {
		// The factor breakdown AS SHOWN, not just the total.
		//
		// Blueprint §32 asks the decision record to answer "why was this ranked
		// here for this user on this date", and a score without its arithmetic
		// does not answer that — re-deriving it later under different weights is
		// a different number about a different model. The column and the
		// migration comment promising it were both here from step 17; the writer
		// simply never populated it, so all 283 recorded decisions carried a
		// total and no reasoning. Found by the §38 readiness gate.
		breakdown, merr := json.Marshal(m.Fit.Factors)
		if merr != nil {
			// Degrade to a row without it rather than dropping the impression:
			// the saturation signal is still worth having, and a feed must not
			// fail because a log entry would not serialize.
			s.log.Warn("encoding factor breakdown",
				"opportunity_id", m.Opportunity.ID.String(), "err", merr)
			breakdown = nil
		}
		rows = append(rows, []any{
			userID, m.Opportunity.ID, EventShown,
			int16(m.Fit.Score), int16(m.Fit.MaxPossible), breakdown,
			matching.WeightsVersion, embed.LocalVersion,
			profileVersion, m.Opportunity.Version,
		})
	}
	n, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"engagement_event"},
		[]string{"user_id", "opportunity_id", "event_type",
			"fit_score_at_event", "max_possible_at_event", "factor_breakdown",
			"weights_version", "embedding_version",
			"profile_version", "opportunity_version"},
		pgx.CopyFromRows(rows))
	if err != nil {
		s.log.Error("recording feed impressions",
			"user_id", userID.String(), "count", len(rows), "err", err)
		return
	}
	if int(n) != len(rows) {
		s.log.Warn("partial impression write",
			"user_id", userID.String(), "wrote", n, "expected", len(rows))
	}
}

// StateFor returns the engagement state of every posting this user has touched.
//
// The whole set in one query rather than per posting: the feed needs it for every
// row it renders.
func (s *Service) StateFor(ctx context.Context, userID pgtype.UUID) (map[string]State, error) {
	rows, err := s.q.GetEngagementState(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("engagement: loading state: %w", err)
	}
	out := make(map[string]State, len(rows))
	for _, r := range rows {
		st := State{Saved: r.Saved, Applied: r.Applied, Dismissed: r.Dismissed}
		if r.AppliedAt.Valid {
			t := r.AppliedAt.Time
			st.AppliedAt = &t
		}
		out[r.OpportunityID.String()] = st
	}
	return out, nil
}

// SaturationFor returns how many distinct days each posting was shown and not
// acted on, for the priority term.
func (s *Service) SaturationFor(ctx context.Context, userID pgtype.UUID) (map[string]int, error) {
	rows, err := s.q.CountShownDays(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("engagement: counting impressions: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.OpportunityID.String()] = int(r.DaysShown)
	}
	return out, nil
}

// ListSaved returns saved postings, most recent first, keyset-paginated.
func (s *Service) ListSaved(
	ctx context.Context, userID pgtype.UUID, before *time.Time, pageSize int32,
) ([]store.ListSavedOpportunitiesRow, error) {
	var cursor pgtype.Timestamptz
	if before != nil {
		cursor = pgtype.Timestamptz{Time: *before, Valid: true}
	}
	rows, err := s.q.ListSavedOpportunities(ctx, store.ListSavedOpportunitiesParams{
		UserID: userID, Before: cursor, PageSize: pageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("engagement: listing saved: %w", err)
	}
	return rows, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isForeignKeyViolation distinguishes "that posting does not exist" from a real
// database failure, so the caller can answer 404 rather than 500.
func isForeignKeyViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23503"
	}
	return false
}

// CachedDecision returns the ranking context a user was shown for one posting.
//
// Reads the fit_score cache, which is keyed on every version behind the score, so
// a row that survives the lookup is one still valid for the current profile and
// posting. A miss is not an error worth failing on — see the caller.
func (s *Service) CachedDecision(
	ctx context.Context, userID, oppID pgtype.UUID,
) (*Decision, error) {
	row, err := s.q.GetCachedFitScore(ctx, store.GetCachedFitScoreParams{
		UserID: userID, OpportunityID: oppID, WeightsVersion: matching.WeightsVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoDecision
		}
		return nil, fmt.Errorf("engagement: loading cached decision: %w", err)
	}

	d := &Decision{
		FitScore: int(row.Score), MaxPossible: int(row.MaxPossible),
		WeightsVersion: row.WeightsVersion, EmbeddingVersion: row.EmbeddingVersion,
		ProfileVersion: row.ProfileVersion, OpportunityVersion: row.OpportunityVersion,
	}
	if len(row.Factors) > 0 {
		if err := json.Unmarshal(row.Factors, &d.Factors); err != nil {
			// The score is still usable without the breakdown, and a partial record
			// beats none.
			s.log.Warn("undecodable cached breakdown",
				"opportunity_id", oppID.String(), "err", err)
		}
	}
	return d, nil
}

// ErrNoDecision means the action did not come from a ranked surface, or the
// cached score was invalidated by a version change.
var ErrNoDecision = errors.New("engagement: no cached ranking decision")
