package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/source"
	"github.com/Xubair001/devsignal/internal/store"
)

// QuarantineYieldFloor is the parse yield below which a source is quarantined.
//
// Alerting on a relative drop is what catches parser rot: a source that fell
// from 98% to 71% field completeness is broken even though nothing errored.
const QuarantineYieldFloor = 0.80

// Runner polls due sources. There is no scheduler process — schedules are rows
// claimed with SKIP LOCKED, so no single point of failure and no double-firing.
type Runner struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	svc    *Service
	client *source.Client
	log    *slog.Logger

	Interval time.Duration
	Lease    time.Duration
	Batch    int32
}

func NewRunner(pool *pgxpool.Pool, client *source.Client, log *slog.Logger) *Runner {
	return &Runner{
		pool: pool, q: store.New(pool), svc: New(pool, log), client: client,
		log:      log.With("component", "source-runner"),
		Interval: 30 * time.Second,
		Lease:    10 * time.Minute,
		Batch:    5,
	}
}

func (r *Runner) Run(ctx context.Context) error {
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	r.log.Info("source runner started", "adapters", source.Registered())
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := r.tick(ctx); err != nil && ctx.Err() == nil {
				r.log.Error("source tick", "err", err)
			}
		}
	}
}

func (r *Runner) tick(ctx context.Context) error {
	due, err := r.q.ClaimDueSources(ctx, store.ClaimDueSourcesParams{
		Lease: pgtype.Interval{Microseconds: r.Lease.Microseconds(), Valid: true},
		Batch: r.Batch,
	})
	if err != nil {
		return fmt.Errorf("claim due sources: %w", err)
	}
	for _, d := range due {
		r.runOne(ctx, d.SourceID, d.Cursor)
	}
	return nil
}

// RunOnce polls a single source by name. Used by the one-shot CLI path and by
// operators re-running a source after fixing a parser.
func (r *Runner) RunOnce(ctx context.Context, name string) (Result, error) {
	src, err := r.q.GetSourceByName(ctx, name)
	if err != nil {
		return Result{}, fmt.Errorf("source %q: %w", name, err)
	}
	// Read the cursor directly rather than via ClaimDueSources: a manual run is
	// not gated on the poll interval, and claiming here would also lease an
	// unrelated source's schedule as a side effect.
	cursor, err := r.q.GetSourceCursor(ctx, src.ID)
	if err != nil {
		r.log.Warn("no stored cursor; falling back to a full fetch", "source", name, "err", err)
		cursor = []byte("{}")
	}
	return r.execute(ctx, src, cursor)
}

func (r *Runner) runOne(ctx context.Context, sourceID pgtype.UUID, cursor []byte) {
	src, err := r.q.GetSourceByID(ctx, sourceID)
	if err != nil {
		r.log.Error("loading source", "source_id", sourceID.String(), "err", err)
		return
	}
	if _, err := r.execute(ctx, src, cursor); err != nil {
		r.log.Error("source run failed", "source", src.Name, "err", err)
	}
}

func (r *Runner) execute(ctx context.Context, src store.Source, cursorJSON []byte) (Result, error) {
	adapter, err := r.buildAdapter(src)
	if err != nil {
		r.recordFailure(ctx, src.ID, err)
		return Result{}, err
	}

	cur := decodeCursor(cursorJSON)

	start := time.Now()
	res, next, err := r.svc.RunSource(ctx, src.ID, adapter, cur)
	if err != nil {
		r.recordFailure(ctx, src.ID, err)
		return res, err
	}

	if res.NotModified {
		// A 304 is a successful poll: reachable and unchanged.
		r.log.Info("source unchanged", "source", src.Name, "took", time.Since(start))
	} else {
		r.log.Info("source polled", "source", src.Name,
			"fetched", res.Fetched, "created", res.Created, "updated", res.Updated,
			"unchanged", res.Unchanged, "skipped", res.Skipped,
			"missed", res.Missed, "closed", res.Closed,
			"yield", fmt.Sprintf("%.3f", res.ParseYield()), "took", time.Since(start))
	}

	yield := res.ParseYield()
	if err := r.q.RecordSourceSuccess(ctx, store.RecordSourceSuccessParams{
		Discovered: int64(res.Fetched),
		Processed:  int64(res.Created + res.Updated + res.Unchanged),
		Yield:      numeric(yield),
		ID:         src.ID,
	}); err != nil {
		r.log.Warn("recording source success", "err", err)
	}

	// Quarantine on a yield collapse rather than on an error: keep serving the
	// last good data and page a human.
	if res.Fetched > 0 && yield < QuarantineYieldFloor {
		r.log.Error("quarantining source on parse-yield collapse",
			"source", src.Name, "yield", yield, "floor", QuarantineYieldFloor)
		if qerr := r.q.QuarantineSource(ctx, store.QuarantineSourceParams{
			ID: src.ID, LastError: strptr(fmt.Sprintf("parse yield %.3f below floor %.2f", yield, QuarantineYieldFloor)),
		}); qerr != nil {
			r.log.Warn("quarantining", "err", qerr)
		}
	}

	if err := r.q.SaveSourceCursor(ctx, store.SaveSourceCursorParams{
		ID: src.ID, Cursor: encodeCursor(next),
	}); err != nil {
		r.log.Warn("saving cursor", "err", err)
	}
	return res, nil
}

// buildAdapter maps a registry row to a constructed adapter. The source `type`
// names the adapter family; `name` carries its configuration.
func (r *Runner) buildAdapter(src store.Source) (source.Adapter, error) {
	// Convention: name is "<family>:<config>", e.g. "greenhouse:gitlab".
	family, cfg, ok := strings.Cut(src.Name, ":")
	if !ok {
		return nil, fmt.Errorf("source name %q must be \"<family>:<config>\"", src.Name)
	}
	return source.Build(family, source.Options{
		Config: map[string]string{"board_token": cfg},
		Client: r.client,
	})
}

func (r *Runner) recordFailure(ctx context.Context, id pgtype.UUID, cause error) {
	msg := cause.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	if err := r.q.RecordSourceFailure(ctx, store.RecordSourceFailureParams{
		ID: id, LastError: &msg,
	}); err != nil {
		r.log.Warn("recording source failure", "err", err)
	}
}
