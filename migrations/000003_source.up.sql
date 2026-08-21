-- The source registry. tier / legal_basis are NOT NULL because an unvetted
-- source must be impossible to add, not merely discouraged (blueprint §12).
CREATE TABLE source (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name               text NOT NULL UNIQUE,
    -- 'a' sanctioned (ATS/official API/feed), 'b' permitted crawl.
    -- 'c' prohibited is deliberately NOT accepted: it must not be storable.
    tier               char(1) NOT NULL CHECK (tier IN ('a','b')),
    type               text NOT NULL,
    legal_basis        text NOT NULL,
    robots_checked_at  timestamptz,
    terms_reviewed_at  timestamptz,
    reviewed_by        text,
    rate_limit_rps     numeric(6,2) NOT NULL DEFAULT 1.0,
    poll_interval      interval NOT NULL,
    etag_supported     boolean NOT NULL DEFAULT false,
    status             text NOT NULL DEFAULT 'active'
                         CHECK (status IN ('active','quarantined','retired')),
    last_success_at    timestamptz,
    last_failure_at    timestamptz,
    last_error         text,
    items_discovered   bigint NOT NULL DEFAULT 0,
    items_processed    bigint NOT NULL DEFAULT 0,
    -- rolling yield. alert on a RELATIVE drop: a source that fell from 98% to
    -- 71% is broken even though nothing errored (blueprint §29).
    parse_yield_7d     numeric(5,4),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    -- Tier B requires a recorded review; Tier A is documented-public by nature.
    CONSTRAINT source_tier_b_reviewed CHECK (
        tier <> 'b' OR (robots_checked_at IS NOT NULL
                        AND terms_reviewed_at IS NOT NULL
                        AND reviewed_by IS NOT NULL)
    )
);

CREATE TRIGGER source_updated_at BEFORE UPDATE ON source
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Schedules are rows, claimed with the same SKIP LOCKED pattern as everything
-- else. There is no scheduler process: one is a SPOF, two double-fire.
CREATE TABLE source_schedule (
    source_id     uuid PRIMARY KEY REFERENCES source(id) ON DELETE CASCADE,
    next_run_at   timestamptz NOT NULL DEFAULT now(),
    lease_until   timestamptz,
    last_run_at   timestamptz,
    cursor        jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_source_schedule_due ON source_schedule (next_run_at)
    WHERE lease_until IS NULL;
