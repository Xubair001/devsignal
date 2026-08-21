-- Low-confidence pairs are NOT merged automatically. They are recorded for a
-- human, because the error costs are asymmetric: a false merge hides a real job
-- permanently and invisibly, while a duplicate left in place is merely visible
-- and annoying. When the evidence is weak, the safe default is to do nothing and
-- ask.
CREATE TABLE merge_candidate (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    left_opportunity_id  uuid NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,
    right_opportunity_id uuid NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,
    reason              text NOT NULL,
    confidence          real NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    -- Why it was not auto-applied, so a reviewer sees the doubt rather than
    -- guessing at it.
    withheld_because    text NOT NULL,
    resolution          text CHECK (resolution IN ('merged','rejected')),
    resolved_by         text,
    resolved_at         timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT merge_candidate_distinct CHECK (left_opportunity_id <> right_opportunity_id),
    -- One open candidate per pair; re-running the sweep must not queue it twice.
    UNIQUE (left_opportunity_id, right_opportunity_id)
);

CREATE INDEX idx_merge_candidate_open ON merge_candidate (created_at)
    WHERE resolution IS NULL;
