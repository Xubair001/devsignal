//go:build integration

package engagement

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/store"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testPool(t *testing.T) *pgxpool.Pool { return dbtest.Pool(t) }

func seedUserAndPosting(t *testing.T, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()

	var tenantID, userID, companyID, oppID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (display_name) VALUES ('Engagement Test') RETURNING id`).
		Scan(&tenantID); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO app_user (tenant_id, email, password_hash) VALUES ($1,$2,'x') RETURNING id`,
		tenantID, "eng-"+uuid.NewString()[:8]+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Eng Co') RETURNING id`,
		"eng-"+uuid.NewString()[:8]+".example").Scan(&companyID); err != nil {
		t.Fatalf("company: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO opportunity (company_id, title_raw, title_normalized, pipeline_state)
		VALUES ($1,'Senior Backend Engineer','senior backend engineer','ready') RETURNING id`,
		companyID).Scan(&oppID); err != nil {
		t.Fatalf("opportunity: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM app_user WHERE id=$1`, userID)
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id=$1`, tenantID)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})
	return userID, oppID
}

func countEvents(t *testing.T, pool *pgxpool.Pool, userID pgtype.UUID, eventType string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM engagement_event WHERE user_id=$1 AND event_type=$2`,
		userID, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The core guarantee: un-saving appends, it does not delete. A decision record
// that can be edited is not a record, and a save the user took back is a
// different label from one they kept.
func TestUnsaveAppendsRatherThanDeleting(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	if err := svc.Save(ctx, userID, oppID, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.Unsave(ctx, userID, oppID); err != nil {
		t.Fatal(err)
	}

	if n := countEvents(t, pool, userID, EventSaved); n != 1 {
		t.Errorf("%d saved events after an unsave; the original must survive", n)
	}
	if n := countEvents(t, pool, userID, EventUnsaved); n != 1 {
		t.Errorf("%d unsaved events, want 1", n)
	}

	// And the derived state says not-saved.
	state, err := svc.StateFor(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if state[oppID.String()].Saved {
		t.Error("state says saved after an unsave")
	}
}

// Save state is the LATEST of saved/unsaved, so re-saving works.
func TestReSavingAfterUnsaveIsSaved(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	for _, act := range []func() error{
		func() error { return svc.Save(ctx, userID, oppID, nil) },
		func() error { return svc.Unsave(ctx, userID, oppID) },
		func() error { return svc.Save(ctx, userID, oppID, nil) },
	} {
		if err := act(); err != nil {
			t.Fatal(err)
		}
		// The log's ordering is by occurred_at, so distinct timestamps matter.
		time.Sleep(2 * time.Millisecond)
	}

	state, err := svc.StateFor(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !state[oppID.String()].Saved {
		t.Error("re-saving after an unsave did not restore the saved state")
	}
	if n := countEvents(t, pool, userID, EventSaved); n != 2 {
		t.Errorf("%d saved events, want 2 — every action is kept", n)
	}
}

// Applying is not reversible in v1, and the state must reflect the user's claim
// rather than a verified fact.
func TestAppliedStateSticksAndCarriesItsTime(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	if err := svc.Apply(ctx, userID, oppID, nil); err != nil {
		t.Fatal(err)
	}
	state, err := svc.StateFor(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	st := state[oppID.String()]
	if !st.Applied {
		t.Fatal("applied state not recorded")
	}
	if st.AppliedAt == nil {
		t.Error("applied state carries no time; a user needs to know when they told us")
	}
}

// A dismissal without a reason teaches nothing, and learning from negatives is
// the whole point of this log.
func TestDismissalRequiresAKnownReason(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	if err := svc.Dismiss(ctx, userID, oppID, "", nil); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("empty reason gave %v, want ErrReasonRequired", err)
	}
	if err := svc.Dismiss(ctx, userID, oppID, "because i said so", nil); !errors.Is(err, ErrUnknownReason) {
		t.Errorf("free-text reason gave %v, want ErrUnknownReason", err)
	}
	if err := svc.Dismiss(ctx, userID, oppID, ReasonWrongLevel, nil); err != nil {
		t.Fatalf("a valid reason was rejected: %v", err)
	}
	if n := countEvents(t, pool, userID, EventDismissed); n != 1 {
		t.Errorf("%d dismissals recorded, want exactly the valid one", n)
	}
}

// The decision record is the point of blueprint §32: it must be possible to say
// why a posting was ranked where it was, for this user, on that date.
func TestDecisionRecordIsStoredWithEveryVersion(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	d := &Decision{
		FitScore: 72, MaxPossible: 90,
		Factors: []matching.FactorScore{{
			Factor: matching.FactorDomain, Available: true, Value: 1,
			Contribution: 10, MaxContribution: 10,
		}},
		WeightsVersion: matching.WeightsVersion, EmbeddingVersion: "v1",
		ProfileVersion: 3, OpportunityVersion: 5,
	}
	if err := svc.Save(ctx, userID, oppID, d); err != nil {
		t.Fatal(err)
	}

	var (
		score, maxPossible                 *int16
		weights, embedding                 *string
		profileVersion, opportunityVersion *int32
		breakdown                          []byte
	)
	if err := pool.QueryRow(ctx, `
		SELECT fit_score_at_event, max_possible_at_event, weights_version,
		       embedding_version, profile_version, opportunity_version, factor_breakdown
		  FROM engagement_event WHERE user_id=$1 AND event_type='saved'`, userID).
		Scan(&score, &maxPossible, &weights, &embedding,
			&profileVersion, &opportunityVersion, &breakdown); err != nil {
		t.Fatal(err)
	}

	if score == nil || *score != 72 || maxPossible == nil || *maxPossible != 90 {
		t.Errorf("score recorded as %v of %v, want 72 of 90", score, maxPossible)
	}
	for name, got := range map[string]*string{"weights": weights, "embedding": embedding} {
		if got == nil || *got == "" {
			t.Errorf("%s version missing; the row records that something was ranked, not why", name)
		}
	}
	if profileVersion == nil || *profileVersion != 3 {
		t.Errorf("profile version = %v, want 3", profileVersion)
	}
	if opportunityVersion == nil || *opportunityVersion != 5 {
		t.Errorf("opportunity version = %v, want 5", opportunityVersion)
	}
	if len(breakdown) == 0 {
		t.Error("no factor breakdown stored; a score without its working is not an explanation")
	}
}

// An action from a direct link has no ranking behind it. Recording zeros would
// fabricate one.
func TestActionWithNoRankingRecordsNoDecision(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	if err := svc.Save(ctx, userID, oppID, nil); err != nil {
		t.Fatal(err)
	}
	var score *int16
	if err := pool.QueryRow(ctx,
		`SELECT fit_score_at_event FROM engagement_event WHERE user_id=$1`, userID).
		Scan(&score); err != nil {
		t.Fatal(err)
	}
	if score != nil {
		t.Errorf("recorded a fit score of %d for an action with no ranking behind it", *score)
	}
}

// Saturation counts distinct DAYS, not impressions: a user who refreshes twenty
// times has not ignored a role twenty times.
func TestSaturationCountsDaysNotImpressions(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	// Five impressions today.
	for range 5 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO engagement_event (user_id, opportunity_id, event_type)
			VALUES ($1,$2,'shown')`, userID, oppID); err != nil {
			t.Fatal(err)
		}
	}
	sat, err := svc.SaturationFor(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sat[oppID.String()]; got != 1 {
		t.Errorf("five impressions in one day counted as %d, want 1", got)
	}

	// One more on a different day.
	if _, err := pool.Exec(ctx, `
		INSERT INTO engagement_event (user_id, opportunity_id, event_type, occurred_at)
		VALUES ($1,$2,'shown', now() - interval '2 days')`, userID, oppID); err != nil {
		t.Fatal(err)
	}
	sat, err = svc.SaturationFor(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sat[oppID.String()]; got != 2 {
		t.Errorf("impressions across two days counted as %d, want 2", got)
	}
}

// RecordShown is on the path a user waits on, so it batches. It must also never
// fail the feed.
func TestRecordShownWritesEveryImpression(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	matches := []matching.Match{{
		Opportunity: store.Opportunity{ID: oppID, Version: 4},
		Fit:         matching.Fit{Score: 60, MaxPossible: 90},
	}}
	svc.RecordShown(ctx, userID, matches, 2)

	if n := countEvents(t, pool, userID, EventShown); n != 1 {
		t.Errorf("%d shown events, want 1", n)
	}
	// An empty feed must not write anything, and must not error.
	svc.RecordShown(ctx, userID, nil, 2)
	if n := countEvents(t, pool, userID, EventShown); n != 1 {
		t.Errorf("recording an empty feed wrote %d extra events", n)
	}
}

// A save against a posting that does not exist is a 404, not a 500.
func TestActionOnUnknownPostingIsNotFound(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, _ := seedUserAndPosting(t, pool)

	var missing pgtype.UUID
	missing.Bytes = [16]byte{9, 9, 9}
	missing.Valid = true

	if err := svc.Save(ctx, userID, missing, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("saving an unknown posting gave %v, want ErrNotFound", err)
	}
}

// The behavioural label export is what replaces the rubric labels in
// internal/eval, and it must key on the ATS identity rather than a local UUID.
func TestLabelExportUsesTheStableATSIdentity(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quietLog())
	userID, oppID := seedUserAndPosting(t, pool)

	// A label needs an opportunity_source row to carry the ATS identity.
	var sourceID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO source (name, tier, type, legal_basis, poll_interval)
		VALUES ($1,'a','test','test', interval '1 hour') RETURNING id`,
		"eng-src-"+uuid.NewString()[:8]).Scan(&sourceID); err != nil {
		t.Fatalf("source: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM source WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO opportunity_source (opportunity_id, source_id, ats_type, ats_job_id, source_job_id, apply_url)
		VALUES ($1,$2,'greenhouse','12345','12345','https://example.invalid/12345')`,
		oppID, sourceID); err != nil {
		t.Fatalf("opportunity_source: %v", err)
	}

	if err := svc.Dismiss(ctx, userID, oppID, ReasonWrongLevel, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := store.New(pool).ListEngagementLabels(ctx, store.ListEngagementLabelsParams{
		Since: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}, PageSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		// Pointers because opportunity_source allows these to be null for non-ATS
		// sources; a label without an ATS identity cannot travel, so skip it.
		if r.AtsType == nil || r.AtsJobID == nil {
			continue
		}
		if *r.AtsType == "greenhouse" && *r.AtsJobID == "12345" {
			found = true
			if r.EventType != EventDismissed {
				t.Errorf("event type = %q", r.EventType)
			}
			if r.DismissReason == nil || *r.DismissReason != ReasonWrongLevel {
				t.Errorf("dismiss reason = %v, want %q", r.DismissReason, ReasonWrongLevel)
			}
		}
	}
	if !found {
		t.Error("the label export did not return the dismissal keyed by its ATS identity")
	}
}
