package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
)

// ErrVersionConflict means another stage wrote first. The caller reloads and
// retries; it must never force the write.
var ErrVersionConflict = errors.New("pipeline: version conflict")

// ErrHandled means the handler completed the work AND settled the row's state
// itself, so the worker must not advance it again.
//
// A handler that writes to the row it is advancing has to do both in one
// statement: any write bumps version, which then invalidates the worker's
// separate version-guarded Advance. Getting this wrong livelocks the stage —
// every item succeeds, every advance conflicts, nothing ever progresses.
var ErrHandled = errors.New("pipeline: handled by stage")

// ErrRetryLater means the failure is SYSTEMIC, not this record's fault — a
// missing credential, a provider outage, a misconfiguration.
//
// Spending the attempt budget on it is wrong twice over: the budget exists to
// give up on records that are individually bad, and a systemic fault fails
// identically for every record, so N attempts just multiply the noise. The item
// is deferred without consuming an attempt, so the moment the cause is fixed the
// backlog drains on its own.
var ErrRetryLater = errors.New("pipeline: retry later, systemic failure")

type Config struct {
	BatchSize     int32
	Lease         time.Duration
	MaxAttempts   int32
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration
	SweepAfter    time.Duration
	SweepInterval time.Duration
	// SystemicBackoff is how long to wait after a systemic failure. Longer than a
	// normal retry: hammering a provider that is down or misconfigured helps
	// nobody.
	SystemicBackoff time.Duration
}

func DefaultConfig() Config {
	return Config{
		BatchSize: 100,
		// Lease must exceed the slowest realistic unit of work, or a healthy
		// worker's row gets stolen mid-flight and processed twice.
		Lease:       5 * time.Minute,
		MaxAttempts: 5,
		BaseBackoff: 30 * time.Second,
		MaxBackoff:  1 * time.Hour,
		// Blueprint SLO: no record in a non-terminal state for more than an hour.
		SweepAfter:      30 * time.Minute,
		SweepInterval:   5 * time.Minute,
		SystemicBackoff: 2 * time.Minute,
	}
}

type Queue struct {
	q   *store.Queries
	cfg Config
	log *slog.Logger
}

func NewQueue(pool *pgxpool.Pool, cfg Config, log *slog.Logger) *Queue {
	return &Queue{q: store.New(pool), cfg: cfg, log: log}
}

// Item is a claimed unit of work. Version is required to advance it.
type Item struct {
	ID       pgtype.UUID
	Version  int32
	Attempts int32
	State    State
}

// Claim takes at most BatchSize items in the given state and leases them.
// Backpressure comes from the batch size, never from memory.
func (qu *Queue) Claim(ctx context.Context, state State) ([]Item, error) {
	rows, err := qu.q.ClaimBatch(ctx, store.ClaimBatchParams{
		Lease: pgInterval(qu.cfg.Lease),
		State: string(state),
		Batch: qu.cfg.BatchSize,
	})
	if err != nil {
		return nil, fmt.Errorf("claim %s: %w", state, err)
	}
	items := make([]Item, 0, len(rows))
	for _, r := range rows {
		items = append(items, Item{
			ID: r.ID, Version: r.Version, Attempts: r.Attempts, State: State(r.PipelineState),
		})
	}
	return items, nil
}

// Advance moves an item to the next state, guarded by its version.
//
// The caller must have already committed its work in the same transaction for
// full atomicity; this is the standalone form used by stages whose work lives
// entirely in the row they are advancing.
func (qu *Queue) Advance(ctx context.Context, it Item) error {
	next, err := Next(it.State)
	if err != nil {
		return err
	}
	n, err := qu.q.AdvanceState(ctx, store.AdvanceStateParams{
		NextState:    string(next),
		ID:           it.ID,
		Version:      it.Version,
		CurrentState: string(it.State),
	})
	if err != nil {
		return fmt.Errorf("advance %s->%s: %w", it.State, next, err)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// Fail records the error, backs off, and parks the item once attempts are spent.
func (qu *Queue) Fail(ctx context.Context, it Item, cause error) error {
	msg := cause.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if _, err := qu.q.FailAttempt(ctx, store.FailAttemptParams{
		Err:         &msg,
		Backoff:     pgInterval(qu.backoff(it.Attempts)),
		MaxAttempts: qu.cfg.MaxAttempts,
		ID:          it.ID,
	}); err != nil {
		return fmt.Errorf("fail attempt: %w", err)
	}
	return nil
}

// backoff is exponential with a ceiling. Without the ceiling a few failures put
// the next attempt beyond any useful horizon.
func (qu *Queue) backoff(attempts int32) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(float64(qu.cfg.BaseBackoff) * math.Pow(2, float64(attempts-1)))
	if d > qu.cfg.MaxBackoff || d <= 0 {
		return qu.cfg.MaxBackoff
	}
	return d
}

// Defer puts an item back without spending an attempt.
//
// Used for systemic failures: the record is fine, the world is not. Attempts is
// decremented because ClaimBatch already incremented it on the way in.
func (qu *Queue) Defer(ctx context.Context, it Item, delay time.Duration) error {
	if err := qu.q.DeferItem(ctx, store.DeferItemParams{
		Delay: pgInterval(delay), ID: it.ID,
	}); err != nil {
		return fmt.Errorf("deferring: %w", err)
	}
	return nil
}

// Release drops a lease without advancing. Used on graceful shutdown so a clean
// deploy does not wait out the lease.
func (qu *Queue) Release(ctx context.Context, ids []pgtype.UUID) {
	for _, id := range ids {
		if err := qu.q.ReleaseClaim(ctx, id); err != nil {
			qu.log.Warn("releasing claim", "err", err)
		}
	}
}

// Sweep re-enqueues stranded records. This is what makes a lost event survivable.
func (qu *Queue) Sweep(ctx context.Context) (int64, error) {
	rows, err := qu.q.SweepStranded(ctx, store.SweepStrandedParams{
		Threshold: pgInterval(qu.cfg.SweepAfter),
		Batch:     qu.cfg.BatchSize,
	})
	if err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		qu.log.Warn("stranded record requeued",
			"opportunity_id", r.ID.String(), "state", r.PipelineState,
			"attempts", r.Attempts, "in_state_since", r.StateEnteredAt.Time)
	}
	n, err := qu.q.RequeueStranded(ctx, ids)
	if err != nil {
		return 0, fmt.Errorf("requeue: %w", err)
	}
	return n, nil
}

// Stats is the pipeline dashboard.
func (qu *Queue) Stats(ctx context.Context) ([]store.PipelineStatsRow, error) {
	return qu.q.PipelineStats(ctx)
}

func pgInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}
