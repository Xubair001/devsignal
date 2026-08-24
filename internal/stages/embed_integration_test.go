//go:build integration

package stages

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/store"
)

const (
	backendText = "Senior Backend Engineer. Design, build and operate Go services backed " +
		"by PostgreSQL serving millions of requests per day. Own reliability end to end " +
		"including on-call, capacity planning and incident review, and mentor backend engineers."
	frontendText = "Senior Frontend Engineer. Build accessible React interfaces and own the " +
		"design system, working with product designers on component architecture, visual " +
		"polish and browser performance across the web application."
	marketingText = "Demand Generation Manager. Own paid search, paid social and lifecycle " +
		"email, manage agency relationships and the media budget, and report pipeline " +
		"contribution to the executive team."
)

func seedForEmbedding(t *testing.T, pool *pgxpool.Pool, texts map[string]string) (pgtype.UUID, map[string]pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Embed Co') RETURNING id`,
		"embed-"+uuid.NewString()[:8]+".example").Scan(&companyID); err != nil {
		t.Fatalf("company: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity_embedding WHERE opportunity_id IN
		  (SELECT id FROM opportunity WHERE company_id=$1)`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})

	ids := map[string]pgtype.UUID{}
	for name, text := range texts {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO opportunity (company_id, title_raw, title_normalized, description_text,
			  pipeline_state) VALUES ($1,$2,$2,$3,'enriched') RETURNING id`,
			companyID, name, text).Scan(&id); err != nil {
			t.Fatalf("opportunity %s: %v", name, err)
		}
		ids[name] = id
	}
	return companyID, ids
}

func runEmbedStage(t *testing.T, pool *pgxpool.Pool, id pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	q := store.New(pool)
	e := NewEmbedder(pool, embed.NewLocal(), quiet())
	row, err := q.GetOpportunityState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Handle(ctx, pipeline.Item{
		ID: id, Version: row.Version, State: pipeline.StateEnriched,
	}); !errors.Is(err, pipeline.ErrHandled) {
		t.Fatalf("handle: got %v, want ErrHandled", err)
	}
}

func TestEmbeddingStageStoresVectorAndVersion(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seedForEmbedding(t, pool, map[string]string{"backend": backendText})
	runEmbedStage(t, pool, ids["backend"])

	q := store.New(pool)
	row, err := q.GetOpportunityEmbedding(ctx, store.GetOpportunityEmbeddingParams{
		OpportunityID: ids["backend"], EmbeddingVersion: embed.LocalVersion,
	})
	if err != nil {
		t.Fatalf("reading embedding: %v", err)
	}
	if row.EmbeddingDim != embed.Dim {
		t.Errorf("dim = %d, want %d", row.EmbeddingDim, embed.Dim)
	}
	// Model and version must be recorded, or a future migration cannot tell which
	// vectors are stale.
	if row.EmbeddingModel != embed.LocalModelID || row.EmbeddingVersion != embed.LocalVersion {
		t.Errorf("identity not recorded: model=%q version=%q", row.EmbeddingModel, row.EmbeddingVersion)
	}
	state, _ := q.GetOpportunityState(ctx, ids["backend"])
	if state.PipelineState != string(pipeline.StateEmbedded) {
		t.Errorf("state = %q, want embedded", state.PipelineState)
	}
}

// The property retrieval is built on: nearest-neighbour order must be sensible.
func TestNearestNeighbourOrderingIsUseful(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seedForEmbedding(t, pool, map[string]string{
		"backend":   backendText,
		"frontend":  frontendText,
		"marketing": marketingText,
	})
	for _, id := range ids {
		runEmbedStage(t, pool, id)
	}
	// Only 'ready' rows are searchable, which is itself correct — but it means the
	// fixtures must be promoted before querying.
	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET pipeline_state='ready' WHERE id = ANY($1)`,
		[]pgtype.UUID{ids["backend"], ids["frontend"], ids["marketing"]}); err != nil {
		t.Fatal(err)
	}

	// Query with a backend-flavoured vector.
	qv, err := embed.NewLocal().Embed(ctx, backendText)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.New(pool).NearestOpportunities(ctx, store.NearestOpportunitiesParams{
		QueryVector: pgvector.NewVector(qv), EmbeddingVersion: embed.LocalVersion,
		MaxCandidates: 200,
	})
	if err != nil {
		t.Fatalf("knn: %v", err)
	}

	// Find our three fixtures within whatever else the corpus holds.
	pos := map[string]int{}
	for i, r := range rows {
		for name, id := range ids {
			if r.ID == id {
				pos[name] = i
			}
		}
	}
	for _, name := range []string{"backend", "frontend", "marketing"} {
		if _, ok := pos[name]; !ok {
			t.Fatalf("%s fixture was not returned by the search at all", name)
		}
	}
	if pos["backend"] > pos["frontend"] || pos["frontend"] > pos["marketing"] {
		t.Errorf("ordering wrong: backend=%d frontend=%d marketing=%d (want ascending)",
			pos["backend"], pos["frontend"], pos["marketing"])
	}
}

// A version filter is not optional: mixing vectors from two models makes every
// distance meaningless.
func TestSearchIsScopedToOneEmbeddingVersion(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seedForEmbedding(t, pool, map[string]string{"backend": backendText})
	runEmbedStage(t, pool, ids["backend"])
	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET pipeline_state='ready' WHERE id=$1`, ids["backend"]); err != nil {
		t.Fatal(err)
	}

	qv, _ := embed.NewLocal().Embed(ctx, backendText)
	q := store.New(pool)

	// A version nobody has written must return nothing, not fall back.
	rows, err := q.NearestOpportunities(ctx, store.NearestOpportunitiesParams{
		QueryVector: pgvector.NewVector(qv), EmbeddingVersion: "v-does-not-exist",
		MaxCandidates: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == ids["backend"] {
			t.Fatal("a vector from another version was returned; distances would be meaningless")
		}
	}
}

// The dual-write migration path: both versions coexist, the backfill is
// observable, and only then is the old version dropped.
func TestDualWriteMigrationPathWorks(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seedForEmbedding(t, pool, map[string]string{"backend": backendText})
	id := ids["backend"]
	runEmbedStage(t, pool, id)

	q := store.New(pool)
	const newVersion = "v2-test"
	vec, _ := embed.NewLocal().Embed(ctx, backendText)

	// Write the new version alongside the old.
	if err := q.PutOpportunityEmbedding(ctx, store.PutOpportunityEmbeddingParams{
		OpportunityID: id, EmbeddingModel: "some-hosted-model",
		EmbeddingVersion: newVersion, EmbeddingDim: int32(len(vec)),
		Embedding: pgvector.NewVector(vec),
	}); err != nil {
		t.Fatalf("dual write: %v", err)
	}
	t.Cleanup(func() {
		_, _ = q.DeleteEmbeddingVersion(context.Background(), newVersion)
	})

	// Both must be present and independently readable.
	for _, v := range []string{embed.LocalVersion, newVersion} {
		if _, err := q.GetOpportunityEmbedding(ctx, store.GetOpportunityEmbeddingParams{
			OpportunityID: id, EmbeddingVersion: v,
		}); err != nil {
			t.Errorf("version %q not readable during dual write: %v", v, err)
		}
	}

	// The backfill must be observable, or there is no way to know it finished.
	counts, err := q.CountEmbeddingsByVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range counts {
		seen[c.EmbeddingVersion] = true
	}
	if !seen[embed.LocalVersion] || !seen[newVersion] {
		t.Errorf("both versions must appear in the migration view, saw %v", seen)
	}

	// Completing the migration drops the old vectors.
	if n, err := q.DeleteEmbeddingVersion(ctx, newVersion); err != nil || n == 0 {
		t.Errorf("dropping a version removed %d rows, err=%v", n, err)
	}
	if _, err := q.GetOpportunityEmbedding(ctx, store.GetOpportunityEmbeddingParams{
		OpportunityID: id, EmbeddingVersion: embed.LocalVersion,
	}); err != nil {
		t.Error("dropping the new version also removed the old one")
	}
}

// Re-embedding must be idempotent: a retry after a crash costs a re-embed and
// nothing else.
func TestReEmbeddingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seedForEmbedding(t, pool, map[string]string{"backend": backendText})
	id := ids["backend"]

	runEmbedStage(t, pool, id)
	// Put it back and run again, as a sweeper-driven retry would.
	if _, err := pool.Exec(ctx,
		`UPDATE opportunity SET pipeline_state='enriched' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	runEmbedStage(t, pool, id)

	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity_embedding WHERE opportunity_id=$1`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d embedding rows after a retry, want 1", rows)
	}
}

// The backfill driver must find rows lacking the current version — that is how a
// migration and an outage recovery are both driven.
func TestBackfillFindsRowsMissingTheCurrentVersion(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	_, ids := seedForEmbedding(t, pool, map[string]string{"backend": backendText})
	id := ids["backend"]

	q := store.New(pool)
	missing, err := q.OpportunitiesMissingEmbedding(ctx, store.OpportunitiesMissingEmbeddingParams{
		EmbeddingVersion: embed.LocalVersion, Batch: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	var foundBefore bool
	for _, m := range missing {
		if m == id {
			foundBefore = true
		}
	}
	if !foundBefore {
		t.Fatal("an un-embedded row was not reported as missing")
	}

	runEmbedStage(t, pool, id)

	missing, err = q.OpportunitiesMissingEmbedding(ctx, store.OpportunitiesMissingEmbeddingParams{
		EmbeddingVersion: embed.LocalVersion, Batch: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range missing {
		if m == id {
			t.Error("an embedded row is still reported as missing")
		}
	}
}

// Every version that has vectors must have an HNSW index that covers it.
//
// This is the guard for the trap that migration 000018 removed. A version filter
// cannot be served by an unconditional HNSW index, so writing vectors under a
// new version without adding its partial index silently turns every retrieval
// query into a sequential scan — and, if the planner does reach for an
// unconditional index, into one that can return fewer rows than asked for.
//
// The invariant is index existence, not query plan shape. A test-sized table is
// small enough that the planner will correctly prefer an exact scan over any
// vector index, so asserting on EXPLAIN output here would assert the opposite of
// what production does.
func TestEveryLiveEmbeddingVersionHasAPartialIndex(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	// Predicates of the HNSW indexes on the table, as Postgres renders them.
	rows, err := pool.Query(ctx, `
		SELECT coalesce(pg_get_expr(i.indpred, i.indrelid), '')
		  FROM pg_index i
		  JOIN pg_class c ON c.oid = i.indexrelid
		  JOIN pg_class t ON t.oid = i.indrelid
		  JOIN pg_am   am ON am.oid = c.relam
		 WHERE t.relname = 'opportunity_embedding' AND am.amname = 'hnsw'`)
	if err != nil {
		t.Fatal(err)
	}
	var predicates []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		predicates = append(predicates, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(predicates) == 0 {
		t.Fatal("no HNSW index on opportunity_embedding: kNN retrieval has no index at all")
	}

	// The version the code writes today must be covered even before any row
	// exists, so a rollout is caught at deploy time rather than first query.
	versions := map[string]bool{embed.LocalVersion: true}
	vrows, err := pool.Query(ctx, `SELECT DISTINCT embedding_version FROM opportunity_embedding`)
	if err != nil {
		t.Fatal(err)
	}
	for vrows.Next() {
		var v string
		if err := vrows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions[v] = true
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		t.Fatal(err)
	}

	for v := range versions {
		// Test fixtures write throwaway versions; only shipped ones need an index.
		if strings.Contains(v, "test") || strings.Contains(v, "probe") {
			continue
		}
		want := "embedding_version = '" + v + "'::text"
		var covered bool
		for _, p := range predicates {
			// An unconditional index (empty predicate) does not count: it cannot
			// serve the filter, which is the whole point of this test.
			if p != "" && strings.Contains(p, want) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("embedding version %q has vectors but no partial HNSW index covers it; "+
				"add one in a migration alongside the version bump (see 000018). predicates=%v",
				v, predicates)
		}
	}
}
