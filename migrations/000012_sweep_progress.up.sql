-- The sweeper had no progress guarantee.
--
-- SweepStranded ordered by state_entered_at and requeuing does not change that
-- column, so every sweep returned the SAME oldest batch and never advanced to
-- the rest of the backlog. With more stranded rows than the batch size, the tail
-- was starved forever — and because the sweeper is the safety net for the whole
-- pipeline, a starving sweeper is worse than none: it looks like it is working.
--
-- swept_at is a cursor, not a signal: ordering by it NULLS FIRST guarantees every
-- stranded row is visited before any row is revisited.
ALTER TABLE opportunity ADD COLUMN swept_at timestamptz;

CREATE INDEX idx_opp_sweep_cursor ON opportunity (swept_at NULLS FIRST, state_entered_at)
    WHERE pipeline_state NOT IN ('ready','failed_permanent');
