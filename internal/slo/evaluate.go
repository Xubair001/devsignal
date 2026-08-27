package slo

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
)

// Evaluator computes the report from stored data.
//
// Latency and availability are absent here on purpose: a request that already
// returned left no row behind, so those come from metrics rather than from
// Postgres. Rather than pretend otherwise, they report as no_data until a metrics
// backend is wired — which is the difference between a gap and a fabrication.
type Evaluator struct {
	pool  *pgxpool.Pool
	q     *store.Queries
	clock func() time.Time
}

// NewEvaluator builds one.
func NewEvaluator(pool *pgxpool.Pool) *Evaluator {
	return &Evaluator{pool: pool, q: store.New(pool), clock: func() time.Time {
		return time.Now().UTC()
	}}
}

// WithClock replaces the clock, for tests.
func (e *Evaluator) WithClock(f func() time.Time) *Evaluator { e.clock = f; return e }

// LivenessCheckThreshold is how recently a visible posting must have been
// verified for the corpus to count as freshly checked.
//
// Not an SLO in blueprint §28 — it is the measurable neighbour of the liveness
// ACCURACY objective, which needs ground truth we do not have. Reported
// separately so the two are never confused.
const LivenessCheckThreshold = 24 * time.Hour

// Evaluate produces the whole report.
func (e *Evaluator) Evaluate(ctx context.Context) (*Report, error) {
	now := e.clock()
	rep := &Report{MeasuredAt: now}

	for _, o := range Objectives {
		res, err := e.evaluateOne(ctx, o)
		if err != nil {
			return nil, err
		}
		rep.Results = append(rep.Results, res...)
	}
	return rep, nil
}

func (e *Evaluator) evaluateOne(ctx context.Context, o Objective) ([]Result, error) {
	// An objective we cannot measure says so and stops. No query is run, because
	// running one would invite reporting whatever it returned.
	if !o.Measurable {
		return []Result{{Objective: o, Status: StatusUnmeasurable, Detail: o.Blocker}}, nil
	}

	switch o.ID {
	case PipelineBacklog:
		r, err := e.backlog(ctx, o)
		return []Result{r}, err
	case FreshnessTierA:
		r, err := e.freshness(ctx, o)
		return []Result{r}, err
	case ParseYield:
		return e.parseYield(ctx, o)
	case ExtractionValidity:
		r, err := e.extractionValidity(ctx, o)
		return []Result{r}, err
	case DigestGeneration:
		r, err := e.digestGeneration(ctx, o)
		return []Result{r}, err
	case FeedLatencyCached, FeedLatencyCold, APIAvailability:
		// Measurable in principle and instrumented, but the numbers live in the
		// metrics pipeline rather than the database. Saying no_data is honest;
		// substituting a database query that measures something adjacent is not.
		return []Result{{
			Objective: o, Status: StatusNoData,
			Detail: "instrumented as an OpenTelemetry metric; read it from the metrics " +
				"backend, not from this report",
		}}, nil
	default:
		return []Result{{
			Objective: o, Status: StatusNoData,
			Detail: "no evaluator wired for this objective",
		}}, nil
	}
}

func (e *Evaluator) backlog(ctx context.Context, o Objective) (Result, error) {
	row, err := e.q.SLIPipelineBacklog(ctx, interval(o.Window))
	if err != nil {
		return Result{}, fmt.Errorf("slo: pipeline backlog: %w", err)
	}
	r := EvaluateCount(o, row.Stranded)
	if row.Stranded > 0 {
		r.Detail = fmt.Sprintf("%d records stranded over %s, oldest %s",
			row.Stranded, o.Window, formatInterval(row.Oldest))
	}
	return r, nil
}

func (e *Evaluator) freshness(ctx context.Context, o Objective) (Result, error) {
	row, err := e.q.SLIFreshness(ctx, store.SLIFreshnessParams{
		Percentile: float64(o.Percentile) / 100,
		Lookback:   interval(o.Window),
	})
	if err != nil {
		return Result{}, fmt.Errorf("slo: freshness: %w", err)
	}
	res := EvaluateDuration(o, intervalDuration(row.Observed), row.Sample)
	// The caveat travels with the number. This is our pipeline's latency, not the
	// employer's publish-to-visible time, and the difference matters to anyone
	// reading it as a freshness guarantee.
	if res.Status != StatusNoData {
		res.Detail += " (measured from OUR first sight, not the source's claimed publish date)"
	}
	return res, nil
}

func (e *Evaluator) parseYield(ctx context.Context, o Objective) ([]Result, error) {
	rows, err := e.q.SLIParseYield(ctx, interval(o.Window))
	if err != nil {
		return nil, fmt.Errorf("slo: parse yield: %w", err)
	}
	if len(rows) == 0 {
		return []Result{{Objective: o, Status: StatusNoData,
			Detail: "no active sources"}}, nil
	}

	// One result per source, not an aggregate. An aggregate stays green while one
	// board silently returns empty fields, which is precisely the failure this
	// objective exists to catch.
	out := make([]Result, 0, len(rows))
	for _, row := range rows {
		per := o
		per.ID = o.ID + ":" + row.Name
		per.Description = o.Description + " — " + row.Name
		r := EvaluateRatio(per, row.Usable, row.Seen, o.Window)
		if row.Seen == 0 {
			r.Detail = "no postings seen in the window"
		}
		out = append(out, r)
	}
	return out, nil
}

func (e *Evaluator) extractionValidity(ctx context.Context, o Objective) (Result, error) {
	row, err := e.q.SLIExtractionValidity(ctx, interval(o.Window))
	if err != nil {
		return Result{}, fmt.Errorf("slo: extraction validity: %w", err)
	}
	r := EvaluateRatio(o, row.Valid, row.Total, o.Window)
	if row.Total == 0 {
		// Distinguish "extraction is not configured" from "extraction is failing".
		// Both give a zero count and they need opposite responses.
		r.Detail = "no extractions in the window; extraction may not be configured"
	}
	return r, nil
}

// digestGeneration measures the most recent run's spread.
//
// Reports no_data rather than met when no run has happened. A 0-second spread
// over zero users would clear a 30-minute target trivially, and an objective
// that goes green because the job never ran is worse than one with a visible
// gap — the gap prompts a question, the false green ends the conversation.
func (e *Evaluator) digestGeneration(ctx context.Context, o Objective) (Result, error) {
	row, err := e.q.SLIDigestGeneration(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("slo: digest generation: %w", err)
	}
	if row.Users == 0 {
		return Result{
			Objective: o, Status: StatusNoData,
			Detail: "no digest run has produced anything yet; the objective is " +
				"measurable but there is nothing to measure",
		}, nil
	}
	r := EvaluateDuration(o, intervalDuration(row.Spread), row.Users)
	r.Detail += fmt.Sprintf(" across %d users in the most recent run", row.Users)
	return r, nil
}

// LivenessFreshness reports how recently the visible corpus was verified.
//
// Returned separately from the report rather than folded into the liveness
// accuracy objective, because they are different claims and conflating them is
// how a dashboard starts asserting something nobody measured.
type LivenessFreshness struct {
	Shown           int64
	CheckedRecently int64
	OldestCheck     time.Duration
	Threshold       time.Duration
}

// Fraction is the share of visible postings verified inside the threshold.
func (l LivenessFreshness) Fraction() float64 {
	if l.Shown == 0 {
		return 0
	}
	return float64(l.CheckedRecently) / float64(l.Shown)
}

// LivenessFreshness measures verification recency.
func (e *Evaluator) LivenessFreshness(ctx context.Context) (*LivenessFreshness, error) {
	row, err := e.q.SLILivenessFreshness(ctx, interval(LivenessCheckThreshold))
	if err != nil {
		return nil, fmt.Errorf("slo: liveness freshness: %w", err)
	}
	return &LivenessFreshness{
		Shown: row.Shown, CheckedRecently: row.CheckedRecently,
		OldestCheck: intervalDuration(row.OldestCheck),
		Threshold:   LivenessCheckThreshold,
	}, nil
}

// StateRow is one row of the pipeline state distribution.
type StateRow struct {
	State   string
	Records int64
	Oldest  time.Time
}

// PipelineStates returns the state distribution, which CLAUDE.md calls the
// pipeline dashboard. A large count that is moving is healthy; a small one that
// is not is an incident, so the oldest entry travels with the count.
func (e *Evaluator) PipelineStates(ctx context.Context) ([]StateRow, error) {
	rows, err := e.q.SLIPipelineStateDistribution(ctx)
	if err != nil {
		return nil, fmt.Errorf("slo: state distribution: %w", err)
	}
	out := make([]StateRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, StateRow{
			State: r.PipelineState, Records: r.Records, Oldest: r.OldestEntered.Time,
		})
	}
	return out, nil
}

func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func intervalDuration(i pgtype.Interval) time.Duration {
	if !i.Valid {
		return 0
	}
	// Postgres splits an interval into months, days and microseconds. Days matter
	// here — a stranded record can be days old — and months are not reachable by
	// these windows but are converted anyway rather than silently dropped.
	d := time.Duration(i.Microseconds) * time.Microsecond
	d += time.Duration(i.Days) * 24 * time.Hour
	d += time.Duration(i.Months) * 30 * 24 * time.Hour
	return d
}

func formatInterval(i pgtype.Interval) string {
	return intervalDuration(i).Round(time.Second).String()
}
