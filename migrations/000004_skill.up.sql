-- Bootstrapped from an open taxonomy (Lightcast Open Skills / ESCO); we own the
-- alias layer and the edges on top (blueprint §9).
CREATE TABLE skill (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_slug   citext NOT NULL UNIQUE,
    display_name     text   NOT NULL,
    external_ref     text,
    ontology_version text   NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skill_alias (
    skill_id  uuid   NOT NULL REFERENCES skill(id) ON DELETE CASCADE,
    alias     citext NOT NULL,
    PRIMARY KEY (skill_id, alias)
);

-- One alias must not map to two skills, or normalization stops being a function.
CREATE UNIQUE INDEX idx_skill_alias_unique ON skill_alias (alias);

CREATE TABLE skill_edge (
    from_skill_id uuid NOT NULL REFERENCES skill(id) ON DELETE CASCADE,
    to_skill_id   uuid NOT NULL REFERENCES skill(id) ON DELETE CASCADE,
    relation      text NOT NULL CHECK (relation IN ('prerequisite','related','supersedes')),
    PRIMARY KEY (from_skill_id, to_skill_id, relation),
    CONSTRAINT skill_edge_no_self CHECK (from_skill_id <> to_skill_id)
);

-- THE MOAT. Written from the first ingested posting, because it cannot be
-- backfilled — you cannot retroactively observe what the market wanted last
-- quarter. The product surface ships much later; the collection starts now.
CREATE TABLE skill_demand_daily (
    skill_id      uuid NOT NULL REFERENCES skill(id) ON DELETE CASCADE,
    day           date NOT NULL,
    country       char(2) NOT NULL,
    work_mode     text NOT NULL,
    posting_count integer NOT NULL DEFAULT 0,
    PRIMARY KEY (skill_id, day, country, work_mode)
);

CREATE INDEX idx_skill_demand_day ON skill_demand_daily (day DESC, skill_id);
