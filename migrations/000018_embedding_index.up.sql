-- Replace the unconditional HNSW index with one partial index per live
-- embedding version.
--
-- Every retrieval query filters by embedding_version, and it must: vectors from
-- two different models occupy unrelated spaces, so a cosine distance across
-- versions is arithmetic without meaning. The unconditional index cannot serve
-- that filter, which costs correctness and speed:
--
--   * Speed. Measured on 50k vectors where the queried version held 1,000 of
--     them — the shape of every version rollout, when the new version is still
--     a small slice of the table. The planner abandoned the index and ran a
--     sequential scan over all 50k rows: 12.6 ms, growing linearly with the
--     table. The partial index answered the same query in 0.99 ms.
--   * Correctness. HNSW walks the graph and returns ef_search candidates, and a
--     filter outside the index is applied after that. Asking for 20 can return
--     fewer than 20 even when far more matching rows exist. Every row in a
--     partial index satisfies the predicate, so the count asked for is the
--     count returned.
--
-- The cost is that each new embedding version needs its own index, created here
-- as part of the rollout. That is the same expand/contract discipline the rest
-- of the schema follows, and it is enforced by
-- TestEveryLiveEmbeddingVersionHasAPartialIndex, which fails if vectors are
-- written for a version no index covers.
--
-- Not CONCURRENTLY: golang-migrate wraps each migration in a transaction, and
-- CREATE INDEX CONCURRENTLY cannot run inside one. At the volumes here the table
-- lock is brief; a rollout against a large live table should instead create the
-- next version's index out-of-band with CONCURRENTLY, then record it here.
DROP INDEX IF EXISTS idx_opp_emb_hnsw;

CREATE INDEX idx_opp_emb_hnsw_v1 ON opportunity_embedding
    USING hnsw (embedding vector_cosine_ops)
    WHERE embedding_version = 'v1';
