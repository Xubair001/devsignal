DROP INDEX IF EXISTS idx_opp_country;
DROP INDEX IF EXISTS idx_opp_retrieval;
CREATE INDEX idx_opp_retrieval ON opportunity (role_family, seniority_ordinal, work_mode)
    WHERE closed_at IS NULL AND pipeline_state = 'ready';
CREATE INDEX idx_opp_country ON opportunity (location_country)
    WHERE closed_at IS NULL AND pipeline_state = 'ready';
DROP TABLE IF EXISTS opportunity_merge;
DROP INDEX IF EXISTS idx_opp_merged_into;
ALTER TABLE opportunity DROP COLUMN IF EXISTS merged_into;
DROP INDEX IF EXISTS idx_opp_block_key;
ALTER TABLE opportunity DROP COLUMN IF EXISTS block_key;
