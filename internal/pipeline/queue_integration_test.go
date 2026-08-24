//go:build integration

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/store"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.Pool(t)
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// seed creates an isolated company plus n opportunities, and cleans up after.
func seed(t *testing.T, pool *pgxpool.Pool, n int, state State) (pgtype.UUID, []pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	q := store.New(pool)

	var companyID pgtype.UUID
	domain := fmt.Sprintf("test-%s.example", uuid.NewString()[:8])
	err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,$2) RETURNING id`,
		domain, "Test Co").Scan(&companyID)
	if err != nil {
		t.Fatalf("seed company: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = q.DeleteOpportunitiesForCompany(c, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})

	ids := make([]pgtype.UUID, 0, n)
	for i := 0; i < n; i++ {
		o, err := q.CreateOpportunity(ctx, store.CreateOpportunityParams{
			CompanyID:       companyID,
			TitleRaw:        fmt.Sprintf("Engineer %d", i),
			TitleNormalized: fmt.Sprintf("engineer %d", i),
			PipelineState:   string(state),
			ContentHash:     []byte(fmt.Sprintf("hash-%d", i)),
		})
		if err != nil {
			t.Fatalf("seed opportunity: %v", err)
		}
		ids = append(ids, o.ID)
	}
	return companyID, ids
}

func cfg() Config {
	c := DefaultConfig()
	c.BatchSize = 50
	c.Lease = 2 * time.Second
	c.BaseBackoff = 10 * time.Millisecond
	c.MaxBackoff = 50 * time.Millisecond
	c.MaxAttempts = 3
	return c
}

// The core guarantee: N concurrent workers must never see the same row twice.
func TestSkipLockedGivesEachRowToExactlyOneWorker(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	const total = 40
	_, ids := seed(t, pool, total, StateNormalized)
	want := make(map[string]bool, total)
	for _, id := range ids {
		want[id.String()] = true
	}

	c := cfg()
	c.BatchSize = 7 // deliberately not a divisor of total
	q := NewQueue(pool, c, quietLog())

	var mu sync.Mutex
	seen := map[string]int{}

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				items, err := q.Claim(ctx, StateNormalized)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if len(items) == 0 {
					return
				}
				mu.Lock()
				for _, it := range items {
					if want[it.ID.String()] {
						seen[it.ID.String()]++
					}
				}
				mu.Unlock()
				for _, it := range items {
					if err := q.Advance(ctx, it); err != nil {
						t.Errorf("advance: %v", err)
					}
				}
			}
		}()
	}
	wg.Wait()

	if len(seen) != total {
		t.Fatalf("claimed %d distinct rows, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("row %s claimed %d times: SKIP LOCKED is not isolating", id, n)
		}
	}
}

func TestAdvanceIsGuardedByVersion(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seed(t, pool, 1, StateNormalized)
	q := NewQueue(pool, cfg(), quietLog())

	items, err := q.Claim(ctx, StateNormalized)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim: %v (n=%d)", err, len(items))
	}
	it := items[0]

	if err := q.Advance(ctx, it); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	// Replaying the same stale version must not clobber the newer write.
	if err := q.Advance(ctx, it); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale advance: got %v, want ErrVersionConflict", err)
	}

	got, err := store.New(pool).GetOpportunityState(ctx, ids[0])
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.PipelineState != string(StateDeduped) {
		t.Fatalf("state = %q, want deduped (advanced exactly once)", got.PipelineState)
	}
}

// A hard-killed worker leaves a lease behind; the row must become claimable
// again on its own, with no cleanup step.
func TestExpiredLeaseBecomesClaimableAgain(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	seed(t, pool, 1, StateNormalized)

	c := cfg()
	c.Lease = 900 * time.Millisecond
	q := NewQueue(pool, c, quietLog())

	first, err := q.Claim(ctx, StateNormalized)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %v (n=%d)", err, len(first))
	}
	// Still leased: nobody else may take it.
	again, err := q.Claim(ctx, StateNormalized)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatal("a leased row was claimed by a second worker")
	}

	time.Sleep(1200 * time.Millisecond)
	third, err := q.Claim(ctx, StateNormalized)
	if err != nil {
		t.Fatalf("third claim: %v", err)
	}
	if len(third) != 1 {
		t.Fatal("row did not become claimable after its lease expired")
	}
	if third[0].Attempts <= first[0].Attempts {
		t.Error("attempts did not increase across re-claim")
	}
}

func TestFailBacksOffThenParks(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seed(t, pool, 1, StateNormalized)
	c := cfg()
	c.MaxAttempts = 2
	q := NewQueue(pool, c, quietLog())
	st := store.New(pool)

	for i := 0; i < 3; i++ {
		items, err := q.Claim(ctx, StateNormalized)
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if len(items) == 0 {
			break // parked, so no longer claimable in this state
		}
		if err := q.Fail(ctx, items[0], errors.New("synthetic failure")); err != nil {
			t.Fatalf("fail %d: %v", i, err)
		}
		time.Sleep(60 * time.Millisecond)
	}

	got, err := st.GetOpportunityState(ctx, ids[0])
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.PipelineState != string(StateFailedPermanent) {
		t.Fatalf("state = %q, want failed_permanent after exhausting attempts", got.PipelineState)
	}
	if got.LastError == nil || *got.LastError == "" {
		t.Fatal("last_error not recorded: a parked record must say why")
	}
}

func TestSweeperRequeuesStranded(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seed(t, pool, 1, StateNormalized)

	// Strand it: unreachable next_attempt_at, no lease. state_entered_at cannot
	// be backdated from here (a trigger owns it), so the test uses a tiny
	// threshold instead — which is also closer to how the sweeper really runs.
	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET next_attempt_at = now() + interval '1 day',
		 lease_until = NULL WHERE id=$1`, ids[0]); err != nil {
		t.Fatalf("strand: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	c := cfg()
	c.SweepAfter = 10 * time.Millisecond
	q := NewQueue(pool, c, quietLog())

	// Sweep in a loop and assert on THIS row, not on the batch. SweepStranded
	// takes the oldest stranded rows up to the batch size, so with other data in
	// the database a single sweep may never reach the row this test seeded — the
	// earlier version of this test silently depended on an empty database.
	var due bool
	for i := 0; i < 20 && !due; i++ {
		if _, err := q.Sweep(ctx); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT next_attempt_at <= now() AND lease_until IS NULL
			   FROM opportunity WHERE id=$1`, ids[0]).Scan(&due); err != nil {
			t.Fatalf("read back: %v", err)
		}
	}
	if !due {
		t.Fatal("sweeper never requeued the stranded record")
	}
}

// Worker-level: a handler that always fails on a DEGRADABLE stage must still let
// the record reach the next state rather than blocking visibility forever.
func TestWorkerDegradesPastFailedOptionalStage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := testPool(t)
	_, ids := seed(t, pool, 1, StateEnriched) // enrichment is degradable

	c := cfg()
	c.MaxAttempts = 2
	q := NewQueue(pool, c, quietLog())

	var calls atomic.Int32
	w := NewWorker(q, Stage{
		State:        StateEnriched,
		Concurrency:  1,
		PollInterval: 50 * time.Millisecond,
		Handle: func(context.Context, Item) error {
			calls.Add(1)
			return errors.New("model unavailable")
		},
	}, quietLog())

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = w.Run(runCtx); close(done) }()

	st := store.New(pool)
	deadline := time.Now().Add(15 * time.Second)
	var final string
	for time.Now().Before(deadline) {
		got, err := st.GetOpportunityState(ctx, ids[0])
		if err == nil {
			final = got.PipelineState
			if final == string(StateEmbedded) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	<-done

	if final != string(StateEmbedded) {
		t.Fatalf("state = %q, want embedded: a failing optional stage must not block", final)
	}
	if calls.Load() < 2 {
		t.Errorf("handler called %d times, expected retries before degrading", calls.Load())
	}
}

// A panicking handler must not kill the worker or the rest of the batch.
func TestWorkerSurvivesPanickingHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := testPool(t)
	_, ids := seed(t, pool, 4, StateNormalized)

	q := NewQueue(pool, cfg(), quietLog())
	var handled atomic.Int32
	w := NewWorker(q, Stage{
		State:        StateNormalized,
		Concurrency:  2,
		PollInterval: 50 * time.Millisecond,
		Handle: func(_ context.Context, it Item) error {
			n := handled.Add(1)
			if n == 1 {
				panic("synthetic panic on the first record")
			}
			return nil
		},
	}, quietLog())

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = w.Run(runCtx); close(done) }()

	st := store.New(pool)
	deadline := time.Now().Add(15 * time.Second)
	advanced := 0
	for time.Now().Before(deadline) {
		advanced = 0
		for _, id := range ids {
			if got, err := st.GetOpportunityState(ctx, id); err == nil &&
				got.PipelineState == string(StateDeduped) {
				advanced++
			}
		}
		if advanced >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	<-done

	// The three non-panicking records must have gone through.
	if advanced < 3 {
		t.Fatalf("%d/4 records advanced; a panic took out its batch siblings", advanced)
	}
}

// Regression: an unrelated write must not reset the stranding clock. Keying the
// sweeper off updated_at meant a liveness re-poll hid a record that had been
// stuck in one state for a week.
func TestUnrelatedWriteDoesNotResetStrandingClock(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seed(t, pool, 1, StateNormalized)

	time.Sleep(60 * time.Millisecond)

	// A liveness re-poll: touches last_seen_at, not pipeline_state.
	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET last_seen_at = now(),
		 next_attempt_at = now() + interval '1 day' WHERE id=$1`, ids[0]); err != nil {
		t.Fatalf("liveness update: %v", err)
	}

	var updatedAge, stateAge time.Duration
	if err := pool.QueryRow(ctx,
		`SELECT now()-updated_at, now()-state_entered_at FROM opportunity WHERE id=$1`,
		ids[0]).Scan(&updatedAge, &stateAge); err != nil {
		t.Fatalf("read ages: %v", err)
	}
	if stateAge <= updatedAge {
		t.Fatalf("state_entered_at (%v) should be older than updated_at (%v): "+
			"the unrelated write reset the state clock", stateAge, updatedAge)
	}

	c := cfg()
	c.SweepAfter = 50 * time.Millisecond
	q := NewQueue(pool, c, quietLog())
	n, err := q.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n < 1 {
		t.Fatal("record hidden from the sweeper by an unrelated write")
	}
}

// Regression: the sweeper must make progress through a backlog larger than one
// batch. It ordered by state_entered_at, which requeuing does not change, so
// every sweep returned the same oldest batch and the tail was starved forever.
// A starving safety net is worse than none, because it looks like it is working.
func TestSweeperMakesProgressThroughABacklog(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	const total = 12
	_, ids := seed(t, pool, total, StateNormalized)

	// Strand every one of them, all unreachable by normal claiming.
	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET next_attempt_at = now() + interval '1 day',
		 lease_until = NULL, swept_at = NULL WHERE id = ANY($1)`, ids); err != nil {
		t.Fatalf("strand: %v", err)
	}

	c := cfg()
	c.SweepAfter = 10 * time.Millisecond
	c.BatchSize = 3 // deliberately far smaller than the backlog
	q := NewQueue(pool, c, quietLog())
	time.Sleep(50 * time.Millisecond)

	// Enough sweeps to cover the backlog several times over if progress is made,
	// but never enough if the same head is returned repeatedly.
	for i := 0; i < 12; i++ {
		if _, err := q.Sweep(ctx); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}

	var requeued int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity
		  WHERE id = ANY($1) AND next_attempt_at <= now()`, ids).Scan(&requeued); err != nil {
		t.Fatalf("count: %v", err)
	}
	if requeued != total {
		t.Fatalf("%d of %d stranded records requeued: the sweeper is not advancing "+
			"through the backlog", requeued, total)
	}
}
