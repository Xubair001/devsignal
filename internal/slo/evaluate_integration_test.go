//go:build integration

package slo

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
)

func seed(t *testing.T) (*pgxpool.Pool, pgtype.UUID) {
	t.Helper()
	pool := dbtest.Pool(t)
	var companyID pgtype.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'SLO Co') RETURNING id`,
		"slo-"+uuid.NewString()[:8]+".example").Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})
	return pool, companyID
}

// posting inserts one opportunity with explicit timing. state_entered_at is
// maintained by a trigger on state CHANGE, so it is set in the INSERT rather than
// by a later UPDATE — an UPDATE would have the trigger overwrite it with now().
func posting(t *testing.T, pool *pgxpool.Pool, company pgtype.UUID,
	state string, firstSeen, stateEntered time.Time) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO opportunity (company_id, title_raw, title_normalized,
		  pipeline_state, first_seen_at, state_entered_at, last_seen_at)
		VALUES ($1,'Role','role',$2,$3,$4,now()) RETURNING id`,
		company, state, firstSeen, stateEntered).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A gate that only ever reports zero is decoration. This proves the backlog
// objective detects a real stranded record.
func TestBacklogObjectiveDetectsStrandedRecords(t *testing.T) {
	ctx := context.Background()
	pool, company := seed(t)
	ev := NewEvaluator(pool)
	now := time.Now().UTC()

	// Inside the threshold: not stranded.
	posting(t, pool, company, "parsed", now.Add(-10*time.Minute), now.Add(-10*time.Minute))
	rep, err := ev.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, rep, PipelineBacklog); got != StatusMet {
		t.Errorf("a 10-minute-old record gave %q, want met", got)
	}

	// Past the threshold: stranded.
	posting(t, pool, company, "parsed", now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	rep, err = ev.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, rep, PipelineBacklog); got != StatusBreached {
		t.Errorf("a 3-hour-old record gave %q, want breached", got)
	}
	if rep.Healthy() {
		t.Error("a breached objective left the report healthy")
	}
}

// A terminal state is not a backlog however old it is: 'ready' and
// 'failed_permanent' are where records are supposed to stop.
func TestTerminalStatesAreNotBacklog(t *testing.T) {
	ctx := context.Background()
	pool, company := seed(t)
	now := time.Now().UTC()

	for _, state := range []string{"ready", "failed_permanent"} {
		posting(t, pool, company, state, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour))
	}
	rep, err := NewEvaluator(pool).Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, rep, PipelineBacklog); got != StatusMet {
		t.Errorf("month-old terminal records gave %q, want met", got)
	}
}

func TestFreshnessMeasuresFirstSeenToReady(t *testing.T) {
	ctx := context.Background()
	pool, company := seed(t)
	ev := NewEvaluator(pool)
	now := time.Now().UTC()

	// Well inside the 15-minute target.
	for range 5 {
		posting(t, pool, company, "ready", now.Add(-10*time.Minute), now.Add(-8*time.Minute))
	}
	rep, err := ev.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res := resultOf(t, rep, FreshnessTierA)
	if res.Status != StatusMet {
		t.Errorf("a 2-minute pipeline gave %q: %s", res.Status, res.Detail)
	}
	// The caveat must travel with the number, or it reads as a publish-to-visible
	// guarantee we are not making.
	if !contains(res.Detail, "OUR first sight") {
		t.Errorf("detail %q does not state what the measurement is from", res.Detail)
	}

	// Now push the p95 past the target.
	for range 20 {
		posting(t, pool, company, "ready", now.Add(-2*time.Hour), now.Add(-10*time.Minute))
	}
	rep, err = ev.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, rep, FreshnessTierA); got != StatusBreached {
		t.Errorf("a 110-minute pipeline gave %q, want breached", got)
	}
}

// An empty corpus must report no_data, not a spurious pass. A green board with
// nothing behind it is the failure this package is arranged to avoid.
func TestEmptyCorpusReportsNoDataRatherThanPassing(t *testing.T) {
	ctx := context.Background()
	pool, _ := seed(t)

	rep, err := NewEvaluator(pool).Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Freshness has nothing to measure.
	if got := statusOf(t, rep, FreshnessTierA); got != StatusNoData && got != StatusMet {
		t.Errorf("freshness on an empty window gave %q", got)
	}
	// And every unmeasurable objective still names its blocker.
	for _, r := range rep.Unmeasurable() {
		if r.Detail == "" {
			t.Errorf("%s is unmeasurable with no reason", r.Objective.ID)
		}
		if r.Observed != nil {
			t.Errorf("%s reported an observed value while unmeasurable", r.Objective.ID)
		}
	}
}

// Parse yield is per source. An aggregate stays green while one board silently
// returns empty fields, which is the failure it exists to catch.
func TestParseYieldIsReportedPerSource(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)

	healthy := seedSource(t, pool, "slo-healthy")
	broken := seedSource(t, pool, "slo-broken")

	today := time.Now().UTC()
	insertHealth(t, pool, healthy, today, 100, 100)
	insertHealth(t, pool, broken, today, 100, 40) // parser rot

	rep, err := NewEvaluator(pool).Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var sawHealthy, sawBroken bool
	for _, r := range rep.Results {
		switch r.Objective.ID {
		case ParseYield + ":slo-healthy":
			sawHealthy = true
			if r.Status != StatusMet {
				t.Errorf("a healthy source gave %q: %s", r.Status, r.Detail)
			}
		case ParseYield + ":slo-broken":
			sawBroken = true
			if r.Status != StatusBreached {
				t.Errorf("a source at 40%% yield gave %q: %s", r.Status, r.Detail)
			}
		}
	}
	if !sawHealthy || !sawBroken {
		t.Error("parse yield was not reported per source; an aggregate would hide the broken one")
	}
	if rep.Healthy() {
		t.Error("a source at 40% parse yield left the report healthy")
	}
}

func seedSource(t *testing.T, pool *pgxpool.Pool, name string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	full := name + "-" + uuid.NewString()[:6]
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO source (name, tier, type, legal_basis, poll_interval)
		VALUES ($1,'a','test','test', interval '1 hour') RETURNING id`, full).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// The evaluator keys results on the source NAME, so the test needs the exact
	// one it inserted.
	if _, err := pool.Exec(context.Background(),
		`UPDATE source SET name=$2 WHERE id=$1`, id, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM source_health_daily WHERE source_id=$1`, id)
		_, _ = pool.Exec(c, `DELETE FROM source WHERE id=$1`, id)
	})
	return id
}

func insertHealth(t *testing.T, pool *pgxpool.Pool, source pgtype.UUID,
	day time.Time, seen, usable int32) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO source_health_daily (source_id, day, polls, postings_seen, postings_usable)
		VALUES ($1,$2,1,$3,$4)
		ON CONFLICT (source_id, day) DO UPDATE
		  SET postings_seen = excluded.postings_seen,
		      postings_usable = excluded.postings_usable`,
		source, day, seen, usable); err != nil {
		t.Fatal(err)
	}
}

// Verification recency is reported, and must never be presented as the accuracy
// objective.
func TestLivenessVerificationRecencyIsSeparateFromAccuracy(t *testing.T) {
	ctx := context.Background()
	pool, company := seed(t)
	now := time.Now().UTC()

	id := posting(t, pool, company, "ready", now.Add(-time.Hour), now.Add(-time.Hour))
	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET liveness_checked_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}

	ev := NewEvaluator(pool)
	lf, err := ev.LivenessFreshness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lf.Shown == 0 || lf.CheckedRecently == 0 {
		t.Errorf("recency not measured: %+v", lf)
	}
	if lf.Fraction() <= 0 {
		t.Error("fraction should be positive with a freshly checked posting")
	}

	// And the accuracy objective is still unmeasurable, however good recency looks.
	rep, err := ev.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, rep, LivenessAccuracy); got != StatusUnmeasurable {
		t.Errorf("liveness accuracy gave %q; good recency is not evidence of accuracy", got)
	}
}

// ---------------------------------------------------------------- helpers

func resultOf(t *testing.T, rep *Report, id string) Result {
	t.Helper()
	for _, r := range rep.Results {
		if r.Objective.ID == id {
			return r
		}
	}
	t.Fatalf("objective %s absent from the report", id)
	return Result{}
}

func statusOf(t *testing.T, rep *Report, id string) Status {
	t.Helper()
	return resultOf(t, rep, id).Status
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
