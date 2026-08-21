-- The sweeper cannot key off updated_at.
--
-- updated_at is refreshed by ANY write, so a liveness re-poll touching
-- last_seen_at resets the stranding clock on a record that has actually been
-- stuck in 'parsed' for a week — and the sweeper never sees it. Stranding is
-- time spent in one STATE, not time since the last write of any kind.
ALTER TABLE opportunity ADD COLUMN state_entered_at timestamptz NOT NULL DEFAULT now();

CREATE OR REPLACE FUNCTION set_state_entered_at() RETURNS trigger AS $$
BEGIN
    IF NEW.pipeline_state IS DISTINCT FROM OLD.pipeline_state THEN
        NEW.state_entered_at = now();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER opportunity_state_entered_at BEFORE UPDATE ON opportunity
    FOR EACH ROW EXECUTE FUNCTION set_state_entered_at();

-- Partial index: only non-terminal rows are ever swept, so the index stays small
-- as the ready corpus grows.
CREATE INDEX idx_opp_stranded_state ON opportunity (state_entered_at)
    WHERE pipeline_state NOT IN ('ready','failed_permanent');

-- The old index keyed on updated_at is now misleading; drop it so nobody
-- reintroduces the bug by using it.
DROP INDEX IF EXISTS idx_opp_stranded;
