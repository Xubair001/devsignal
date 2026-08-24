//go:build integration

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/source"
	"github.com/Xubair001/devsignal/internal/store"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.Pool(t)
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeAdapter serves a scripted sequence of boards, so the liveness lifecycle can
// be driven deterministically: appear, change, disappear, reappear.
type fakeAdapter struct {
	id    string
	feed  [][]job
	round int
}

type job struct {
	ID    string
	Title string
	Desc  string
}

func (f *fakeAdapter) ID() string        { return f.id }
func (f *fakeAdapter) Tier() source.Tier { return source.TierA }

func (f *fakeAdapter) Fetch(context.Context, source.Cursor) ([]source.RawDocument, source.Cursor, error) {
	if f.round >= len(f.feed) {
		return nil, source.Cursor{}, fmt.Errorf("no more scripted rounds")
	}
	body, _ := json.Marshal(f.feed[f.round])
	f.round++
	return []source.RawDocument{{SourceJobID: "board", Body: body}}, source.Cursor{ETag: "e"}, nil
}

func (f *fakeAdapter) Parse(doc source.RawDocument) ([]source.ParsedPosting, error) {
	var jobs []job
	if err := json.Unmarshal(doc.Body, &jobs); err != nil {
		return nil, err
	}
	out := make([]source.ParsedPosting, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, source.ParsedPosting{
			SourceJobID: j.ID, ATSType: "faketype", ATSJobID: j.ID,
			Title: j.Title, CompanyName: "Fake Co",
			DescriptionHTML: j.Desc,
			ContentHash:     []byte(j.Title + "|" + j.Desc),
		})
	}
	return out, nil
}

func seedSource(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	q := store.New(pool)
	name := "test-src-" + uuid.NewString()[:8]
	src, err := q.UpsertSource(ctx, store.UpsertSourceParams{
		Name: name, Tier: "a", Type: "ats_api",
		LegalBasis:   "public documented board API",
		PollInterval: pgtype.Interval{Microseconds: int64(5 * time.Minute / time.Microsecond), Valid: true},
	})
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE id IN
		  (SELECT opportunity_id FROM opportunity_source WHERE source_id=$1)`, src.ID)
		_, _ = pool.Exec(c, `DELETE FROM source_schedule WHERE source_id=$1`, src.ID)
		_, _ = pool.Exec(c, `DELETE FROM source WHERE id=$1`, src.ID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE canonical_domain LIKE '%.ats.invalid'
		  AND NOT EXISTS (SELECT 1 FROM opportunity o WHERE o.company_id = company.id)`)
	})
	return src.ID
}

func TestIngestCreatesUpdatesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quiet())
	srcID := seedSource(t, pool)

	board := []job{
		{ID: "1", Title: "Backend Engineer", Desc: "go and postgres"},
		{ID: "2", Title: "Frontend Engineer", Desc: "react"},
	}
	a := &fakeAdapter{id: "faketype:acme", feed: [][]job{board, board, {
		{ID: "1", Title: "Senior Backend Engineer", Desc: "go and postgres"}, // changed
		{ID: "2", Title: "Frontend Engineer", Desc: "react"},                 // same
	}}}

	// Round 1: both are new.
	r1, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if r1.Created != 2 || r1.Updated != 0 {
		t.Fatalf("round 1: created=%d updated=%d, want 2/0", r1.Created, r1.Updated)
	}

	// Round 2: identical content. Re-ingesting must not duplicate or churn —
	// this is the idempotency that stops the extraction bill repeating.
	r2, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if r2.Created != 0 || r2.Updated != 0 || r2.Unchanged != 2 {
		t.Fatalf("round 2: created=%d updated=%d unchanged=%d, want 0/0/2",
			r2.Created, r2.Updated, r2.Unchanged)
	}

	// Round 3: one title changed -> exactly one update.
	r3, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{})
	if err != nil {
		t.Fatalf("round 3: %v", err)
	}
	if r3.Updated != 1 || r3.Unchanged != 1 {
		t.Fatalf("round 3: updated=%d unchanged=%d, want 1/1", r3.Updated, r3.Unchanged)
	}

	q := store.New(pool)
	live, err := q.CountLiveOpportunitiesForSource(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	if live != 2 {
		t.Fatalf("live opportunities = %d, want 2 (no duplicates)", live)
	}
}

// A changed posting must re-enter the pipeline and bump its version, because the
// version is what invalidates a cached fit score.
func TestChangedPostingReentersPipelineAndBumpsVersion(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quiet())
	srcID := seedSource(t, pool)

	a := &fakeAdapter{id: "faketype:v", feed: [][]job{
		{{ID: "9", Title: "Engineer", Desc: "v1"}},
		{{ID: "9", Title: "Engineer", Desc: "v2"}},
	}}
	if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err != nil {
		t.Fatal(err)
	}

	var id pgtype.UUID
	var stateBefore string
	var verBefore int32
	if err := pool.QueryRow(ctx,
		`SELECT o.id, o.pipeline_state, o.version FROM opportunity o
		   JOIN opportunity_source s ON s.opportunity_id=o.id
		  WHERE s.source_id=$1`, srcID).Scan(&id, &stateBefore, &verBefore); err != nil {
		t.Fatal(err)
	}
	// Pretend the pipeline finished with it.
	if _, err := pool.Exec(ctx, `UPDATE opportunity SET pipeline_state='ready' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err != nil {
		t.Fatal(err)
	}

	var stateAfter string
	var verAfter int32
	if err := pool.QueryRow(ctx,
		`SELECT pipeline_state, version FROM opportunity WHERE id=$1`, id).Scan(&stateAfter, &verAfter); err != nil {
		t.Fatal(err)
	}
	if stateAfter != "parsed" {
		t.Errorf("state = %q, want parsed: changed content must be reprocessed", stateAfter)
	}
	if verAfter <= verBefore {
		t.Errorf("version %d -> %d: must bump so cached scores invalidate", verBefore, verAfter)
	}
}

// The liveness lifecycle: a posting that stops appearing closes after
// MaxConsecutiveMisses successful polls, and reopens if it comes back.
func TestAbsenceClosesThenReappearanceReopens(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quiet())
	srcID := seedSource(t, pool)

	full := []job{{ID: "a", Title: "Stays", Desc: "d"}, {ID: "b", Title: "Vanishes", Desc: "d"}}
	partial := []job{{ID: "a", Title: "Stays", Desc: "d"}}

	feed := [][]job{full}
	for i := 0; i < MaxConsecutiveMisses; i++ {
		feed = append(feed, partial)
	}
	feed = append(feed, full) // it comes back
	a := &fakeAdapter{id: "faketype:live", feed: feed}

	if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err != nil {
		t.Fatal(err)
	}

	closedOf := func(jobID string) (bool, int32) {
		t.Helper()
		var closed *time.Time
		var misses int32
		if err := pool.QueryRow(ctx,
			`SELECT o.closed_at, o.consecutive_misses FROM opportunity o
			   JOIN opportunity_source s ON s.opportunity_id=o.id
			  WHERE s.source_id=$1 AND s.source_job_id=$2`, srcID, jobID).Scan(&closed, &misses); err != nil {
			t.Fatal(err)
		}
		return closed != nil, misses
	}

	if c, _ := closedOf("b"); c {
		t.Fatal("posting closed on its first sighting")
	}

	for i := 0; i < MaxConsecutiveMisses; i++ {
		if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}

	closed, misses := closedOf("b")
	if !closed {
		t.Fatalf("posting absent from %d successful polls is still open (misses=%d)",
			MaxConsecutiveMisses, misses)
	}
	if c, _ := closedOf("a"); c {
		t.Fatal("a posting that kept appearing was closed")
	}

	// It comes back: seeing it is stronger evidence than having missed it.
	if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	if c, m := closedOf("b"); c {
		t.Fatalf("reappeared posting still closed (misses=%d)", m)
	}
}

// A not-modified response is a SUCCESSFUL poll. It must not count as a miss —
// otherwise an unchanged board would close its own postings.
func TestNotModifiedDoesNotCountAsMiss(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quiet())
	srcID := seedSource(t, pool)

	a := &fakeAdapter{id: "faketype:nm", feed: [][]job{{{ID: "z", Title: "Job", Desc: "d"}}}}
	if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err != nil {
		t.Fatal(err)
	}

	nm := &notModifiedAdapter{}
	for i := 0; i < MaxConsecutiveMisses+2; i++ {
		res, _, err := svc.RunSource(ctx, srcID, nm, source.Cursor{ETag: "e"})
		if err != nil {
			t.Fatalf("not-modified poll %d: %v", i, err)
		}
		if !res.NotModified {
			t.Fatal("expected NotModified result")
		}
	}

	var closed *time.Time
	var misses int32
	if err := pool.QueryRow(ctx,
		`SELECT o.closed_at, o.consecutive_misses FROM opportunity o
		   JOIN opportunity_source s ON s.opportunity_id=o.id WHERE s.source_id=$1`,
		srcID).Scan(&closed, &misses); err != nil {
		t.Fatal(err)
	}
	if closed != nil {
		t.Fatal("304 responses closed a live posting")
	}
	if misses != 0 {
		t.Fatalf("misses = %d, want 0: a 304 is not an absence", misses)
	}
}

type notModifiedAdapter struct{}

func (notModifiedAdapter) ID() string        { return "faketype:nm" }
func (notModifiedAdapter) Tier() source.Tier { return source.TierA }
func (notModifiedAdapter) Fetch(context.Context, source.Cursor) ([]source.RawDocument, source.Cursor, error) {
	return nil, source.Cursor{}, source.ErrNotModified
}
func (notModifiedAdapter) Parse(source.RawDocument) ([]source.ParsedPosting, error) {
	return nil, fmt.Errorf("must not be called on 304")
}

// A fetch failure must never close anything: inferring absence from an outage
// would let one bad afternoon delete the corpus.
func TestFetchFailureDoesNotCloseAnything(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := New(pool, quiet())
	srcID := seedSource(t, pool)

	a := &fakeAdapter{id: "faketype:fail", feed: [][]job{{{ID: "k", Title: "Job", Desc: "d"}}}}
	if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err != nil {
		t.Fatal(err)
	}

	// The scripted feed is exhausted, so Fetch now errors.
	for i := 0; i < MaxConsecutiveMisses+2; i++ {
		if _, _, err := svc.RunSource(ctx, srcID, a, source.Cursor{}); err == nil {
			t.Fatal("expected a fetch error")
		}
	}

	var closed *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT o.closed_at FROM opportunity o
		   JOIN opportunity_source s ON s.opportunity_id=o.id WHERE s.source_id=$1`,
		srcID).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if closed != nil {
		t.Fatal("a failed poll closed a live posting")
	}
}

func TestParseYieldReportsSkippedRows(t *testing.T) {
	r := Result{Created: 8, Updated: 1, Unchanged: 0, Skipped: 1}
	if got := r.ParseYield(); got != 0.9 {
		t.Fatalf("ParseYield = %v, want 0.9", got)
	}
	if got := (Result{}).ParseYield(); got != 1 {
		t.Fatalf("empty ParseYield = %v, want 1", got)
	}
}
