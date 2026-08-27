// Package admin is the operations surface.
//
// It exists because of a specific prediction in blueprint §30: the first week of
// real data produces a wrongly merged pair, a source emitting garbage, and a scam
// listing. Without tooling each of those becomes hand-written SQL against
// production, which is how the second incident gets caused by the fix for the
// first.
//
// Two invariants hold everywhere in this package.
//
// Every action is AUDITED, in the same transaction as the change it describes.
// The audit log is hash-chained (step 5), so an admin action that is not recorded
// there is indistinguishable from one that never happened — and these are exactly
// the actions someone will need to reconstruct after an incident.
//
// Every destructive action is REVERSIBLE or COUNTED FIRST. Quarantine is a toggle.
// Un-merge restores rather than deletes. The one genuinely destructive operation,
// a source purge, reports what it would remove and requires the caller to confirm
// that number.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/auth"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/store"
)

// Audit action names. Stable strings, because they are the thing someone greps
// for at 3am.
const (
	ActionSourceQuarantined   = "admin.source.quarantined"
	ActionSourceActivated     = "admin.source.activated"
	ActionSourceRetired       = "admin.source.retired"
	ActionSourceRequeued      = "admin.source.requeued"
	ActionSourcePurged        = "admin.source.purged"
	ActionOpportunityRequeued = "admin.opportunity.requeued"
	ActionMergeUndone         = "admin.merge.undone"
	ActionMergeResolved       = "admin.merge_candidate.resolved"
	ActionFlagResolved        = "admin.flag.resolved"
	ActionFlagRaised          = "flag.raised"
)

// Audit metadata keys. Named because they appear in six audit entries and a typo
// would make one incident's trail unsearchable alongside the others.
const (
	metaNote   = "note"
	metaStatus = "status"
)

// Source statuses.
const (
	StatusActive      = "active"
	StatusQuarantined = "quarantined"
	StatusRetired     = "retired"
)

// Flag reasons. Free text lives in `detail`; the reason itself is a closed set so
// the queue can be triaged.
const (
	FlagScamOrFraud    = "scam_or_fraud"
	FlagNotARealJob    = "not_a_real_job"
	FlagDuplicate      = "duplicate"
	FlagExpired        = "expired"
	FlagMisleadingPay  = "misleading_pay"
	FlagDiscriminatory = "discriminatory"
	FlagOther          = "other"
)

// FlagReasons is the accepted set, most actionable first.
var FlagReasons = []string{
	FlagScamOrFraud, FlagNotARealJob, FlagMisleadingPay, FlagDiscriminatory,
	FlagExpired, FlagDuplicate, FlagOther,
}

// Flag resolutions.
const (
	FlagUpheld     = "upheld"
	FlagRejected   = "rejected"
	FlagDuplicated = "duplicate"
)

// Merge candidate resolutions.
const (
	MergeConfirmed = "merged"
	MergeRejected  = "rejected"
)

// Errors the HTTP layer maps to status codes.
var (
	ErrForbidden       = errors.New("admin: caller is not an administrator")
	ErrNotFound        = errors.New("admin: not found")
	ErrInvalidStatus   = errors.New("admin: unknown status")
	ErrInvalidReason   = errors.New("admin: unknown reason")
	ErrAlreadyResolved = errors.New("admin: already resolved")
	// ErrConfirmationMismatch guards the purge: the caller must state how many
	// postings they expect to lose, and it must match what we counted. A
	// destructive operation that runs on a stale number is how the wrong source
	// gets emptied.
	ErrConfirmationMismatch = errors.New("admin: confirmation count does not match")
)

// Service is the operations surface.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	log  *slog.Logger
}

// New builds the service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), log: log}
}

// Authorize reports whether the identity may use this surface.
//
// One query in one place, called by the middleware rather than by each handler.
// A per-handler check is how one handler ends up without it.
func (s *Service) Authorize(ctx context.Context, userID pgtype.UUID) error {
	ok, err := s.q.IsAdmin(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return fmt.Errorf("admin: authorizing: %w", err)
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// inTx runs fn in a transaction so a change and its audit entry commit together.
//
// The audit append takes a transaction-scoped advisory lock, so it must share the
// transaction with the change it describes — otherwise a crash between them leaves
// the log disagreeing with reality.
func (s *Service) inTx(ctx context.Context, fn func(*store.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("admin: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(store.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin: commit: %w", err)
	}
	return nil
}

func (s *Service) audit(
	ctx context.Context, q *store.Queries, actor pgtype.UUID,
	action, subject string, meta map[string]any,
) error {
	a := auth.NewAuditor(q)
	return a.Append(ctx, auth.Event{
		ActorID: &actor, Action: action, Subject: subject, Metadata: meta,
	})
}

// ---------------------------------------------------------------- sources

// ListSources returns every source with its health columns.
func (s *Service) ListSources(ctx context.Context) ([]store.AdminListSourcesRow, error) {
	rows, err := s.q.AdminListSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin: listing sources: %w", err)
	}
	return rows, nil
}

// SourceHealth returns per-day history for one source.
func (s *Service) SourceHealth(
	ctx context.Context, sourceID pgtype.UUID, days int32,
) ([]store.AdminSourceHealthHistoryRow, error) {
	if days <= 0 || days > 90 {
		days = 30
	}
	rows, err := s.q.AdminSourceHealthHistory(ctx, store.AdminSourceHealthHistoryParams{
		SourceID: sourceID, Days: days,
	})
	if err != nil {
		return nil, fmt.Errorf("admin: source health: %w", err)
	}
	return rows, nil
}

// SetSourceStatus quarantines, reactivates or retires a source.
//
// Quarantine stops polling and nothing else. It deliberately does NOT close the
// source's postings: hard rule 9 requires a successful poll in which a posting was
// absent before closing it, and inferring closure from a quarantined source is
// exactly how one outage deletes the corpus.
func (s *Service) SetSourceStatus(
	ctx context.Context, actor, sourceID pgtype.UUID, status, note string,
) (store.AdminSetSourceStatusRow, error) {
	var out store.AdminSetSourceStatusRow

	action, ok := map[string]string{
		StatusQuarantined: ActionSourceQuarantined,
		StatusActive:      ActionSourceActivated,
		StatusRetired:     ActionSourceRetired,
	}[status]
	if !ok {
		return out, fmt.Errorf("%w: %q", ErrInvalidStatus, status)
	}

	err := s.inTx(ctx, func(q *store.Queries) error {
		row, err := q.AdminSetSourceStatus(ctx, store.AdminSetSourceStatusParams{
			SourceID: sourceID, Status: status,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("admin: setting source status: %w", err)
		}
		out = row
		return s.audit(ctx, q, actor, action, "source:"+row.Name,
			map[string]any{metaStatus: status, metaNote: note})
	})
	return out, err
}

// RequeueSource sends every posting from one source back to a pipeline state.
//
// The state is validated against the state machine rather than accepted as a
// string: an unknown value would strand every posting from that source in a state
// no worker claims.
func (s *Service) RequeueSource(
	ctx context.Context, actor, sourceID pgtype.UUID, targetState, note string,
) (int64, error) {
	if err := validateRequeueTarget(targetState); err != nil {
		return 0, err
	}
	var n int64
	err := s.inTx(ctx, func(q *store.Queries) error {
		var err error
		n, err = q.AdminRequeueSource(ctx, store.AdminRequeueSourceParams{
			TargetState: targetState, SourceID: sourceID,
		})
		if err != nil {
			return fmt.Errorf("admin: requeueing source: %w", err)
		}
		return s.audit(ctx, q, actor, ActionSourceRequeued, "source:"+sourceID.String(),
			map[string]any{"target_state": targetState, "rows": n, metaNote: note})
	})
	return n, err
}

// RequeueOpportunity re-runs one record.
func (s *Service) RequeueOpportunity(
	ctx context.Context, actor, oppID pgtype.UUID, targetState, note string,
) error {
	if err := validateRequeueTarget(targetState); err != nil {
		return err
	}
	return s.inTx(ctx, func(q *store.Queries) error {
		n, err := q.AdminRequeueOpportunity(ctx, store.AdminRequeueOpportunityParams{
			TargetState: targetState, OpportunityID: oppID,
		})
		if err != nil {
			return fmt.Errorf("admin: requeueing opportunity: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return s.audit(ctx, q, actor, ActionOpportunityRequeued, "opportunity:"+oppID.String(),
			map[string]any{"target_state": targetState, metaNote: note})
	})
}

// validateRequeueTarget accepts only states a worker will pick up again.
//
// 'ready' is excluded on purpose: requeueing TO ready would skip the work the
// re-run was requested for, which looks like success and is not.
func validateRequeueTarget(state string) error {
	switch pipeline.State(state) {
	case pipeline.StateFetched, pipeline.StateParsed, pipeline.StateNormalized,
		pipeline.StateDeduped, pipeline.StateEnriched:
		return nil
	default:
		return fmt.Errorf("%w: %q is not a re-runnable state", ErrInvalidStatus, state)
	}
}

// ---------------------------------------------------------------- provenance

// Provenance is everything known about where a posting came from.
type Provenance struct {
	Sources    []store.AdminOpportunitySourcesRow
	MergedInto []store.AdminListMergedIntoRow
}

// Provenance returns every source row for a posting plus what was merged into it.
func (s *Service) Provenance(ctx context.Context, oppID pgtype.UUID) (*Provenance, error) {
	sources, err := s.q.AdminOpportunitySources(ctx, oppID)
	if err != nil {
		return nil, fmt.Errorf("admin: loading provenance: %w", err)
	}
	merged, err := s.q.AdminListMergedInto(ctx, oppID)
	if err != nil {
		return nil, fmt.Errorf("admin: loading merged postings: %w", err)
	}
	return &Provenance{Sources: sources, MergedInto: merged}, nil
}

// ErrNotReversible means the merge predates the column that records which source
// rows it moved, so reversing it would be guesswork.
var ErrNotReversible = errors.New(
	"admin: this merge did not record which source rows it moved and cannot be reversed automatically")

// Unmerge restores a posting that dedup merged into another.
//
// The operation that makes hard rule 11 real. A false merge hides a real job and
// is otherwise invisible — the user simply never sees the role, and nothing looks
// broken — so this is the only way back.
//
// Three statements, in one transaction, because a crash between any two of them
// leaves a posting neither merged nor visible:
//
//  1. Move back exactly the opportunity_source rows the merge moved, and clear
//     the merge provenance from them. Exactly those rows, from the recorded ids:
//     with two merges into one canonical there is nothing to infer from.
//  2. Clear merged_into, stamp unmerged_at, and resume the posting at 'deduped'.
//     unmerged_at is what stops dedup merging it straight back on the next pass —
//     without it an operator watches their un-merge undo itself.
//  3. Stamp the merge record reversed, so the history says what happened rather
//     than losing the fact that a merge ever occurred.
func (s *Service) Unmerge(ctx context.Context, actor, oppID pgtype.UUID, note string) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		merge, err := q.FindLatestMergeFor(ctx, oppID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("admin: finding the merge: %w", err)
		}
		if merge.MovedSourceIds == nil {
			return ErrNotReversible
		}

		restored, err := q.RestoreSourceRows(ctx, store.RestoreSourceRowsParams{
			BackToID: oppID, SourceRowIds: merge.MovedSourceIds,
		})
		if err != nil {
			return fmt.Errorf("admin: restoring source rows: %w", err)
		}

		visible, err := q.RestoreMergedOpportunity(ctx, oppID)
		if err != nil {
			return fmt.Errorf("admin: restoring the posting: %w", err)
		}
		if visible == 0 {
			// Not currently merged. Either already reversed or never merged; either
			// way there is nothing to do and pretending otherwise would leave the
			// source rows moved with no posting marked.
			return ErrAlreadyResolved
		}

		if _, err := q.UndoMerge(ctx, merge.ID); err != nil {
			return fmt.Errorf("admin: marking the merge reversed: %w", err)
		}

		return s.audit(ctx, q, actor, ActionMergeUndone, "opportunity:"+oppID.String(),
			map[string]any{
				"merge_id":             merge.ID.String(),
				"was_merged_into":      merge.IntoOpportunityID.String(),
				"source_rows_restored": restored,
				"merge_reason":         merge.Reason,
				"note":                 note,
			})
	})
}

// ListMergeCandidates returns merges dedup declined to make automatically.
func (s *Service) ListMergeCandidates(
	ctx context.Context, pageSize int32,
) ([]store.AdminListMergeCandidatesRow, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	rows, err := s.q.AdminListMergeCandidates(ctx, pageSize)
	if err != nil {
		return nil, fmt.Errorf("admin: listing merge candidates: %w", err)
	}
	return rows, nil
}

// ResolveMergeCandidate records a human decision on a withheld merge.
func (s *Service) ResolveMergeCandidate(
	ctx context.Context, actor, candidateID pgtype.UUID, resolution, note string,
) error {
	if resolution != MergeConfirmed && resolution != MergeRejected {
		return fmt.Errorf("%w: %q", ErrInvalidStatus, resolution)
	}
	return s.inTx(ctx, func(q *store.Queries) error {
		row, err := q.AdminResolveMergeCandidate(ctx, store.AdminResolveMergeCandidateParams{
			CandidateID: candidateID, Resolution: &resolution,
			ResolvedBy: strPtr(actor.String()),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Either unknown or already decided. Both mean "do not act".
				return ErrAlreadyResolved
			}
			return fmt.Errorf("admin: resolving merge candidate: %w", err)
		}
		return s.audit(ctx, q, actor, ActionMergeResolved, "merge_candidate:"+candidateID.String(),
			map[string]any{
				"resolution": resolution, metaNote: note,
				"left":  row.LeftOpportunityID.String(),
				"right": row.RightOpportunityID.String(),
			})
	})
}

// ---------------------------------------------------------------- flags

// RaiseFlag records a user report against a posting.
//
// Audited with the reporter as actor, because a flag is an action a person took
// and the queue needs to be attributable — including when the same person files
// many.
func (s *Service) RaiseFlag(
	ctx context.Context, reporter, oppID pgtype.UUID, reason string, detail *string,
) (pgtype.UUID, error) {
	var id pgtype.UUID
	if !contains(FlagReasons, reason) {
		return id, fmt.Errorf("%w: %q", ErrInvalidReason, reason)
	}
	err := s.inTx(ctx, func(q *store.Queries) error {
		row, err := q.CreateFlag(ctx, store.CreateFlagParams{
			OpportunityID: oppID, ReportedBy: reporter, Reason: reason, Detail: detail,
		})
		if err != nil {
			if isUniqueViolation(err) {
				// Already reported by this user. Not an error worth surfacing as a
				// failure: their point is already in the queue.
				return ErrAlreadyResolved
			}
			if isForeignKeyViolation(err) {
				return ErrNotFound
			}
			return fmt.Errorf("admin: creating flag: %w", err)
		}
		id = row.ID
		return s.audit(ctx, q, reporter, ActionFlagRaised, "opportunity:"+oppID.String(),
			map[string]any{"reason": reason})
	})
	return id, err
}

// ListFlags returns the review queue.
func (s *Service) ListFlags(
	ctx context.Context, status *string, pageSize int32,
) ([]store.AdminListFlagsRow, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	rows, err := s.q.AdminListFlags(ctx, store.AdminListFlagsParams{
		Status: status, PageSize: pageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("admin: listing flags: %w", err)
	}
	return rows, nil
}

// ResolveFlag records a decision on a reported listing.
//
// Upholding a flag does not itself close the posting. Closure has one cause —
// a successful poll in which the posting was absent (hard rule 9) — and giving
// admins a second path to it would make the liveness guarantee unverifiable.
// Quarantining the source or purging it are the levers for a bad source; a single
// fraudulent posting is handled by upholding the flag and letting the corpus
// reflect it.
func (s *Service) ResolveFlag(
	ctx context.Context, actor, flagID pgtype.UUID, status string, note *string,
) error {
	switch status {
	case FlagUpheld, FlagRejected, FlagDuplicated:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidStatus, status)
	}
	return s.inTx(ctx, func(q *store.Queries) error {
		row, err := q.AdminResolveFlag(ctx, store.AdminResolveFlagParams{
			FlagID: flagID, Status: status, ResolutionNote: note, ResolvedBy: actor,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAlreadyResolved
			}
			return fmt.Errorf("admin: resolving flag: %w", err)
		}
		return s.audit(ctx, q, actor, ActionFlagResolved, "flag:"+flagID.String(),
			map[string]any{
				metaStatus: status, "opportunity": row.OpportunityID.String(),
			})
	})
}

// ---------------------------------------------------------------- purge

// PurgePlan is what a source purge would do, counted before it runs.
type PurgePlan struct {
	SourceID pgtype.UUID
	// Total postings attributed to this source.
	Total int64
	// Merged is how many of those were merged into another posting.
	Merged int64
	// AlsoSeenElsewhere will SURVIVE the purge: another source vouches for them.
	AlsoSeenElsewhere int64
	// WillBeDeleted is what actually goes.
	WillBeDeleted int64
}

// PlanSourcePurge counts what a purge would remove without removing anything.
//
// Required before Purge, and the count is the confirmation token. A destructive
// operation that runs on a number the operator has not seen is how the wrong
// source gets emptied.
func (s *Service) PlanSourcePurge(ctx context.Context, sourceID pgtype.UUID) (*PurgePlan, error) {
	row, err := s.q.AdminCountSourcePostings(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("admin: planning purge: %w", err)
	}
	return &PurgePlan{
		SourceID: sourceID, Total: row.Total, Merged: row.Merged,
		AlsoSeenElsewhere: row.AlsoSeenElsewhere,
		WillBeDeleted:     row.Total - row.AlsoSeenElsewhere,
	}, nil
}

// PurgeResult is what a purge actually did.
type PurgeResult struct {
	SourceRowsDeleted    int64
	OpportunitiesDeleted int64
	// MergeRecordsDeleted is merge history that went with the postings. Reported
	// because losing it is a real consequence, not an implementation detail.
	MergeRecordsDeleted int64
	// DryRun reports that nothing was written. The drill blueprint §30 asks for
	// runs this way, so the exercise is real without being destructive.
	DryRun bool
}

// PurgeSource removes one source's contribution to the corpus.
//
// The only genuinely destructive operation in this package, so it is the most
// guarded:
//
//   - The caller must pass the count PlanSourcePurge reported. A mismatch aborts,
//     because the corpus moved under them and the number they approved is not the
//     number they would get.
//   - Provenance rows are deleted first, then only postings left with NO
//     provenance at all. A posting also seen on another source survives; deleting
//     by source would take those with it, which is data loss disguised as cleanup.
//   - dryRun performs the count and the audit entry without writing, so the drill
//     the blueprint asks for is a real rehearsal rather than a comment.
func (s *Service) PurgeSource(
	ctx context.Context, actor, sourceID pgtype.UUID,
	confirmDeleteCount int64, dryRun bool, note string,
) (*PurgeResult, error) {
	plan, err := s.PlanSourcePurge(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if plan.WillBeDeleted != confirmDeleteCount {
		return nil, fmt.Errorf("%w: plan says %d postings would be deleted, caller confirmed %d",
			ErrConfirmationMismatch, plan.WillBeDeleted, confirmDeleteCount)
	}

	out := &PurgeResult{DryRun: dryRun}
	err = s.inTx(ctx, func(q *store.Queries) error {
		if !dryRun {
			// Capture what this source touched BEFORE removing its provenance.
			// Everything the purge may delete is bounded by this list; a table-wide
			// orphan sweep would take unrelated postings with it.
			candidates, err := q.AdminSourceOpportunityIDs(ctx, sourceID)
			if err != nil {
				return fmt.Errorf("admin: listing the source's postings: %w", err)
			}

			n, err := q.AdminDeleteSourceRows(ctx, sourceID)
			if err != nil {
				return fmt.Errorf("admin: deleting source rows: %w", err)
			}
			out.SourceRowsDeleted = n

			// Merge records reference these postings with no ON DELETE clause, so
			// they block the delete until removed. Counted, because merge history
			// going away is worth reporting rather than doing quietly.
			mr, err := q.AdminDeleteMergeRecordsFor(ctx, candidates)
			if err != nil {
				return fmt.Errorf("admin: clearing merge records: %w", err)
			}
			out.MergeRecordsDeleted = mr

			m, err := q.AdminDeleteOrphanedAmong(ctx, candidates)
			if err != nil {
				return fmt.Errorf("admin: deleting orphaned postings: %w", err)
			}
			out.OpportunitiesDeleted = m
		}
		// Audited either way. A rehearsal that leaves no trace is not a rehearsal.
		return s.audit(ctx, q, actor, ActionSourcePurged, "source:"+sourceID.String(),
			map[string]any{
				"dry_run": dryRun, metaNote: note,
				"planned_deletions":     plan.WillBeDeleted,
				"survived_elsewhere":    plan.AlsoSeenElsewhere,
				"source_rows_deleted":   out.SourceRowsDeleted,
				"postings_deleted":      out.OpportunitiesDeleted,
				"merge_records_deleted": out.MergeRecordsDeleted,
			})
	})
	if err != nil {
		return nil, err
	}
	s.log.Warn("source purge executed",
		"source_id", sourceID.String(), "dry_run", dryRun,
		"postings_deleted", out.OpportunitiesDeleted)
	return out, nil
}

// ---------------------------------------------------------------- helpers

func strPtr(s string) *string { return &s }

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func isUniqueViolation(err error) bool { return sqlState(err) == "23505" }

func isForeignKeyViolation(err error) bool { return sqlState(err) == "23503" }

func sqlState(err error) string {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState()
	}
	return ""
}
