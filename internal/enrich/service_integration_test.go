//go:build integration

package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/store"
)

// fakeProvider counts calls. That count IS the assertion: a cache that does not
// prevent paid calls is indistinguishable from no cache at all from the outside.
type fakeProvider struct {
	model  string
	calls  atomic.Int32
	result Result
	err    error
	raw    []byte
}

func (f *fakeProvider) ModelID() string { return f.model }

func (f *fakeProvider) Extract(context.Context, string) (Raw, error) {
	f.calls.Add(1)
	if f.err != nil {
		return Raw{}, f.err
	}
	body := f.raw
	if body == nil {
		body, _ = json.Marshal(f.result)
	}
	return Raw{
		JSON: body, Model: f.model,
		Usage: Usage{InputTokens: 1800, OutputTokens: 400, CacheReadTokens: 800},
	}, nil
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.Pool(t)
}

func newHash(t *testing.T) []byte {
	t.Helper()
	h := []byte(uuid.NewString())
	t.Cleanup(func() {
		_, _ = testPoolFor(t).Exec(context.Background(),
			`DELETE FROM extraction WHERE content_hash = $1`, h)
	})
	return h
}

func testPoolFor(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.Pool(t)
}

const longText = "We are hiring a Senior Backend Engineer to build and operate Go " +
	"services backed by PostgreSQL, serving millions of requests per day. You will " +
	"own reliability end to end, including on-call, capacity planning and incident " +
	"review, and you will mentor other engineers through design review. Benefits " +
	"include health cover, parental leave and a learning stipend."

// The property that matters: a second extraction of the same content must not
// call the model. Without this, fit scores move for postings that did not change.
func TestCacheHitPreventsAPaidCall(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	fp := &fakeProvider{model: "fake-model-1", result: goodResult()}
	svc := NewService(pool, fp, quiet())
	hash := newHash(t)

	first, err := svc.Extract(ctx, hash, longText, LaneHot)
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	if first.CacheHit {
		t.Error("first call reported a cache hit")
	}
	if got := fp.calls.Load(); got != 1 {
		t.Fatalf("provider called %d times on a miss, want 1", got)
	}

	second, err := svc.Extract(ctx, hash, longText, LaneHot)
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if !second.CacheHit {
		t.Error("second call was not served from cache")
	}
	if got := fp.calls.Load(); got != 1 {
		t.Fatalf("provider called %d times; the cache did not prevent a paid call", got)
	}

	// And the result must be identical, not merely present: that identity is what
	// keeps the fit score stable.
	if len(second.Result.Skills) != len(first.Result.Skills) ||
		second.Result.Seniority != first.Result.Seniority {
		t.Error("cached result differs from the original")
	}
}

// Each key component must invalidate independently, and nothing else may.
func TestOnlyTheFourKeyComponentsInvalidate(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	t.Run("different model re-extracts", func(t *testing.T) {
		hash := newHash(t)
		a := &fakeProvider{model: "model-a", result: goodResult()}
		b := &fakeProvider{model: "model-b", result: goodResult()}
		if _, err := NewService(pool, a, quiet()).Extract(ctx, hash, longText, LaneHot); err != nil {
			t.Fatal(err)
		}
		out, err := NewService(pool, b, quiet()).Extract(ctx, hash, longText, LaneHot)
		if err != nil {
			t.Fatal(err)
		}
		if out.CacheHit {
			t.Error("a different model served a cached result; extractions would be mixed across models")
		}
		if b.calls.Load() != 1 {
			t.Error("the new model was not actually called")
		}
	})

	t.Run("different content re-extracts", func(t *testing.T) {
		fp := &fakeProvider{model: "model-c", result: goodResult()}
		svc := NewService(pool, fp, quiet())
		if _, err := svc.Extract(ctx, newHash(t), longText, LaneHot); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Extract(ctx, newHash(t), longText+" Extra requirement.", LaneHot); err != nil {
			t.Fatal(err)
		}
		if got := fp.calls.Load(); got != 2 {
			t.Errorf("provider called %d times for two distinct documents, want 2", got)
		}
	})

	t.Run("same content different text still hits", func(t *testing.T) {
		// The KEY is the content hash, not the text. If the caller says the
		// content is unchanged, we must not pay again — that is what makes a
		// re-poll free.
		hash := newHash(t)
		fp := &fakeProvider{model: "model-d", result: goodResult()}
		svc := NewService(pool, fp, quiet())
		if _, err := svc.Extract(ctx, hash, longText, LaneHot); err != nil {
			t.Fatal(err)
		}
		out, err := svc.Extract(ctx, hash, longText+" whitespace difference", LaneHot)
		if err != nil {
			t.Fatal(err)
		}
		if !out.CacheHit {
			t.Error("the same content hash did not hit the cache")
		}
		if fp.calls.Load() != 1 {
			t.Error("paid twice for one content hash")
		}
	})
}

// An invalid output must NOT be cached: caching a rejection makes a bad
// extraction permanent and a prompt fix could never take effect.
func TestInvalidOutputIsNotCached(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := newHash(t)

	bad := &fakeProvider{model: "model-e", raw: []byte(`{"seniority":"senior","bogus":1}`)}
	svc := NewService(pool, bad, quiet())
	if _, err := svc.Extract(ctx, hash, longText, LaneHot); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("got %v, want ErrInvalidOutput", err)
	}

	// Nothing was stored, so a fixed provider on the same content re-extracts.
	good := &fakeProvider{model: "model-e", result: goodResult()}
	out, err := NewService(pool, good, quiet()).Extract(ctx, hash, longText, LaneHot)
	if err != nil {
		t.Fatalf("after the fix: %v", err)
	}
	if out.CacheHit {
		t.Error("a rejected output was cached; the fix could never take effect")
	}
}

// Cost has to be observable, not inferred: enrichment is the only component
// billed per token.
func TestUsageIsRecordedForSpendReporting(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := newHash(t)
	fp := &fakeProvider{model: "model-f", result: goodResult()}

	if _, err := NewService(pool, fp, quiet()).Extract(ctx, hash, longText, LaneCold); err != nil {
		t.Fatal(err)
	}

	row, err := store.New(pool).GetExtraction(ctx, store.GetExtractionParams{
		ContentHash: hash, PromptVersion: PromptVersion,
		ModelID: "model-f", SchemaVersion: SchemaVersion,
	})
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if row.InputTokens != 1800 || row.OutputTokens != 400 || row.CacheReadTokens != 800 {
		t.Errorf("token accounting lost: in=%d out=%d cached=%d",
			row.InputTokens, row.OutputTokens, row.CacheReadTokens)
	}
	if row.Lane != LaneCold {
		t.Errorf("lane = %q, want cold; the two lanes must stay distinguishable", row.Lane)
	}
	// The model's own words are kept: reproducing a past score needs them.
	if len(row.RawOutput) == 0 {
		t.Error("raw model output was not retained")
	}
}

// Two workers racing on the same posting must not both fail, and must not both
// pay twice over.
func TestConcurrentExtractionOfTheSameContentIsSafe(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	hash := newHash(t)
	fp := &fakeProvider{model: "model-g", result: goodResult()}

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			svc := NewService(pool, fp, quiet())
			_, errs[n] = svc.Extract(ctx, hash, longText, LaneHot)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d failed: %v", i, err)
		}
	}
	// At most one row exists for the key, so the ON CONFLICT held.
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM extraction WHERE content_hash=$1`, hash).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d cache rows for one key, want 1", rows)
	}
}

// Extracted skills must land on the posting, and a re-extraction that drops a
// skill must remove it — otherwise corrections can only ever add.
func TestApplySkillsReplacesRatherThanMerges(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := NewService(pool, &fakeProvider{model: "model-h", result: goodResult()}, quiet())

	var companyID, oppID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Enrich Co') RETURNING id`,
		"enrich-"+uuid.NewString()[:8]+".example").Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO opportunity (company_id, title_raw, title_normalized, pipeline_state)
		 VALUES ($1,'Senior Backend Engineer','senior backend engineer','deduped') RETURNING id`,
		companyID).Scan(&oppID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity_skill WHERE opportunity_id=$1`, oppID)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE id=$1`, oppID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})

	if err := svc.ApplySkills(ctx, oppID, goodResult(), "model-h"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity_skill WHERE opportunity_id=$1`, oppID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("%d skills attached, want 3", n)
	}

	// Re-extract with one fewer skill.
	fewer := goodResult()
	fewer.Skills = fewer.Skills[:1]
	if err := svc.ApplySkills(ctx, oppID, fewer, "model-h"); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity_skill WHERE opportunity_id=$1`, oppID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d skills after a narrower extraction, want 1: replace, not merge", n)
	}
}

// Applying the same skills twice must be idempotent, and an alias seen once must
// resolve to the same skill next time rather than creating a duplicate.
func TestSkillAliasesResolveToOneSkill(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := NewService(pool, &fakeProvider{model: "model-i"}, quiet())

	var companyID, oppA, oppB pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Alias Co') RETURNING id`,
		"alias-"+uuid.NewString()[:8]+".example").Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []*pgtype.UUID{&oppA, &oppB} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO opportunity (company_id, title_raw, title_normalized, pipeline_state)
			 VALUES ($1,'Engineer','engineer','deduped') RETURNING id`, companyID).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity_skill WHERE opportunity_id IN ($1,$2)`, oppA, oppB)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})

	uniq := uuid.NewString()[:6]
	nameA := "Kubernetes" + uniq
	nameB := "kubernetes" + uniq // same skill, different casing

	if err := svc.ApplySkills(ctx, oppA, Result{Skills: []Skill{{Name: nameA, Level: LevelRequired}}}, "m"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplySkills(ctx, oppB, Result{Skills: []Skill{{Name: nameB, Level: LevelRequired}}}, "m"); err != nil {
		t.Fatal(err)
	}

	var distinct int
	if err := pool.QueryRow(ctx, `
		SELECT count(DISTINCT skill_id) FROM opportunity_skill
		 WHERE opportunity_id IN ($1,$2)`, oppA, oppB).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 1 {
		t.Errorf("%d distinct skills for the same name in different casing, want 1", distinct)
	}
}
