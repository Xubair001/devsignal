-- Per-source, per-day history.
--
-- A single current parse_yield cannot answer the question that matters. Parser
-- rot does not error: the parser keeps returning rows, with fields quietly
-- empty. Success counters stay green while match quality degrades for weeks. The
-- only way to see it is a RELATIVE drop against this source's own recent past,
-- so the past has to be recorded.
CREATE TABLE source_health_daily (
    source_id       uuid NOT NULL REFERENCES source(id) ON DELETE CASCADE,
    day             date NOT NULL,

    polls           integer NOT NULL DEFAULT 0,
    poll_failures   integer NOT NULL DEFAULT 0,
    -- A 304 is a SUCCESSFUL poll. Counted separately so it neither inflates
    -- throughput nor looks like a failure.
    not_modified    integer NOT NULL DEFAULT 0,

    postings_seen   integer NOT NULL DEFAULT 0,
    postings_usable integer NOT NULL DEFAULT 0,

    -- Field fill counts. These are the parser-rot signal: a source that went
    -- from filling location on 98% of postings to 71% is broken even though
    -- nothing errored and every row still arrived.
    with_company    integer NOT NULL DEFAULT 0,
    with_location   integer NOT NULL DEFAULT 0,
    with_apply_url  integer NOT NULL DEFAULT 0,
    with_language   integer NOT NULL DEFAULT 0,
    with_salary     integer NOT NULL DEFAULT 0,

    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_id, day)
);

CREATE INDEX idx_source_health_day ON source_health_daily (day DESC, source_id);

-- Quarantine requires SUSTAINED degradation, not one bad poll. A transient blip
-- must not take a source offline, and a source taken offline by mistake is a
-- silent loss of corpus coverage.
ALTER TABLE source ADD COLUMN consecutive_degraded integer NOT NULL DEFAULT 0;
ALTER TABLE source ADD COLUMN last_health_note text;

-- The reviewable unit for Tier A is the ATS PLATFORM, not each company board:
-- every board on Greenhouse is the same documented public endpoint pattern, so
-- reviewing one board's terms tells you nothing the platform review did not.
-- Recording which platform review a source inherits keeps bulk registration
-- honest rather than rubber-stamped.
ALTER TABLE source ADD COLUMN platform_review_ref text;
