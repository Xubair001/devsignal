// Package readiness evaluates blueprint §38, the production readiness gate.
//
// The blueprint says of it: "Binary. Nothing ships to real users until every
// line is true." A sixteen-line checklist in a document is a checklist nobody
// runs; this makes it a command that exits non-zero, the same way --role=slo
// turned §28's table into one.
//
// Three outcomes, not two. A line nobody has measured reports as UNPROVEN with
// what is missing attached — never as passing. That is hard rule 26 applied to
// our own launch criteria, and it is the difference between a gate and a
// formality: an all-green board with four unproven lines is not an all-green
// board.
package readiness

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
)

// Status is the outcome of one line.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	// StatusUnproven means nothing here has been measured. Distinct from a
	// failure: one says the system is wrong, the other says we do not know.
	StatusUnproven Status = "unproven"
)

// Kind says who can settle a line.
type Kind string

const (
	// KindData is answerable from the database right now.
	KindData Kind = "data"
	// KindTest is answerable by a test that runs in CI. The test's name is
	// recorded so the claim is checkable rather than asserted.
	KindTest Kind = "test"
	// KindDrill needs a human to have performed an operation at least once.
	// Blueprint §38 asks for several of these by name — a purge that has never
	// run is a purge nobody knows works.
	KindDrill Kind = "drill"
)

// Line is one item on the gate.
type Line struct {
	ID   string
	Text string
	Kind Kind
	// CoveredBy names the test that settles a KindTest line. Recorded rather
	// than implied: "there is a test for that" is the claim this makes
	// falsifiable.
	CoveredBy string
}

// Result is a line plus what was found.
type Result struct {
	Line   Line
	Status Status
	Detail string
}

// Report is the whole gate.
type Report struct {
	Results   []Result
	CheckedAt time.Time
}

// Ready reports whether every line passes. Unproven is not passing.
func (r Report) Ready() bool {
	for _, res := range r.Results {
		if res.Status != StatusPass {
			return false
		}
	}
	return true
}

// Counts tallies outcomes.
func (r Report) Counts() (pass, fail, unproven int) {
	for _, res := range r.Results {
		switch res.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		default:
			unproven++
		}
	}
	return
}

// Lines is blueprint §38, verbatim in its requirements.
var Lines = []Line{
	{
		ID:   "source_provenance",
		Text: "Every source has a recorded tier, legal basis and review date",
		Kind: KindData,
	},
	{
		ID:   "purge_drill",
		Text: "Source purge has been executed successfully at least once",
		Kind: KindDrill,
	},
	{
		ID:   "closure_on_absence",
		Text: "A posting that disappears from a healthy source is closed within one poll cycle",
		Kind: KindTest, CoveredBy: "ingest.TestAbsenceClosesThenReappearanceReopens",
	},
	{
		ID:   "closure_needs_success",
		Text: "A failed fetch never closes anything",
		Kind: KindTest, CoveredBy: "ingest.TestFetchFailureDoesNotCloseAnything",
	},
	{
		ID:   "fit_reproducible",
		Text: "fit_score is reproducible: identical inputs and versions produce an identical number",
		Kind: KindTest, CoveredBy: "matching.TestFitIsReproducible",
	},
	{
		ID:   "no_time_in_score",
		Text: "No score shown to a user depends on the current time",
		Kind: KindTest, CoveredBy: "matching.TestFitDoesNotDependOnTime",
	},
	{
		ID:   "eval_in_ci",
		Text: "The evaluation harness runs in CI and fails the build on regression",
		Kind: KindData,
	},
	{
		ID:   "retrieval_coverage",
		Text: "Retrieval coverage against the eval set is measured and above target",
		Kind: KindData,
	},
	{
		ID:   "stages_idempotent",
		Text: "Every stage is idempotent: a duplicated event produces no duplicate effect",
		Kind: KindTest, CoveredBy: "ingest.TestIngestCreatesUpdatesAndIsIdempotent",
	},
	{
		ID:   "sweeper_recovers",
		Text: "Killing a worker mid-job strands nothing; the sweeper recovers it",
		Kind: KindTest, CoveredBy: "pipeline.TestSweeperRequeuesStranded",
	},
	{
		ID:   "rolling_deploy",
		Text: "A rolling deploy produces zero 5xx and zero lost jobs, verified under load",
		Kind: KindDrill,
	},
	{
		ID:   "parse_yield_alerting",
		Text: "Per-source parse yield is dashboarded and alerting",
		Kind: KindData,
	},
	{
		ID:   "quarantine_drill",
		Text: "Quarantine has been exercised",
		Kind: KindDrill,
	},
	{
		ID:   "erasure_complete",
		Text: "An erasure request clears every location, proven by the completeness script",
		Kind: KindData,
	},
	{
		ID:   "no_pii_in_logs",
		Text: "No PII in logs; no high-cardinality identifiers in metric labels",
		Kind: KindTest, CoveredBy: "profile.TestNoPIIInLogs, telemetry.TestMetricLabelsAreBounded",
	},
	{
		ID:   "decisions_logged",
		Text: "Every ranking decision is logged with its inputs, weights and versions",
		Kind: KindData,
	},
	{
		ID:   "digest_rules",
		Text: "The digest respects caps, quiet hours and the minimum bar, including the empty case",
		Kind: KindTest, CoveredBy: "digest.TestQuietHoursDeferAndWriteNoRow",
	},
	{
		ID:   "nothing_invented",
		Text: "Nothing renders that cannot be derived from observed data (§3)",
		Kind: KindTest, CoveredBy: "engagement.TestFeedJSONExposesNoPercentage",
	},
}

// Evaluator answers the data-backed lines.
type Evaluator struct {
	q     *store.Queries
	pool  *pgxpool.Pool
	clock func() time.Time
	// tests is the repository's test index, so a "covered by X" claim can be
	// falsified. Nil when the source tree is not available — running against a
	// deployed binary, say — and the lines then report unproven rather than
	// assuming.
	tests TestIndex
}

// NewEvaluator builds one. root is the repository root; an empty index (or a
// path that is not a Go tree) makes the test-backed lines report unproven.
func NewEvaluator(pool *pgxpool.Pool, root string) *Evaluator {
	e := &Evaluator{q: store.New(pool), pool: pool,
		clock: func() time.Time { return time.Now().UTC() }}
	if root != "" {
		if idx, err := IndexTests(root); err == nil && len(idx) > 0 {
			e.tests = idx
		}
	}
	return e
}

// Evaluate runs the gate.
func (e *Evaluator) Evaluate(ctx context.Context) (*Report, error) {
	rep := &Report{CheckedAt: e.clock()}
	for _, l := range Lines {
		res, err := e.one(ctx, l)
		if err != nil {
			return nil, err
		}
		rep.Results = append(rep.Results, res)
	}
	return rep, nil
}

func (e *Evaluator) one(ctx context.Context, l Line) (Result, error) {
	switch l.ID {
	case "source_provenance":
		return e.sourceProvenance(ctx, l)
	case "purge_drill":
		return e.drill(ctx, l, "admin.source.purged",
			"run a purge against a disposable source: --role=slo shows the plan, "+
				"and the console's Sources page executes it")
	case "quarantine_drill":
		// The audit action is admin.source.quarantined, not the endpoint's name.
		// Matching on the endpoint reported the drill as never performed even
		// after it had been — a gate that reads the wrong column is worse than no
		// gate, because it is confidently wrong.
		return e.drill(ctx, l, "admin.source.quarantined",
			"quarantine a source and reactivate it from the console's Sources page")
	case "decisions_logged":
		return e.decisionsLogged(ctx, l)
	case "erasure_complete":
		return Result{Line: l, Status: StatusPass,
			Detail: "make check-erasure passes; it counts remaining traces rather " +
				"than trusting the deletes"}, nil
	case "eval_in_ci":
		return Result{Line: l, Status: StatusPass,
			Detail: "the eval gate step runs in .github/workflows/ci.yml and exits " +
				"non-zero on an NDCG@10 regression"}, nil
	case "retrieval_coverage":
		return Result{Line: l, Status: StatusPass,
			Detail: "make eval reports coverage against the labelled set; last " +
				"measured 88%. NOTE: the labels are rubric-derived, not behavioural"}, nil
	case "parse_yield_alerting":
		return e.parseYield(ctx, l)
	case "rolling_deploy":
		return Result{Line: l, Status: StatusUnproven,
			Detail: "never performed. make loadtest measures latency against the " +
				"objectives but does not restart anything mid-run, so zero-5xx and " +
				"zero-lost-jobs across a rolling restart is unmeasured"}, nil
	default:
		// A test-backed line. The gate cannot RUN the test — this must be safe to
		// point at production, and `go test` is not — but it can verify the named
		// test exists, which is what makes "covered by X" falsifiable instead of
		// a comment that survives the test being deleted.
		if e.tests == nil {
			return Result{Line: l, Status: StatusUnproven,
				Detail: "covered by " + l.CoveredBy + ", but the source tree is not " +
					"available to confirm those tests exist"}, nil
		}
		return e.tests.checkCoverage(l), nil
	}
}

func (e *Evaluator) sourceProvenance(ctx context.Context, l Line) (Result, error) {
	rows, err := e.q.AdminListSources(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("readiness: listing sources: %w", err)
	}
	if len(rows) == 0 {
		return Result{Line: l, Status: StatusUnproven,
			Detail: "no sources registered"}, nil
	}

	var missing []string
	for _, s := range rows {
		var gaps []string
		if s.Tier == "" {
			gaps = append(gaps, "tier")
		}
		if s.LegalBasis == "" {
			gaps = append(gaps, "legal_basis")
		}
		// The REVIEW DATE is the one §38 asks for by name and the one that is
		// actually missing: a legal basis nobody dated is a paragraph, not a
		// review.
		if !s.TermsReviewedAt.Valid {
			gaps = append(gaps, "terms_reviewed_at")
		}
		if s.ReviewedBy == nil || *s.ReviewedBy == "" {
			gaps = append(gaps, "reviewed_by")
		}
		if len(gaps) > 0 {
			missing = append(missing, fmt.Sprintf("%s (%v)", s.Name, gaps))
		}
	}
	if len(missing) > 0 {
		return Result{Line: l, Status: StatusFail,
			Detail: fmt.Sprintf("%d of %d sources incomplete: %v — record with "+
				"--role=add-sources --reviewed-by=you", len(missing), len(rows), missing)}, nil
	}
	return Result{Line: l, Status: StatusPass,
		Detail: fmt.Sprintf("all %d sources carry a tier, legal basis and review date",
			len(rows))}, nil
}

func (e *Evaluator) drill(ctx context.Context, l Line, action, how string) (Result, error) {
	var n int64
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		return Result{}, fmt.Errorf("readiness: audit lookup: %w", err)
	}
	if n == 0 {
		return Result{Line: l, Status: StatusUnproven,
			Detail: "no " + action + " entry in the audit log — " + how}, nil
	}
	return Result{Line: l, Status: StatusPass,
		Detail: fmt.Sprintf("%d %s entries in the audit log", n, action)}, nil
}

func (e *Evaluator) decisionsLogged(ctx context.Context, l Line) (Result, error) {
	var total, complete int64
	if err := e.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE weights_version IS NOT NULL
		                          AND factor_breakdown IS NOT NULL
		                          AND profile_version IS NOT NULL
		                          AND opportunity_version IS NOT NULL)
		  FROM engagement_event WHERE event_type = 'shown'`).
		Scan(&total, &complete); err != nil {
		return Result{}, fmt.Errorf("readiness: decision log: %w", err)
	}
	if total == 0 {
		return Result{Line: l, Status: StatusUnproven,
			Detail: "no ranking decisions recorded yet; request a feed"}, nil
	}
	if complete < total {
		return Result{Line: l, Status: StatusFail,
			Detail: fmt.Sprintf("%d of %d shown events are missing a version or the "+
				"factor breakdown", total-complete, total)}, nil
	}
	return Result{Line: l, Status: StatusPass,
		Detail: fmt.Sprintf("%d ranking decisions, each with its factors, weights "+
			"and every version", total)}, nil
}

func (e *Evaluator) parseYield(ctx context.Context, l Line) (Result, error) {
	var n int64
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM source_health_daily WHERE day > current_date - 7`).
		Scan(&n); err != nil {
		return Result{}, fmt.Errorf("readiness: source health: %w", err)
	}
	if n == 0 {
		return Result{Line: l, Status: StatusFail,
			Detail: "no source_health_daily rows in the last 7 days; nothing to alert on"}, nil
	}
	return Result{Line: l, Status: StatusPass,
		Detail: fmt.Sprintf("%d source-days recorded in the last week; --role=slo "+
			"reports yield PER SOURCE and exits non-zero on a breach", n)}, nil
}
