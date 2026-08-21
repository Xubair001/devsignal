-- The canonical opportunity. Three principles (blueprint §7):
--   1. trust our own observations over what the source claims
--   2. keep provenance separable so merges stay reversible
--   3. version everything a score depends on
--
-- NOTE ON GEOGRAPHY: the blueprint sketches `location_point geography`, which
-- needs PostGIS. pgvector/pgvector:pg17 does not ship PostGIS, and the MVP has
-- no distance search — the constraint that actually matters is timezone/geo
-- SCOPE, not proximity. Storing lat/lon as double precision keeps the door open;
-- adopting PostGIS is a triggered decision, not a default.
CREATE TABLE opportunity (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- NULL = the global corpus, which is the normal case. Present only for
    -- tenant-private postings later. Carried from day one so org scoping is a
    -- policy addition rather than a migration across every table.
    tenant_id     uuid,
    company_id    uuid NOT NULL REFERENCES company(id),

    title_raw          text NOT NULL,
    title_normalized   text NOT NULL,
    role_family        text,
    seniority_ordinal  smallint CHECK (seniority_ordinal BETWEEN 0 AND 9),

    description_text       text,
    description_html_key   text,          -- large bodies live in object storage

    employment_type    text,
    work_mode          text CHECK (work_mode IN ('onsite','hybrid','remote')),
    -- "remote, but US timezones only" is the single most common mismatch and
    -- the fastest route to user rage. It needs its own fields.
    remote_geo_scope       text,
    remote_timezone_min    smallint CHECK (remote_timezone_min BETWEEN -12 AND 14),
    remote_timezone_max    smallint CHECK (remote_timezone_max BETWEEN -12 AND 14),

    location_country   char(2),
    location_region    text,
    location_city      text,
    location_lat       double precision CHECK (location_lat BETWEEN -90 AND 90),
    location_lon       double precision CHECK (location_lon BETWEEN -180 AND 180),
    location_timezone  text,

    language           char(2),

    -- Money: integer minor units only. A float eventually shows a user
    -- $119,999.99, and one column cannot hold a range.
    salary_min_minor     bigint,
    salary_max_minor     bigint,
    salary_currency      char(3),
    salary_period        text CHECK (salary_period IN ('year','month','week','day','hour')),
    salary_is_estimated  boolean NOT NULL DEFAULT false,
    fx_rate_date         date,

    -- Tri-state. Collapsing unknown to false silently filters out good matches,
    -- and unknown is the common case.
    visa_sponsorship  text NOT NULL DEFAULT 'unknown'
                        CHECK (visa_sponsorship IN ('unknown','yes','no')),

    apply_method  text,
    ats_type      text,

    -- Liveness, derived from observation rather than from claims.
    first_seen_at              timestamptz NOT NULL DEFAULT now(),  -- ours: the only trustworthy age
    last_seen_at               timestamptz NOT NULL DEFAULT now(),
    source_reported_posted_at  timestamptz,                         -- theirs: display only, never scored
    closed_at                  timestamptz,
    close_reason               text CHECK (close_reason IN ('absent','expired','source_purged')),
    consecutive_misses         integer NOT NULL DEFAULT 0,
    liveness_checked_at        timestamptz,

    -- Pipeline state machine. The DB row is the truth; events are a latency
    -- optimization. A lost event costs seconds, never data.
    pipeline_state   text NOT NULL DEFAULT 'discovered' CHECK (pipeline_state IN (
        'discovered','fetched','parsed','normalized','deduped',
        'enriched','embedded','ready','failed_permanent')),
    attempts         integer NOT NULL DEFAULT 0,
    last_error       text,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    lease_until      timestamptz,

    -- Optimistic concurrency. Also the opportunity_version in the fit-score
    -- cache key, so bumping it correctly is what invalidates stale scores.
    version   integer NOT NULL DEFAULT 0,

    quality_score     real CHECK (quality_score BETWEEN 0 AND 1),
    ghost_risk_score  real CHECK (ghost_risk_score BETWEEN 0 AND 1),

    content_hash  bytea,
    simhash       bigint,

    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT opp_salary_range CHECK (
        salary_min_minor IS NULL OR salary_max_minor IS NULL
        OR salary_max_minor >= salary_min_minor),
    CONSTRAINT opp_salary_needs_currency CHECK (
        (salary_min_minor IS NULL AND salary_max_minor IS NULL)
        OR salary_currency IS NOT NULL),
    CONSTRAINT opp_tz_range CHECK (
        remote_timezone_min IS NULL OR remote_timezone_max IS NULL
        OR remote_timezone_max >= remote_timezone_min),
    CONSTRAINT opp_close_reason_needs_time CHECK (
        (closed_at IS NULL) = (close_reason IS NULL))
);

CREATE TRIGGER opportunity_updated_at BEFORE UPDATE ON opportunity
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The claim query. Partial index: claimable rows only, which keeps it small
-- even as the ready corpus grows.
CREATE INDEX idx_opp_claim ON opportunity (pipeline_state, next_attempt_at)
    WHERE pipeline_state <> 'ready';
-- The sweeper's stranded-record query.
CREATE INDEX idx_opp_stranded ON opportunity (pipeline_state, updated_at)
    WHERE pipeline_state NOT IN ('ready','failed_permanent');
-- Serving: live postings only.
CREATE INDEX idx_opp_live ON opportunity (company_id, first_seen_at DESC)
    WHERE closed_at IS NULL AND pipeline_state = 'ready';
CREATE INDEX idx_opp_liveness ON opportunity (last_seen_at) WHERE closed_at IS NULL;
CREATE INDEX idx_opp_content_hash ON opportunity (content_hash);
-- Dedup blocking key: (company, title tokens, country) — see blueprint §15.
CREATE INDEX idx_opp_block ON opportunity (company_id, title_normalized, location_country);
-- Keyword search. v1 keeps FTS here rather than in a separate system.
CREATE INDEX idx_opp_fts ON opportunity
    USING gin (to_tsvector('english', coalesce(title_normalized,'') || ' ' || coalesce(description_text,'')));

-- Provenance. One row per place we saw it; merges stay reversible.
CREATE TABLE opportunity_source (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    opportunity_id  uuid NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,
    -- Required on every derived artifact so source-level purge stays a single
    -- operation rather than archaeology under time pressure.
    source_id       uuid NOT NULL REFERENCES source(id),
    source_job_id   text NOT NULL,
    ats_type        text,
    ats_job_id      text,
    apply_url       text,
    raw_object_key  text,
    content_hash    bytea,
    simhash         bigint,
    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    etag            text,
    merge_reason    text CHECK (merge_reason IN
                      ('exact_ats','content_hash','apply_url','simhash','human')),
    merge_confidence real CHECK (merge_confidence BETWEEN 0 AND 1),
    merged_by        text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, source_job_id)
);

CREATE INDEX idx_opp_source_opp ON opportunity_source (opportunity_id);
CREATE INDEX idx_opp_source_source ON opportunity_source (source_id);
-- For Tier-A sources this pair is a stable global identifier, which is what
-- makes dedup nearly free. Partial: only where the source actually provides it.
CREATE UNIQUE INDEX idx_opp_source_ats ON opportunity_source (ats_type, ats_job_id)
    WHERE ats_type IS NOT NULL AND ats_job_id IS NOT NULL;

CREATE TABLE opportunity_skill (
    opportunity_id        uuid NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,
    skill_id              uuid NOT NULL REFERENCES skill(id),
    requirement_level     text NOT NULL CHECK (requirement_level IN ('required','preferred','mentioned')),
    extraction_confidence real CHECK (extraction_confidence BETWEEN 0 AND 1),
    -- Reproducibility: a score is only defensible if you can say which model,
    -- prompt and ontology produced its inputs.
    ontology_version  text NOT NULL,
    model_id          text,
    prompt_version    text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (opportunity_id, skill_id, requirement_level)
);

CREATE INDEX idx_opp_skill_skill ON opportunity_skill (skill_id);

-- Vectors from two models are not comparable, so the version is part of the key:
-- that is what makes the dual-write migration (write both, backfill, verify
-- recall, switch reads by version) possible at all.
CREATE TABLE opportunity_embedding (
    opportunity_id    uuid NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,
    embedding_model   text NOT NULL,
    embedding_version text NOT NULL,
    embedding_dim     integer NOT NULL,
    embedding         vector(768) NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (opportunity_id, embedding_version)
);

-- The operator class must match the distance function the query uses, or the
-- index is silently ignored and you get a sequential scan.
CREATE INDEX idx_opp_emb_hnsw ON opportunity_embedding
    USING hnsw (embedding vector_cosine_ops);
