-- Step 15: the eligibility gate, the fit score and its explanation.

-- fit_score is a PURE function of (profile_version, opportunity_version,
-- weights_version, model_version) — never of the current time. That is what makes
-- it cacheable at all, and it is why every one of those versions is part of the
-- primary key rather than a payload column: a version change must produce a new
-- row, not silently overwrite a score computed under different rules.
--
-- Recency, closing-soon and saturation live in priority_score, which is computed
-- at read time and never stored. There is deliberately no column for it here.
CREATE TABLE fit_score (
    user_id            uuid        NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    opportunity_id     uuid        NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,

    -- Every input version the score depends on. Hard rule 10.
    weights_version    text        NOT NULL,
    profile_version    integer     NOT NULL,
    opportunity_version integer    NOT NULL,
    embedding_version  text        NOT NULL,

    -- 0-100, stored as the integer the user is shown a band derived from. Not a
    -- probability, and never displayed as a bare percentage (blueprint §3).
    score              smallint    NOT NULL CHECK (score BETWEEN 0 AND 100),

    -- The per-factor breakdown, as the arithmetic that produced the score:
    -- [{factor, weight, value, contribution, available, reason}]. Stored rather
    -- than recomputed so an explanation shown to a user can be reproduced exactly,
    -- including which factors had no data.
    factors            jsonb       NOT NULL,

    -- The points that were ACHIEVABLE given what could be observed, below 100
    -- whenever a factor had no data. Stored because the band the user saw is
    -- derived from score/max_possible, not from score alone, and an explanation
    -- that cannot be reproduced is not an explanation.
    --
    -- An earlier design redistributed missing weight so the maximum was always
    -- 100. That made removing information RAISE the score: a posting nothing
    -- could be extracted from, whose one legible factor matched, read as a
    -- Strong fit. Earned-out-of-achievable has no such incentive.
    max_possible       smallint    NOT NULL CHECK (max_possible BETWEEN 0 AND 100),

    computed_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, opportunity_id, weights_version, profile_version,
                 opportunity_version, embedding_version)
);

-- The feed reads a user's scores newest-version-first.
CREATE INDEX idx_fit_user ON fit_score (user_id, score DESC);

COMMENT ON TABLE fit_score IS
    'Cached fit scores. User-derived: erasure deletes these (see internal/profile erasure inventory).';

-- Eligibility failures are recorded, not scored. A user asking "why am I not
-- seeing X" needs the specific reason, and an operator needs to know when a gate
-- is excluding far more than expected — a gate that silently empties a feed is
-- indistinguishable from an empty market.
--
-- Not a cache: this is the audit trail of a boolean decision, so it carries no
-- score and is keyed only by the versions that can change the outcome.
CREATE TABLE eligibility_result (
    user_id            uuid        NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    opportunity_id     uuid        NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,
    profile_version    integer     NOT NULL,
    opportunity_version integer    NOT NULL,
    eligible           boolean     NOT NULL,
    -- Empty when eligible. One row per failed check, so a posting failing on both
    -- geography and salary says both rather than only the first.
    failed_checks      text[]      NOT NULL DEFAULT '{}',
    evaluated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, opportunity_id, profile_version, opportunity_version)
);

CREATE INDEX idx_eligibility_failures ON eligibility_result (user_id)
    WHERE NOT eligible;

COMMENT ON TABLE eligibility_result IS
    'Why a posting was excluded. User-derived: erasure deletes these.';
