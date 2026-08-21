-- Merged rows have left the pipeline. They are excluded from claiming and
-- sweeping in SQL; the index predicate must match, or the partial index stops
-- being used and every sweep degrades to a scan.
DROP INDEX IF EXISTS idx_opp_sweep_cursor;
CREATE INDEX idx_opp_sweep_cursor ON opportunity (swept_at NULLS FIRST, state_entered_at)
    WHERE pipeline_state NOT IN ('ready','failed_permanent') AND merged_into IS NULL;

DROP INDEX IF EXISTS idx_opp_claim;
CREATE INDEX idx_opp_claim ON opportunity (pipeline_state, next_attempt_at)
    WHERE pipeline_state <> 'ready' AND merged_into IS NULL;
