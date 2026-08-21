package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"
)

// Handler does the work for one item. It must be idempotent: a lease expiry, a
// redeploy mid-batch or a duplicated event will all cause a re-run.
type Handler func(ctx context.Context, it Item) error

// Stage binds a state to its handler and its concurrency.
type Stage struct {
	State       State
	Concurrency int
	Handle      Handler
	// PollInterval is how long to wait when a claim comes back empty. Without a
	// pause an empty queue becomes a spin loop against the database.
	PollInterval time.Duration
}

// Worker runs one stage. Concurrency is always bounded — a goroutine per item
// over an unbounded input is how a service OOMs.
type Worker struct {
	queue *Queue
	stage Stage
	log   *slog.Logger

	mu       sync.Mutex
	inFlight map[pgtype.UUID]struct{}
}

func NewWorker(q *Queue, s Stage, log *slog.Logger) *Worker {
	if s.Concurrency < 1 {
		s.Concurrency = 1
	}
	if s.PollInterval <= 0 {
		s.PollInterval = 2 * time.Second
	}
	return &Worker{
		queue: q, stage: s,
		log:      log.With("stage", string(s.State)),
		inFlight: make(map[pgtype.UUID]struct{}),
	}
}

// Run claims and processes until ctx is cancelled, then releases whatever it
// still holds so a clean deploy does not wait out the lease.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("worker started", "concurrency", w.stage.Concurrency)
	// Deliberately context-free: by the time this runs the parent is cancelled,
	// and the release must still reach the database.
	//nolint:contextcheck // release needs a fresh context, not the cancelled one
	defer w.releaseOutstanding()

	for {
		if ctx.Err() != nil {
			w.log.Info("worker stopping: no longer claiming")
			return nil
		}

		items, err := w.queue.Claim(ctx, w.stage.State)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.log.Error("claim failed", "err", err)
			if !sleepCtx(ctx, w.stage.PollInterval) {
				return nil
			}
			continue
		}

		if len(items) == 0 {
			if !sleepCtx(ctx, w.stage.PollInterval) {
				return nil
			}
			continue
		}

		w.log.Debug("batch claimed", "count", len(items))
		w.process(ctx, items)
	}
}

func (w *Worker) process(ctx context.Context, items []Item) {
	// One span per batch, not per item: per-record spans on this pipeline cost
	// more than the compute they observe.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(w.stage.Concurrency)

	for _, it := range items {
		it := it
		w.track(it.ID)
		g.Go(func() error {
			defer w.untrack(it.ID)
			w.handleOne(gctx, it)
			return nil // never abort siblings: one bad record is not a batch failure
		})
	}
	_ = g.Wait()
}

func (w *Worker) handleOne(ctx context.Context, it Item) {
	err := w.safeHandle(ctx, it)
	if errors.Is(err, ErrHandled) {
		// The handler advanced the row itself, in the same statement as its write.
		return
	}
	if errors.Is(err, ErrVersionConflict) {
		// Another stage got there first. Leave it; the next claim or the sweeper
		// picks it up with a fresh version.
		w.log.Debug("version conflict before work; yielding", "id", it.ID.String())
		return
	}
	if err == nil {
		if aerr := w.queue.Advance(ctx, it); aerr != nil {
			if errors.Is(aerr, ErrVersionConflict) {
				// Another stage wrote first. Correct behaviour is to drop it: the
				// sweeper or the next claim will pick it up with a fresh version.
				w.log.Debug("version conflict, leaving for requeue", "id", it.ID.String())
				return
			}
			w.log.Error("advance failed", "id", it.ID.String(), "err", aerr)
		}
		return
	}

	// A degradable stage must not block visibility. Publish with the flag and
	// move on; a posting with no extracted skills beats an invisible posting.
	if it.State.Degradable() && it.Attempts >= w.queue.cfg.MaxAttempts {
		w.log.Warn("degrading past failed optional stage",
			"id", it.ID.String(), "attempts", it.Attempts, "err", err)
		if aerr := w.queue.Advance(ctx, it); aerr != nil && !errors.Is(aerr, ErrVersionConflict) {
			w.log.Error("degrade advance failed", "id", it.ID.String(), "err", aerr)
		}
		return
	}

	if ferr := w.queue.Fail(ctx, it, err); ferr != nil {
		w.log.Error("recording failure", "id", it.ID.String(), "err", ferr)
	}
}

// safeHandle contains panics. One malformed document must not kill the process
// and take the rest of the batch with it.
func (w *Worker) safeHandle(ctx context.Context, it Item) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("panic in handler: " + sprint(r))
			w.log.Error("handler panicked", "id", it.ID.String(), "panic", r)
		}
	}()
	return w.stage.Handle(ctx, it)
}

func (w *Worker) track(id pgtype.UUID) {
	w.mu.Lock()
	w.inFlight[id] = struct{}{}
	w.mu.Unlock()
}

func (w *Worker) untrack(id pgtype.UUID) {
	w.mu.Lock()
	delete(w.inFlight, id)
	w.mu.Unlock()
}

func (w *Worker) releaseOutstanding() {
	w.mu.Lock()
	ids := make([]pgtype.UUID, 0, len(w.inFlight))
	for id := range w.inFlight {
		ids = append(ids, id)
	}
	w.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	// Fresh context: the parent is already cancelled, and the release must run.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w.log.Info("releasing outstanding claims", "count", len(ids))
	w.queue.Release(ctx, ids)
}

// Sweeper periodically re-enqueues stranded records.
type Sweeper struct {
	queue *Queue
	log   *slog.Logger
}

func NewSweeper(q *Queue, log *slog.Logger) *Sweeper {
	return &Sweeper{queue: q, log: log.With("component", "sweeper")}
}

func (s *Sweeper) Run(ctx context.Context) error {
	t := time.NewTicker(s.queue.cfg.SweepInterval)
	defer t.Stop()
	s.log.Info("sweeper started", "interval", s.queue.cfg.SweepInterval,
		"threshold", s.queue.cfg.SweepAfter)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			n, err := s.queue.Sweep(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				s.log.Error("sweep failed", "err", err)
				continue
			}
			if n > 0 {
				s.log.Warn("requeued stranded records", "count", n)
			}
		}
	}
}

// sleepCtx returns false if the context was cancelled during the wait.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func sprint(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "unknown"
}
