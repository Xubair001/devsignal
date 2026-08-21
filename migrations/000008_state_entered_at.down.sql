CREATE INDEX idx_opp_stranded ON opportunity (pipeline_state, updated_at)
    WHERE pipeline_state NOT IN ('ready','failed_permanent');
DROP INDEX IF EXISTS idx_opp_stranded_state;
DROP TRIGGER IF EXISTS opportunity_state_entered_at ON opportunity;
DROP FUNCTION IF EXISTS set_state_entered_at();
ALTER TABLE opportunity DROP COLUMN IF EXISTS state_entered_at;
