//go:build integration

package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/source"
	"github.com/Xubair001/devsignal/internal/sourcehealth"
	"github.com/Xubair001/devsignal/internal/store"
)

func newRunner(t *testing.T, pool *pgxpool.Pool) *Runner {
	t.Helper()
	return NewRunner(pool, source.NewClient(source.DefaultClientConfig()), quiet())
}

// backdateHealth writes a baseline day directly. Real baselines accumulate over
// days, which a test cannot wait for.
func seedBaselineDay(t *testing.T, pool *pgxpool.Pool, srcID pgtype.UUID,
	daysAgo, seen, usable, withLocation int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO source_health_daily (source_id, day, polls, postings_seen,
		  postings_usable, with_company, with_location, with_apply_url, with_language)
		VALUES ($1, CURRENT_DATE - $2::int, 1, $3, $4, $3, $5, $3, $3)
		ON CONFLICT (source_id, day) DO UPDATE SET
		  postings_seen = EXCLUDED.postings_seen,
		  postings_usable = EXCLUDED.postings_usable,
		  with_location = EXCLUDED.with_location`,
		srcID, daysAgo, seen, usable, withLocation)
	if err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
}

func TestHealthRecordingAccumulatesWithinTheDay(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	srcID := seedSource(t, pool)
	r := newRunner(t, pool)

	r.recordHealth(ctx, srcID, Result{Fetched: 100, Created: 90, Skipped: 10,
		WithCompany: 100, WithLocation: 95, WithApplyURL: 100, WithLanguage: 100}, false)
	r.recordHealth(ctx, srcID, Result{Fetched: 50, Created: 50,
		WithCompany: 50, WithLocation: 50, WithApplyURL: 50, WithLanguage: 50}, false)

	today, err := store.New(pool).TodaySourceHealth(ctx, srcID)
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	if today.PostingsSeen != 150 {
		t.Errorf("postings_seen = %d, want 150 (two polls should accumulate)", today.PostingsSeen)
	}
	if today.PostingsUsable != 140 {
		t.Errorf("postings_usable = %d, want 140", today.PostingsUsable)
	}
	if today.WithLocation != 145 {
		t.Errorf("with_location = %d, want 145", today.WithLocation)
	}
}

// A 304 observed no postings, so it must not be judged as a quality signal —
// otherwise an unchanged board would look like a parser that stopped working.
func TestNotModifiedPollDoesNotAffectQuality(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	srcID := seedSource(t, pool)
	r := newRunner(t, pool)

	r.recordHealth(ctx, srcID, Result{NotModified: true}, false)

	today, err := store.New(pool).TodaySourceHealth(ctx, srcID)
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	if today.PostingsSeen != 0 {
		t.Errorf("a 304 recorded %d postings", today.PostingsSeen)
	}
	src, _ := store.New(pool).GetSourceByID(ctx, srcID)
	if src.ConsecutiveDegraded != 0 {
		t.Errorf("a 304 marked the source degraded (%d)", src.ConsecutiveDegraded)
	}
}

// A failed poll must never be read as parser rot: an outage is not a quality
// regression, and conflating them would quarantine sources during incidents.
func TestFailedPollDoesNotDegradeQuality(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	srcID := seedSource(t, pool)
	r := newRunner(t, pool)

	for i := 0; i < sourcehealth.ConsecutiveToQuarantine+2; i++ {
		r.recordHealth(ctx, srcID, Result{}, true)
	}

	src, err := store.New(pool).GetSourceByID(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.ConsecutiveDegraded != 0 {
		t.Errorf("failed polls marked the source degraded (%d)", src.ConsecutiveDegraded)
	}
	if src.Status != "active" {
		t.Errorf("status = %q: an outage must not quarantine a source", src.Status)
	}
}

// The end-to-end parser-rot scenario: a healthy baseline, then a field quietly
// stops being populated, sustained until quarantine.
func TestSustainedFieldRotQuarantinesButNotImmediately(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	srcID := seedSource(t, pool)
	r := newRunner(t, pool)
	q := store.New(pool)

	// A week of healthy history: location populated on ~98% of postings.
	for d := 1; d <= 7; d++ {
		seedBaselineDay(t, pool, srcID, d, 200, 200, 196)
	}

	// Today: rows still arrive, nothing errors, but location collapses to ~30%.
	rotted := Result{Fetched: 200, Created: 200,
		WithCompany: 200, WithLocation: 60, WithApplyURL: 200, WithLanguage: 200}

	r.recordHealth(ctx, srcID, rotted, false)
	src, err := q.GetSourceByID(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.ConsecutiveDegraded != 1 {
		t.Fatalf("consecutive_degraded = %d after one bad evaluation, want 1", src.ConsecutiveDegraded)
	}
	if src.Status != "active" {
		t.Fatalf("quarantined after ONE degraded evaluation; a transient blip must not "+
			"take a source offline (status=%q)", src.Status)
	}
	if src.LastHealthNote == nil || *src.LastHealthNote == "" {
		t.Error("degradation recorded without saying why")
	}

	// Sustained: keep observing the same collapse.
	for i := 1; i < sourcehealth.ConsecutiveToQuarantine; i++ {
		r.recordHealth(ctx, srcID, rotted, false)
	}

	src, err = q.GetSourceByID(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.Status != "quarantined" {
		t.Fatalf("status = %q after %d degraded evaluations, want quarantined",
			src.Status, sourcehealth.ConsecutiveToQuarantine)
	}
	// Quarantined sources must stop being claimed, or the runner keeps hammering
	// a source we know is broken.
	due, err := q.ClaimDueSources(ctx, store.ClaimDueSourcesParams{
		Lease: pgtype.Interval{Microseconds: int64(time.Minute / time.Microsecond), Valid: true},
		Batch: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.SourceID == srcID {
			t.Error("a quarantined source was still claimed for polling")
		}
	}
}

// Recovery must clear the counter, or a source that had one bad day stays one
// blip away from quarantine forever.
func TestRecoveryClearsDegradedCounter(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	srcID := seedSource(t, pool)
	r := newRunner(t, pool)
	q := store.New(pool)

	for d := 1; d <= 7; d++ {
		seedBaselineDay(t, pool, srcID, d, 200, 200, 196)
	}
	r.recordHealth(ctx, srcID, Result{Fetched: 200, Created: 200,
		WithCompany: 200, WithLocation: 60, WithApplyURL: 200, WithLanguage: 200}, false)

	if src, _ := q.GetSourceByID(ctx, srcID); src.ConsecutiveDegraded == 0 {
		t.Fatal("fixture did not degrade")
	}

	// The parser is fixed: today's totals now look like the baseline again.
	// (Today accumulates, so add enough healthy volume to restore the rate.)
	for i := 0; i < 10; i++ {
		r.recordHealth(ctx, srcID, Result{Fetched: 200, Created: 200,
			WithCompany: 200, WithLocation: 200, WithApplyURL: 200, WithLanguage: 200}, false)
	}

	src, err := q.GetSourceByID(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.ConsecutiveDegraded != 0 {
		t.Errorf("consecutive_degraded = %d after recovery, want 0", src.ConsecutiveDegraded)
	}
}

// A brand-new source has no baseline. It must be reported as unknown, never
// judged — and never quarantined for lack of history.
func TestNewSourceWithNoBaselineIsNotJudged(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	srcID := seedSource(t, pool)
	r := newRunner(t, pool)

	for i := 0; i < sourcehealth.ConsecutiveToQuarantine+2; i++ {
		r.recordHealth(ctx, srcID, Result{Fetched: 200, Created: 100, Skipped: 100,
			WithCompany: 100, WithLocation: 0, WithApplyURL: 100, WithLanguage: 100}, false)
	}

	src, err := store.New(pool).GetSourceByID(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	if src.Status != "active" {
		t.Errorf("status = %q: a source with no baseline must not be quarantined", src.Status)
	}
	if src.ConsecutiveDegraded != 0 {
		t.Errorf("consecutive_degraded = %d with no baseline to compare against",
			src.ConsecutiveDegraded)
	}
}

var _ = uuid.NewString
