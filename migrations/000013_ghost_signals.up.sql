-- Repost detection: the strongest observable ghost signal (blueprint §16).
--
-- When a source re-advertises byte-identical content but moves its own
-- posted-at forward, the listing is being refreshed to look fresh rather than
-- genuinely reposted. Counting that is cheap and, unlike anything inferred from
-- description text, it is directly observed.
ALTER TABLE opportunity ADD COLUMN repost_count integer NOT NULL DEFAULT 0;

-- Their claimed posted-at at the moment we last saw the content change. Needed
-- to tell "they refreshed the date" from "the content actually changed".
ALTER TABLE opportunity ADD COLUMN source_posted_at_at_last_change timestamptz;

CREATE INDEX idx_opp_repost ON opportunity (repost_count)
    WHERE repost_count > 0 AND merged_into IS NULL;

-- Ghost risk is VOLATILE: it depends on how long a posting has been open, so it
-- changes every day without anything about the posting changing. It is therefore
-- computed at read time from stored observable inputs, exactly like
-- priority_score and for the same reason (blueprint §20).
--
-- This column is reserved for a periodic materialized snapshot, needed later so
-- ranking can filter on it without a read-time computation. It is deliberately
-- left NULL until that job exists rather than being half-populated.
COMMENT ON COLUMN opportunity.ghost_risk_score IS
  'Reserved for a periodic snapshot. Ghost risk is volatile and is computed at read time from repost_count, first_seen_at, last_seen_at and apply method. NULL means no snapshot has been taken, never "no risk".';
