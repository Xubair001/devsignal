//go:build integration

package admin

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/store"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

type fixture struct {
	pool    *pgxpool.Pool
	svc     *Service
	admin   pgtype.UUID
	user    pgtype.UUID
	source  pgtype.UUID
	company pgtype.UUID
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Pool(t)
	f := &fixture{pool: pool, svc: New(pool, quietLog())}

	var tenantID pgtype.UUID
	must(t, pool.QueryRow(ctx,
		`INSERT INTO tenant (display_name) VALUES ('Admin Test') RETURNING id`).Scan(&tenantID))
	must(t, pool.QueryRow(ctx, `
		INSERT INTO app_user (tenant_id, email, password_hash, role)
		VALUES ($1,$2,'x','admin') RETURNING id`,
		tenantID, "adm-"+uuid.NewString()[:8]+"@example.com").Scan(&f.admin))
	must(t, pool.QueryRow(ctx, `
		INSERT INTO app_user (tenant_id, email, password_hash)
		VALUES ($1,$2,'x') RETURNING id`,
		tenantID, "usr-"+uuid.NewString()[:8]+"@example.com").Scan(&f.user))
	must(t, pool.QueryRow(ctx, `
		INSERT INTO source (name, tier, type, legal_basis, poll_interval)
		VALUES ($1,'a','test','test', interval '1 hour') RETURNING id`,
		"adm-src-"+uuid.NewString()[:8]).Scan(&f.source))
	must(t, pool.QueryRow(ctx, `
		INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Admin Co') RETURNING id`,
		"adm-"+uuid.NewString()[:8]+".example").Scan(&f.company))

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, f.company)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, f.company)
		_, _ = pool.Exec(c, `DELETE FROM source WHERE id=$1`, f.source)
		_, _ = pool.Exec(c, `DELETE FROM app_user WHERE id = ANY($1)`,
			[]pgtype.UUID{f.admin, f.user})
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id=$1`, tenantID)
	})
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// posting inserts an opportunity with one opportunity_source row.
func (f *fixture) posting(t *testing.T, title string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var id pgtype.UUID
	must(t, f.pool.QueryRow(ctx, `
		INSERT INTO opportunity (company_id, title_raw, title_normalized, pipeline_state, block_key)
		VALUES ($1,$2,$2,'ready',$3) RETURNING id`,
		f.company, title, "blk-"+uuid.NewString()[:8]).Scan(&id))
	jobID := uuid.NewString()[:12]
	_, err := f.pool.Exec(ctx, `
		INSERT INTO opportunity_source
		  (opportunity_id, source_id, ats_type, ats_job_id, source_job_id, apply_url)
		VALUES ($1,$2,'greenhouse',$3,$3,'https://example.invalid/'||$3)`,
		id, f.source, jobID)
	must(t, err)
	return id
}

// ---------------------------------------------------------------- authorization

// The gate everything else depends on.
func TestOnlyAdminsAreAuthorized(t *testing.T) {
	ctx := context.Background()
	f := setup(t)

	if err := f.svc.Authorize(ctx, f.admin); err != nil {
		t.Errorf("an admin was refused: %v", err)
	}
	if err := f.svc.Authorize(ctx, f.user); !errors.Is(err, ErrForbidden) {
		t.Errorf("a normal user got %v, want ErrForbidden", err)
	}

	var unknown pgtype.UUID
	unknown.Bytes = [16]byte{7, 7, 7}
	unknown.Valid = true
	if err := f.svc.Authorize(ctx, unknown); !errors.Is(err, ErrForbidden) {
		t.Errorf("an unknown user got %v, want ErrForbidden", err)
	}
}

// A suspended admin must lose the surface immediately: the query requires an
// active account, not merely the role.
func TestSuspendedAdminLosesAccess(t *testing.T) {
	ctx := context.Background()
	f := setup(t)

	_, err := f.pool.Exec(ctx, `UPDATE app_user SET status='suspended' WHERE id=$1`, f.admin)
	must(t, err)

	if err := f.svc.Authorize(ctx, f.admin); !errors.Is(err, ErrForbidden) {
		t.Errorf("a suspended admin got %v, want ErrForbidden", err)
	}
}

// ---------------------------------------------------------------- audit

// Every admin action must land in the hash-chained log, in the same transaction
// as the change. An action that is not recorded is indistinguishable from one
// that never happened.
func TestAdminActionsAreAudited(t *testing.T) {
	ctx := context.Background()
	f := setup(t)

	before := f.auditCount(t)
	if _, err := f.svc.SetSourceStatus(ctx, f.admin, f.source, StatusQuarantined, "emitting garbage"); err != nil {
		t.Fatal(err)
	}
	if after := f.auditCount(t); after != before+1 {
		t.Errorf("audit entries went %d -> %d, want one more", before, after)
	}

	var action, subject string
	var meta []byte
	must(t, f.pool.QueryRow(ctx, `
		SELECT action, subject, metadata FROM audit_log
		 WHERE actor_id=$1 ORDER BY id DESC LIMIT 1`, f.admin).Scan(&action, &subject, &meta))
	if action != ActionSourceQuarantined {
		t.Errorf("action = %q, want %q", action, ActionSourceQuarantined)
	}
	if subject == "" {
		t.Error("audit entry has no subject")
	}
	// The note the operator gave must survive: an unexplained quarantine is what
	// confuses whoever is on call next.
	if !bytesContain(meta, "emitting garbage") {
		t.Errorf("the operator's note is not in the audit metadata: %s", meta)
	}
}

func (f *fixture) auditCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	must(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log`).Scan(&n))
	return n
}

func bytesContain(b []byte, sub string) bool {
	return len(b) > 0 && stringContains(string(b), sub)
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- quarantine

// Quarantine stops polling and NOTHING else. Hard rule 9: closure requires a
// successful poll in which the posting was absent, so a quarantined source must
// not take its postings down with it.
func TestQuarantineDoesNotCloseThePostings(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Senior Backend Engineer")

	if _, err := f.svc.SetSourceStatus(ctx, f.admin, f.source, StatusQuarantined, "parser rot"); err != nil {
		t.Fatal(err)
	}

	var closedAt pgtype.Timestamptz
	var state string
	must(t, f.pool.QueryRow(ctx,
		`SELECT closed_at, pipeline_state FROM opportunity WHERE id=$1`, oppID).
		Scan(&closedAt, &state))
	if closedAt.Valid {
		t.Error("quarantining a source closed its postings; one outage would delete the corpus")
	}
	if state != "ready" {
		t.Errorf("posting state = %q, want it untouched at ready", state)
	}
}

func TestQuarantineIsReversible(t *testing.T) {
	ctx := context.Background()
	f := setup(t)

	for _, want := range []string{StatusQuarantined, StatusActive, StatusRetired, StatusActive} {
		row, err := f.svc.SetSourceStatus(ctx, f.admin, f.source, want, "")
		if err != nil {
			t.Fatalf("setting %s: %v", want, err)
		}
		if row.Status != want {
			t.Errorf("status = %q, want %q", row.Status, want)
		}
	}
}

func TestUnknownSourceStatusIsRejected(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.svc.SetSourceStatus(ctx, f.admin, f.source, "paused", ""); !errors.Is(err, ErrInvalidStatus) {
		t.Errorf("got %v, want ErrInvalidStatus", err)
	}
}

// ---------------------------------------------------------------- un-merge

// The operation that makes hard rule 11 real, end to end.
func TestUnmergeRestoresThePostingAndItsSourceRows(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	canonical := f.posting(t, "Staff Backend Engineer")
	duplicate := f.posting(t, "Staff Backend Engineer")

	f.merge(t, duplicate, canonical)

	// Precondition: the duplicate is hidden and its source row moved.
	if f.sourceRowCount(t, duplicate) != 0 {
		t.Fatal("merge did not move the source row")
	}
	if f.sourceRowCount(t, canonical) != 2 {
		t.Fatalf("canonical has %d source rows, want 2", f.sourceRowCount(t, canonical))
	}

	if err := f.svc.Unmerge(ctx, f.admin, duplicate, "different requisitions"); err != nil {
		t.Fatalf("unmerge: %v", err)
	}

	// The source row went back to exactly the posting it came from.
	if got := f.sourceRowCount(t, duplicate); got != 1 {
		t.Errorf("restored posting has %d source rows, want 1", got)
	}
	if got := f.sourceRowCount(t, canonical); got != 1 {
		t.Errorf("canonical kept %d source rows, want 1", got)
	}

	// And the posting is visible again, marked as a human decision.
	var mergedInto pgtype.UUID
	var unmergedAt pgtype.Timestamptz
	var state string
	must(t, f.pool.QueryRow(ctx,
		`SELECT merged_into, unmerged_at, pipeline_state FROM opportunity WHERE id=$1`,
		duplicate).Scan(&mergedInto, &unmergedAt, &state))
	if mergedInto.Valid {
		t.Error("merged_into was not cleared")
	}
	if !unmergedAt.Valid {
		t.Error("unmerged_at not stamped; dedup would merge it straight back")
	}
	if state != "deduped" {
		t.Errorf("state = %q, want deduped so it resumes after the dedupe stage", state)
	}

	// The merge provenance is cleared from the restored row, not left claiming it
	// was merged by dedupe.
	var reason *string
	must(t, f.pool.QueryRow(ctx,
		`SELECT merge_reason FROM opportunity_source WHERE opportunity_id=$1`, duplicate).
		Scan(&reason))
	if reason != nil {
		t.Errorf("restored source row still claims merge_reason %q", *reason)
	}
}

// The history must still say a merge happened. Losing that would make the
// un-merge itself unexplainable later.
func TestUnmergeMarksTheMergeRecordRatherThanDeletingIt(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	canonical := f.posting(t, "Backend Engineer")
	duplicate := f.posting(t, "Backend Engineer")
	f.merge(t, duplicate, canonical)

	if err := f.svc.Unmerge(ctx, f.admin, duplicate, ""); err != nil {
		t.Fatal(err)
	}

	var undoneAt pgtype.Timestamptz
	var rows int
	must(t, f.pool.QueryRow(ctx, `
		SELECT count(*), max(undone_at) FROM opportunity_merge WHERE from_opportunity_id=$1`,
		duplicate).Scan(&rows, &undoneAt))
	if rows != 1 {
		t.Errorf("%d merge records, want the original kept", rows)
	}
	if !undoneAt.Valid {
		t.Error("the merge record was not marked reversed")
	}
}

// A merge recorded before moved_source_ids existed cannot be reversed exactly, so
// it must fail loudly rather than guess which rows to move.
func TestUnmergeRefusesAMergeWithNoRecordedSourceRows(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	canonical := f.posting(t, "Platform Engineer")
	duplicate := f.posting(t, "Platform Engineer")
	f.merge(t, duplicate, canonical)

	// Simulate a historical row.
	_, err := f.pool.Exec(ctx,
		`UPDATE opportunity_merge SET moved_source_ids = NULL WHERE from_opportunity_id=$1`,
		duplicate)
	must(t, err)

	if err := f.svc.Unmerge(ctx, f.admin, duplicate, ""); !errors.Is(err, ErrNotReversible) {
		t.Errorf("got %v, want ErrNotReversible", err)
	}
}

func TestUnmergingSomethingNotMergedIsRejected(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Never Merged")
	if err := f.svc.Unmerge(ctx, f.admin, oppID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// merge performs a real merge through the same statements dedupe uses.
func (f *fixture) merge(t *testing.T, from, into pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	q := store.New(f.pool)

	reason, conf := "exact_ats", float32(1.0)
	moved, err := q.MoveSourceRows(ctx, store.MoveSourceRowsParams{
		IntoID: into, Reason: &reason, Confidence: &conf, FromID: from,
	})
	must(t, err)
	if _, err := q.MarkMerged(ctx, store.MarkMergedParams{IntoID: into, FromID: from}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RecordMerge(ctx, store.RecordMergeParams{
		FromOpportunityID: from, IntoOpportunityID: into, Reason: reason,
		Confidence: &conf, SourceRowsMoved: int32(len(moved)),
		MergedBy: "test", MovedSourceIds: moved,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) sourceRowCount(t *testing.T, oppID pgtype.UUID) int {
	t.Helper()
	var n int
	must(t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM opportunity_source WHERE opportunity_id=$1`, oppID).Scan(&n))
	return n
}

// ---------------------------------------------------------------- flags

func TestFlagLifecycle(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Suspicious Listing")

	detail := "asks for a payment up front"
	id, err := f.svc.RaiseFlag(ctx, f.user, oppID, FlagScamOrFraud, &detail)
	if err != nil {
		t.Fatal(err)
	}

	open, err := f.svc.ListFlags(ctx, strPtr("open"), 50)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, fl := range open {
		if fl.ID == id {
			found = true
			if fl.Detail == nil || *fl.Detail != detail {
				t.Errorf("detail = %v, want the reporter's text", fl.Detail)
			}
		}
	}
	if !found {
		t.Fatal("the flag is not in the open queue")
	}

	if err := f.svc.ResolveFlag(ctx, f.admin, id, FlagUpheld, strPtr("confirmed scam")); err != nil {
		t.Fatal(err)
	}
	// Resolving twice must not silently succeed.
	if err := f.svc.ResolveFlag(ctx, f.admin, id, FlagRejected, nil); !errors.Is(err, ErrAlreadyResolved) {
		t.Errorf("re-resolving gave %v, want ErrAlreadyResolved", err)
	}
}

// One open flag per user per posting: re-reporting is not more signal, and it
// would let one person flood the queue.
func TestDuplicateFlagFromSameUserIsRejected(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Reported Twice")

	if _, err := f.svc.RaiseFlag(ctx, f.user, oppID, FlagNotARealJob, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RaiseFlag(ctx, f.user, oppID, FlagScamOrFraud, nil); !errors.Is(err, ErrAlreadyResolved) {
		t.Errorf("second flag gave %v, want ErrAlreadyResolved", err)
	}
}

func TestUnknownFlagReasonIsRejected(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Whatever")
	if _, err := f.svc.RaiseFlag(ctx, f.user, oppID, "i just do not like it", nil); !errors.Is(err, ErrInvalidReason) {
		t.Errorf("got %v, want ErrInvalidReason", err)
	}
}

// Upholding a flag must NOT close the posting. Closure has exactly one cause
// (hard rule 9), and a second path to it would make liveness unverifiable.
func TestUpholdingAFlagDoesNotCloseThePosting(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Flagged But Open")

	id, err := f.svc.RaiseFlag(ctx, f.user, oppID, FlagScamOrFraud, nil)
	must(t, err)
	must(t, f.svc.ResolveFlag(ctx, f.admin, id, FlagUpheld, nil))

	var closedAt pgtype.Timestamptz
	must(t, f.pool.QueryRow(ctx,
		`SELECT closed_at FROM opportunity WHERE id=$1`, oppID).Scan(&closedAt))
	if closedAt.Valid {
		t.Error("upholding a flag closed the posting; closure has one cause and this is not it")
	}
}

// ---------------------------------------------------------------- re-runs

func TestRequeueResetsRetryStateSoWorkersPickItUp(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Stuck Posting")

	// A record that already exhausted its retries.
	_, err := f.pool.Exec(ctx, `
		UPDATE opportunity SET pipeline_state='failed_permanent', attempts=5,
		       last_error='boom', next_attempt_at=now() + interval '1 day'
		 WHERE id=$1`, oppID)
	must(t, err)

	must(t, f.svc.RequeueOpportunity(ctx, f.admin, oppID, "normalized", "parser fixed"))

	var state string
	var attempts int
	var lastErr *string
	must(t, f.pool.QueryRow(ctx,
		`SELECT pipeline_state, attempts, last_error FROM opportunity WHERE id=$1`, oppID).
		Scan(&state, &attempts, &lastErr))
	if state != "normalized" {
		t.Errorf("state = %q, want normalized", state)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d; a record with spent retries would never be claimed again", attempts)
	}
	if lastErr != nil {
		t.Errorf("last_error still %q", *lastErr)
	}
}

// 'ready' would skip the work the re-run was requested for, which looks like
// success and is not.
func TestRequeueRejectsNonRerunnableStates(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	oppID := f.posting(t, "Target State Check")

	for _, bad := range []string{"ready", "discovered", "failed_permanent", "banana"} {
		if err := f.svc.RequeueOpportunity(ctx, f.admin, oppID, bad, ""); !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("target %q gave %v, want ErrInvalidStatus", bad, err)
		}
	}
}

func TestRequeueSourceTouchesOnlyThatSource(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	mine := f.posting(t, "From My Source")

	// A posting belonging to a different source.
	var otherSource, other pgtype.UUID
	must(t, f.pool.QueryRow(ctx, `
		INSERT INTO source (name, tier, type, legal_basis, poll_interval)
		VALUES ($1,'a','test','test', interval '1 hour') RETURNING id`,
		"other-"+uuid.NewString()[:8]).Scan(&otherSource))
	must(t, f.pool.QueryRow(ctx, `
		INSERT INTO opportunity (company_id, title_raw, title_normalized, pipeline_state)
		VALUES ($1,'Other Source Posting','other source posting','ready') RETURNING id`,
		f.company).Scan(&other))
	_, err := f.pool.Exec(ctx, `
		INSERT INTO opportunity_source (opportunity_id, source_id, source_job_id)
		VALUES ($1,$2,$3)`, other, otherSource, uuid.NewString()[:12])
	must(t, err)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM opportunity WHERE id=$1`, other)
		_, _ = f.pool.Exec(c, `DELETE FROM source WHERE id=$1`, otherSource)
	})

	n, err := f.svc.RequeueSource(ctx, f.admin, f.source, "normalized", "")
	must(t, err)
	if n < 1 {
		t.Fatalf("requeued %d postings, want at least the one from this source", n)
	}

	var mineState, otherState string
	must(t, f.pool.QueryRow(ctx, `SELECT pipeline_state FROM opportunity WHERE id=$1`, mine).Scan(&mineState))
	must(t, f.pool.QueryRow(ctx, `SELECT pipeline_state FROM opportunity WHERE id=$1`, other).Scan(&otherState))
	if mineState != "normalized" {
		t.Errorf("this source's posting = %q, want normalized", mineState)
	}
	if otherState != "ready" {
		t.Errorf("another source's posting was requeued to %q", otherState)
	}
}

// ---------------------------------------------------------------- purge

// The purge plan must count what would go and what would survive, before
// anything is written.
func TestPurgePlanCountsSurvivorsSeparately(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	onlyMine := f.posting(t, "Only On My Source")
	shared := f.posting(t, "Seen On Two Sources")

	// Give `shared` a second source row.
	var otherSource pgtype.UUID
	must(t, f.pool.QueryRow(ctx, `
		INSERT INTO source (name, tier, type, legal_basis, poll_interval)
		VALUES ($1,'a','test','test', interval '1 hour') RETURNING id`,
		"second-"+uuid.NewString()[:8]).Scan(&otherSource))
	_, err := f.pool.Exec(ctx, `
		INSERT INTO opportunity_source (opportunity_id, source_id, source_job_id)
		VALUES ($1,$2,$3)`, shared, otherSource, uuid.NewString()[:12])
	must(t, err)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM source WHERE id=$1`, otherSource)
	})

	plan, err := f.svc.PlanSourcePurge(ctx, f.source)
	must(t, err)

	if plan.Total != 2 {
		t.Errorf("total = %d, want 2", plan.Total)
	}
	if plan.AlsoSeenElsewhere != 1 {
		t.Errorf("also seen elsewhere = %d, want 1", plan.AlsoSeenElsewhere)
	}
	if plan.WillBeDeleted != 1 {
		t.Errorf("will be deleted = %d, want 1", plan.WillBeDeleted)
	}
	_ = onlyMine
}

// The guard: a purge on a stale number must abort. The corpus moved under the
// operator and the count they approved is not the count they would get.
func TestPurgeRefusesAMismatchedConfirmation(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	f.posting(t, "Will Not Be Purged")

	_, err := f.svc.PurgeSource(ctx, f.admin, f.source, 999, false, "")
	if !errors.Is(err, ErrConfirmationMismatch) {
		t.Errorf("got %v, want ErrConfirmationMismatch", err)
	}
	// And nothing was removed.
	var n int
	must(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity_source WHERE source_id=$1`, f.source).Scan(&n))
	if n == 0 {
		t.Error("a refused purge still deleted rows")
	}
}

// The drill blueprint §30 asks for: a real rehearsal that writes nothing but is
// audited, so the exercise leaves a trace.
func TestPurgeDryRunDeletesNothingButIsAudited(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	f.posting(t, "Drill Subject")

	plan, err := f.svc.PlanSourcePurge(ctx, f.source)
	must(t, err)
	before := f.auditCount(t)

	res, err := f.svc.PurgeSource(ctx, f.admin, f.source, plan.WillBeDeleted, true, "quarterly drill")
	must(t, err)

	if !res.DryRun || res.OpportunitiesDeleted != 0 || res.SourceRowsDeleted != 0 {
		t.Errorf("a dry run reported deletions: %+v", res)
	}
	var n int
	must(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity_source WHERE source_id=$1`, f.source).Scan(&n))
	if n == 0 {
		t.Error("the dry run deleted rows")
	}
	if after := f.auditCount(t); after != before+1 {
		t.Error("the drill left no audit entry; a rehearsal with no trace is not a rehearsal")
	}
}

// A posting seen on a second source must SURVIVE the purge of the first.
// Deleting by source would take those with it, which is data loss disguised as
// cleanup.
func TestPurgeSpareaPostingsSeenElsewhere(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	onlyMine := f.posting(t, "Only Mine")
	shared := f.posting(t, "Shared")

	var otherSource pgtype.UUID
	must(t, f.pool.QueryRow(ctx, `
		INSERT INTO source (name, tier, type, legal_basis, poll_interval)
		VALUES ($1,'a','test','test', interval '1 hour') RETURNING id`,
		"keep-"+uuid.NewString()[:8]).Scan(&otherSource))
	_, err := f.pool.Exec(ctx, `
		INSERT INTO opportunity_source (opportunity_id, source_id, source_job_id)
		VALUES ($1,$2,$3)`, shared, otherSource, uuid.NewString()[:12])
	must(t, err)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM source WHERE id=$1`, otherSource)
	})

	plan, err := f.svc.PlanSourcePurge(ctx, f.source)
	must(t, err)
	res, err := f.svc.PurgeSource(ctx, f.admin, f.source, plan.WillBeDeleted, false, "source retired")
	must(t, err)

	if res.OpportunitiesDeleted != 1 {
		t.Errorf("deleted %d postings, want exactly the one with no other source",
			res.OpportunitiesDeleted)
	}

	if f.exists(t, onlyMine) {
		t.Error("the posting with no other provenance survived the purge")
	}
	if !f.exists(t, shared) {
		t.Error("a posting seen on another source was deleted; that is data loss, not cleanup")
	}
}

func (f *fixture) exists(t *testing.T, oppID pgtype.UUID) bool {
	t.Helper()
	var n int
	must(t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM opportunity WHERE id=$1`, oppID).Scan(&n))
	return n > 0
}
