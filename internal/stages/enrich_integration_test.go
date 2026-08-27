//go:build integration

package stages

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/enrich"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/store"
)

type countingProvider struct {
	calls atomic.Int32
	err   error
}

func (c *countingProvider) ModelID() string { return "stage-fake-model" }

// ExtractWith ignores the task. This fake counts CALLS, which is what the cache
// assertions turn on, and the task does not change that.
func (c *countingProvider) ExtractWith(
	ctx context.Context, _ enrich.Task, text string,
) (enrich.Raw, error) {
	return c.Extract(ctx, text)
}

func (c *countingProvider) Extract(context.Context, string) (enrich.Raw, error) {
	c.calls.Add(1)
	if c.err != nil {
		return enrich.Raw{}, c.err
	}
	body, _ := json.Marshal(enrich.Result{
		Seniority: "senior", RoleFamily: "backend", EmploymentType: "full_time",
		RemotePolicy: "remote",
		Skills: []enrich.Skill{
			{Name: "Go", Level: enrich.LevelRequired},
			{Name: "PostgreSQL", Level: enrich.LevelRequired},
		},
	})
	return enrich.Raw{JSON: body, Model: c.ModelID(),
		Usage: enrich.Usage{InputTokens: 1800, OutputTokens: 400, CacheReadTokens: 800}}, nil
}

const longDesc = "We are hiring a Senior Backend Engineer to build and operate Go services " +
	"backed by PostgreSQL at scale. You will own reliability end to end including on-call, " +
	"capacity planning and incident review, and mentor engineers through design review. " +
	"Benefits include health cover, parental leave and an annual learning stipend."

func seedForEnrichment(t *testing.T, pool *pgxpool.Pool, n int, hash []byte) []pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Enrich Stage Co') RETURNING id`,
		"enrichstage-"+uuid.NewString()[:8]+".example").Scan(&companyID); err != nil {
		t.Fatalf("company: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity_skill WHERE opportunity_id IN
		  (SELECT id FROM opportunity WHERE company_id=$1)`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM extraction WHERE content_hash=$1`, hash)
	})

	var ids []pgtype.UUID
	for i := 0; i < n; i++ {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO opportunity (company_id, title_raw, title_normalized, description_text,
			  content_hash, pipeline_state)
			VALUES ($1,'Senior Backend Engineer','senior backend engineer',$2,$3,'deduped')
			RETURNING id`, companyID, longDesc, hash).Scan(&id); err != nil {
			t.Fatalf("opportunity: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// Many postings sharing content must cost ONE call. This is the cost lever that
// makes re-polling free, and the determinism guarantee that keeps scores stable.
func TestEnrichmentStagePaysOncePerDistinctContent(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := []byte("shared-" + uuid.NewString())
	ids := seedForEnrichment(t, pool, 4, hash)

	fp := &countingProvider{}
	e := NewEnricher(pool, enrich.NewService(pool, fp, quiet()), quiet())
	q := store.New(pool)

	for _, id := range ids {
		row, err := q.GetOpportunityState(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		err = e.Handle(ctx, pipeline.Item{
			ID: id, Version: row.Version, State: pipeline.StateDeduped,
		})
		if !errors.Is(err, pipeline.ErrHandled) {
			t.Fatalf("handle: got %v, want ErrHandled", err)
		}
	}

	if got := fp.calls.Load(); got != 1 {
		t.Errorf("provider called %d times for 4 postings with identical content, want 1", got)
	}

	// All four advanced, and all four got skills.
	for _, id := range ids {
		row, err := q.GetOpportunityState(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.PipelineState != string(pipeline.StateEnriched) {
			t.Errorf("state = %q, want enriched", row.PipelineState)
		}
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM opportunity_skill WHERE opportunity_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("%d skills attached, want 2", n)
		}
	}
}

// A posting too short to extract from must advance, not fail: a stub is not an
// error, and calling the model on it would cache an empty result.
func TestShortDescriptionAdvancesWithoutPaying(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := []byte("short-" + uuid.NewString())
	ids := seedForEnrichment(t, pool, 1, hash)

	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET description_text='too short' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}

	fp := &countingProvider{}
	e := NewEnricher(pool, enrich.NewService(pool, fp, quiet()), quiet())
	q := store.New(pool)
	row, _ := q.GetOpportunityState(ctx, ids[0])

	if err := e.Handle(ctx, pipeline.Item{
		ID: ids[0], Version: row.Version, State: pipeline.StateDeduped,
	}); !errors.Is(err, pipeline.ErrHandled) {
		t.Fatalf("handle: got %v, want ErrHandled", err)
	}
	if fp.calls.Load() != 0 {
		t.Error("paid for a model call on a stub posting")
	}
	after, _ := q.GetOpportunityState(ctx, ids[0])
	if after.PipelineState != string(pipeline.StateEnriched) {
		t.Errorf("state = %q, want enriched: a stub must not block the pipeline", after.PipelineState)
	}
}

// The rule that matters most: a permanently unavailable model must NOT stop
// postings becoming visible. 'deduped' is degradable for exactly this reason.
func TestUnavailableModelStillLetsPostingsReachReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := testPool(t)
	hash := []byte("down-" + uuid.NewString())
	ids := seedForEnrichment(t, pool, 1, hash)

	fp := &countingProvider{err: errors.New("model unavailable")}
	e := NewEnricher(pool, enrich.NewService(pool, fp, quiet()), quiet())

	// Short lease and few attempts so the degrade path is reached quickly.
	pc := pipeline.DefaultConfig()
	pc.MaxAttempts = 2
	pc.BatchSize = 10
	pc.Lease = 2 * time.Second
	pc.BaseBackoff = 10 * time.Millisecond
	pc.MaxBackoff = 50 * time.Millisecond
	q := pipeline.NewQueue(pool, pc, quiet())
	w := pipeline.NewWorker(q, pipeline.Stage{
		State: pipeline.StateDeduped, Concurrency: 1,
		PollInterval: 50 * time.Millisecond, Handle: e.Handle,
	}, quiet())

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = w.Run(runCtx); close(done) }()

	st := store.New(pool)
	deadline := time.Now().Add(20 * time.Second)
	var final string
	for time.Now().Before(deadline) {
		row, err := st.GetOpportunityState(ctx, ids[0])
		if err == nil {
			final = row.PipelineState
			if final == string(pipeline.StateEnriched) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	<-done

	if final != string(pipeline.StateEnriched) {
		t.Fatalf("state = %q: a failing model must degrade to visible, not park in "+
			"failed_permanent", final)
	}
	if fp.calls.Load() < 2 {
		t.Errorf("provider called %d times; expected retries before degrading", fp.calls.Load())
	}
}

// Spend has to be reportable: this is the only per-token component.
func TestSpendIsReportable(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := []byte("spend-" + uuid.NewString())
	ids := seedForEnrichment(t, pool, 1, hash)

	fp := &countingProvider{}
	e := NewEnricher(pool, enrich.NewService(pool, fp, quiet()), quiet())
	q := store.New(pool)
	row, _ := q.GetOpportunityState(ctx, ids[0])
	_ = e.Handle(ctx, pipeline.Item{ID: ids[0], Version: row.Version, State: pipeline.StateDeduped})

	rows, err := q.ExtractionSpendReport(ctx)
	if err != nil {
		t.Fatalf("spend report: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.ModelID == "stage-fake-model" {
			found = true
			if r.InputTokens == 0 || r.OutputTokens == 0 {
				t.Errorf("token totals not accumulated: %+v", r)
			}
		}
	}
	if !found {
		t.Error("the model's spend does not appear in the report")
	}
}

// A systemic fault — no credentials, provider down — must NOT consume a
// posting's retry budget. That budget exists to give up on individually-bad
// records; a systemic fault fails identically for every one of them, so spending
// it there just multiplies noise and delays recovery once the cause is fixed.
func TestSystemicProviderFaultDoesNotBurnAttempts(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := []byte("systemic-" + uuid.NewString())
	ids := seedForEnrichment(t, pool, 1, hash)
	id := ids[0]

	// The exact shape the SDK produces with no key configured.
	fp := &countingProvider{err: errors.New(
		"no Anthropic credentials found. The SDK tried these sources in order: " +
			"1. ANTHROPIC_API_KEY env var: not set")}
	e := NewEnricher(pool, enrich.NewService(pool, fp, quiet()), quiet())
	q := store.New(pool)

	row, err := q.GetOpportunityState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	handleErr := e.Handle(ctx, pipeline.Item{
		ID: id, Version: row.Version, State: pipeline.StateDeduped, Attempts: row.Attempts,
	})
	if !errors.Is(handleErr, pipeline.ErrRetryLater) {
		t.Fatalf("got %v, want ErrRetryLater: a credential fault is systemic", handleErr)
	}
	if !errors.Is(handleErr, enrich.ErrProviderUnavailable) {
		t.Error("the underlying cause should still be inspectable")
	}

	// And it must stay in a retryable state rather than parking.
	after, err := q.GetOpportunityState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.PipelineState != string(pipeline.StateDeduped) {
		t.Errorf("state = %q, want deduped: a systemic fault must not advance or park",
			after.PipelineState)
	}
}

// A document-specific fault is the opposite case: it IS this record's fault, so
// it consumes the budget and eventually degrades.
func TestDocumentFaultIsNotTreatedAsSystemic(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := []byte("docfault-" + uuid.NewString())
	ids := seedForEnrichment(t, pool, 1, hash)

	fp := &countingProvider{err: enrich.ErrInvalidOutput}
	e := NewEnricher(pool, enrich.NewService(pool, fp, quiet()), quiet())
	q := store.New(pool)
	row, _ := q.GetOpportunityState(ctx, ids[0])

	err := e.Handle(ctx, pipeline.Item{
		ID: ids[0], Version: row.Version, State: pipeline.StateDeduped,
	})
	if errors.Is(err, pipeline.ErrRetryLater) {
		t.Error("an unparseable document was misclassified as a systemic fault; it would " +
			"never give up")
	}
	if !errors.Is(err, enrich.ErrInvalidOutput) {
		t.Errorf("got %v, want ErrInvalidOutput", err)
	}
}
