DROP INDEX IF EXISTS idx_opp_sweep_cursor;
ALTER TABLE opportunity DROP COLUMN IF EXISTS swept_at;
