package skill

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
)

// Clock is injected. Hard rule 14: the day a snapshot belongs to is domain
// logic, and time.Now() inside it is untestable.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// DemandWriter snapshots skill demand.
//
// This table had no writer at all until now, despite migration 000004 calling it
// THE MOAT and both CLAUDE.md and the frontend plan claiming it had been
// "collecting since step 8". It had not. The claim is corrected and this is the
// writer.
type DemandWriter struct {
	pool  *pgxpool.Pool
	q     *store.Queries
	clock Clock
}

// NewDemandWriter builds one.
func NewDemandWriter(pool *pgxpool.Pool, c Clock) *DemandWriter {
	if c == nil {
		c = realClock{}
	}
	return &DemandWriter{pool: pool, q: store.New(pool), clock: c}
}

// DemandReport is what a run produces.
type DemandReport struct {
	Day  time.Time
	Rows int64
	// DaysCollected is how much history exists. The number that matters for the
	// moat: one day of data answers nothing, and a gap cannot be filled later.
	DaysCollected int64
	Latest        string
	Top           []store.TopSkillDemandRow
}

// Snapshot records what the live corpus is asking for today.
//
// Idempotent by construction — a recomputed snapshot, not an increment — so
// running it twice in a day is harmless. That is deliberate: an incrementing
// counter would double-count every re-extraction and drift with no way to audit
// it, and this series is the one thing in the system that cannot be rebuilt from
// anything else.
func (w *DemandWriter) Snapshot(ctx context.Context) (*DemandReport, error) {
	day := w.clock.Now().UTC()
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)

	rows, err := w.q.SnapshotSkillDemand(ctx, pgtype.Date{Time: d, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("skill: snapshotting demand: %w", err)
	}

	hist, err := w.q.SkillDemandDays(ctx)
	if err != nil {
		return nil, fmt.Errorf("skill: demand history: %w", err)
	}
	top, err := w.q.TopSkillDemand(ctx, store.TopSkillDemandParams{
		Day: pgtype.Date{Time: d, Valid: true}, MaxRows: 15,
	})
	if err != nil {
		return nil, fmt.Errorf("skill: top demand: %w", err)
	}

	return &DemandReport{
		Day: d, Rows: rows, DaysCollected: hist.Days, Latest: hist.Latest, Top: top,
	}, nil
}
