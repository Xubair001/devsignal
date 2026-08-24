-- Step 14: retrieval. Two additions the profile needs before it can drive the
-- hard predicates, plus somewhere to keep the profile's vector.

-- Employment type is one of the hard predicates in the retrieval spec, and the
-- profile had no way to express it. Empty means "no constraint", which is the
-- right default: a filter nobody set must not silently exclude anything.
ALTER TABLE profile
    ADD COLUMN target_employment_types text[] NOT NULL DEFAULT '{}';

ALTER TABLE profile
    ADD CONSTRAINT profile_employment_types_known CHECK (
        target_employment_types <@ ARRAY['full_time','part_time','contract','internship','temporary']
    );

-- The profile's vector, stored rather than recomputed per retrieval.
--
-- Recomputing is nearly free with the local embedder and would be a network call
-- per request with a hosted one, so the cost of getting this wrong is paid later
-- and all at once. profile_version is recorded alongside so a stale vector is
-- detectable: the trigger on profile bumps that version on every edit, and a
-- vector whose recorded version is behind the profile's is known to be stale
-- instead of quietly ranking against yesterday's preferences.
--
-- No HNSW index here. Nothing searches profiles by vector in v1 — retrieval goes
-- the other way, from one profile to many opportunities — and an unused vector
-- index is write amplification with no reader.
CREATE TABLE profile_embedding (
    user_id           uuid        NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    embedding_model   text        NOT NULL,
    embedding_version text        NOT NULL,
    embedding_dim     integer     NOT NULL,
    embedding         vector(768) NOT NULL,
    profile_version   integer     NOT NULL,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, embedding_version)
);

COMMENT ON TABLE profile_embedding IS
    'Profile vectors by version. Erasure deletes via the app_user cascade.';

-- The keyword retrieval channel matches titles, not descriptions, so it needs an
-- index on the title expression alone. idx_opp_fts covers
-- title_normalized || description_text and cannot serve this query.
--
-- Measured on 199 real GitLab postings with the terms "backend OR platform":
-- matching title plus description returned all 199, because the company's
-- boilerplate names its own platform in every description. Matching the title
-- returned 34, every one of them a genuine backend or platform role. A retrieval
-- channel that returns the entire corpus does not bound anything, which is the
-- one thing stage 1 exists to do.
CREATE INDEX idx_opp_title_fts ON opportunity
    USING gin (to_tsvector('english', coalesce(title_normalized, '')))
    WHERE closed_at IS NULL AND merged_into IS NULL AND pipeline_state = 'ready';
