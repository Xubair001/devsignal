COMMENT ON COLUMN opportunity.ghost_risk_score IS NULL;
DROP INDEX IF EXISTS idx_opp_repost;
ALTER TABLE opportunity DROP COLUMN IF EXISTS source_posted_at_at_last_change;
ALTER TABLE opportunity DROP COLUMN IF EXISTS repost_count;
