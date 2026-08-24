-- Restore the unconditional index. It answers unfiltered kNN correctly; it is
-- the version-filtered query that degrades to a sequential scan without the
-- partial indexes.
DROP INDEX IF EXISTS idx_opp_emb_hnsw_v1;

CREATE INDEX idx_opp_emb_hnsw ON opportunity_embedding
    USING hnsw (embedding vector_cosine_ops);
