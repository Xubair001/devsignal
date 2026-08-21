-- The developer profile: the right-hand side of every match.
--
-- profile_version is the cache key for fit scores (blueprint §20). Any change
-- that could move a score must bump it, or users keep seeing scores computed
-- against a profile they have already edited.
CREATE TABLE profile (
    user_id           uuid PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    tenant_id         uuid NOT NULL,

    headline          text,
    years_experience  smallint CHECK (years_experience BETWEEN 0 AND 70),
    seniority_ordinal smallint CHECK (seniority_ordinal BETWEEN 1 AND 6),
    is_management     boolean NOT NULL DEFAULT false,

    -- Preferences. Arrays rather than join tables: these are small, always read
    -- whole, and never queried across users.
    target_role_families text[] NOT NULL DEFAULT '{}',
    target_countries     char(2)[] NOT NULL DEFAULT '{}',
    work_mode_preference text CHECK (work_mode_preference IN ('remote','hybrid','onsite','any')),
    languages            char(2)[] NOT NULL DEFAULT '{}',

    -- Money as integer minor units, as everywhere else.
    min_salary_minor bigint,
    salary_currency  char(3),
    salary_period    text CHECK (salary_period IN ('year','month','week','day','hour')),

    -- Work authorization per country. The eligibility gate reads this, and
    -- getting it wrong silently excludes roles the user could actually take.
    work_authorization jsonb NOT NULL DEFAULT '{}'::jsonb,

    profile_version int NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT profile_salary_needs_currency CHECK (
        min_salary_minor IS NULL OR salary_currency IS NOT NULL)
);

CREATE TRIGGER profile_updated_at BEFORE UPDATE ON profile
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Bumped on any change that could move a fit score, so a stale cached score is
-- impossible rather than merely unlikely.
CREATE OR REPLACE FUNCTION bump_profile_version() RETURNS trigger AS $$
BEGIN
    NEW.profile_version = OLD.profile_version + 1;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER profile_version_bump BEFORE UPDATE ON profile
    FOR EACH ROW EXECUTE FUNCTION bump_profile_version();

CREATE TABLE profile_skill (
    user_id     uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    skill_id    uuid NOT NULL REFERENCES skill(id),
    -- Where the claim came from. A skill the user typed is stronger evidence
    -- than one a parser guessed from a resume, and the score should be able to
    -- tell them apart.
    origin      text NOT NULL CHECK (origin IN ('manual','resume','github')),
    proficiency smallint CHECK (proficiency BETWEEN 1 AND 5),
    years       smallint CHECK (years BETWEEN 0 AND 70),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, skill_id)
);

CREATE INDEX idx_profile_skill_skill ON profile_skill (skill_id);

-- Resume files. The file itself NEVER lives in Postgres.
--
-- The extracted text is the densest concentration of PII in the system, so it
-- also goes to object storage rather than a column: one place to lock down, one
-- place to delete, and it never lands in a database backup or a query log.
CREATE TABLE resume (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    object_key      text NOT NULL,
    text_object_key text,
    filename        text,
    content_type    text,
    size_bytes      bigint NOT NULL,
    sha256          bytea NOT NULL,
    text_chars      integer,
    parse_state     text NOT NULL DEFAULT 'uploaded'
                      CHECK (parse_state IN ('uploaded','text_extracted','parsed','failed')),
    parse_error     text,
    uploaded_at     timestamptz NOT NULL DEFAULT now(),
    -- Soft-deleted first so the erasure job has something to work from; the
    -- objects are removed by the job, not by the request handler.
    deleted_at      timestamptz
);

CREATE INDEX idx_resume_user ON resume (user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_resume_pending ON resume (parse_state) WHERE deleted_at IS NULL;

-- Erasure as tracked work, not a DELETE statement.
--
-- Deleting the Postgres rows is the easy 60%. What survives a naive
-- implementation: object storage, embeddings, index documents, caches, cached
-- extractions keyed by content hash, analytics copies. Each location reports its
-- own completion so a PARTIAL erasure is visible rather than assumed complete.
CREATE TABLE erasure_request (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL,
    requested_at  timestamptz NOT NULL DEFAULT now(),
    completed_at  timestamptz,
    -- Kept after the user row is gone: proving an erasure happened requires
    -- outliving its subject, so this table holds NO PII beyond the id.
    CONSTRAINT erasure_request_user_once UNIQUE (user_id, requested_at)
);

CREATE TABLE erasure_step (
    request_id   uuid NOT NULL REFERENCES erasure_request(id) ON DELETE CASCADE,
    location     text NOT NULL,
    status       text NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','done','failed','not_applicable')),
    items        integer NOT NULL DEFAULT 0,
    detail       text,
    completed_at timestamptz,
    PRIMARY KEY (request_id, location)
);

CREATE INDEX idx_erasure_incomplete ON erasure_request (requested_at)
    WHERE completed_at IS NULL;
