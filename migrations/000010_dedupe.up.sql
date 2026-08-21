-- Blocking key, stored so it can be indexed. Dedup without blocking is
-- quadratic: 500K postings is 1.25e11 pairs, which will never run.
ALTER TABLE opportunity ADD COLUMN block_key text;

-- A merged posting is retained, not deleted, so the merge stays reversible.
-- Serving excludes it via merged_into IS NULL.
ALTER TABLE opportunity ADD COLUMN merged_into uuid REFERENCES opportunity(id);

-- Indexes come after BOTH columns exist: a partial index predicate cannot
-- reference a column added later in the same migration.
CREATE INDEX idx_opp_block_key ON opportunity (block_key)
    WHERE block_key IS NOT NULL AND merged_into IS NULL;
CREATE INDEX idx_opp_merged_into ON opportunity (merged_into) WHERE merged_into IS NOT NULL;

-- The merge decision itself. Without this, un-merge is guesswork: we would know
-- two rows are joined but not why, how confidently, or what moved.
CREATE TABLE opportunity_merge (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_opportunity_id uuid NOT NULL REFERENCES opportunity(id),
    into_opportunity_id uuid NOT NULL REFERENCES opportunity(id),
    reason              text NOT NULL CHECK (reason IN
                          ('exact_ats','content_hash','apply_url','simhash','human')),
    confidence          real CHECK (confidence BETWEEN 0 AND 1),
    source_rows_moved   int NOT NULL DEFAULT 0,
    merged_by           text NOT NULL,
    merged_at           timestamptz NOT NULL DEFAULT now(),
    undone_at           timestamptz,
    CONSTRAINT opp_merge_not_self CHECK (from_opportunity_id <> into_opportunity_id)
);

CREATE INDEX idx_opp_merge_from ON opportunity_merge (from_opportunity_id) WHERE undone_at IS NULL;
CREATE INDEX idx_opp_merge_into ON opportunity_merge (into_opportunity_id) WHERE undone_at IS NULL;

-- Rebuild the serving indexes to exclude merged rows: a merged posting must
-- never appear in a feed.
DROP INDEX IF EXISTS idx_opp_retrieval;
DROP INDEX IF EXISTS idx_opp_country;
CREATE INDEX idx_opp_retrieval ON opportunity (role_family, seniority_ordinal, work_mode)
    WHERE closed_at IS NULL AND merged_into IS NULL AND pipeline_state = 'ready';
CREATE INDEX idx_opp_country ON opportunity (location_country)
    WHERE closed_at IS NULL AND merged_into IS NULL AND pipeline_state = 'ready';
