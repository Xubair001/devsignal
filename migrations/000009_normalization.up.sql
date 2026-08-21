-- Management is a SEPARATE ladder, not a higher rung on the IC one.
--
-- A Senior Engineer is not "less senior" than an Engineering Manager; they are
-- different tracks. Forcing both onto seniority_ordinal would make the seniority
-- distance term in the fit score compare unlike things, so matching would be
-- confidently wrong rather than merely uncertain.
ALTER TABLE opportunity ADD COLUMN is_management boolean NOT NULL DEFAULT false;

-- Which normalization ruleset produced the derived fields. A ruleset change is
-- then detectable and re-runnable rather than silently mixed with old output.
ALTER TABLE opportunity ADD COLUMN normalization_version text;

-- Distinguishes a real registrable domain from the synthetic *.ats.invalid
-- identity derived from a board token. Without this, a pseudo-domain looks
-- exactly like a confirmed one and company aggregates silently mix them.
ALTER TABLE company ADD COLUMN domain_confirmed boolean NOT NULL DEFAULT false;

-- Merges must be reversible, including company merges. Recording the decision
-- is what makes un-merge possible; a merge that destroys the loser is not.
CREATE TABLE company_merge (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_company_id uuid NOT NULL,
    into_company_id uuid NOT NULL REFERENCES company(id),
    reason         text NOT NULL CHECK (reason IN ('domain_match','alias_match','human')),
    confidence     real CHECK (confidence BETWEEN 0 AND 1),
    merged_by      text NOT NULL,
    merged_at      timestamptz NOT NULL DEFAULT now(),
    undone_at      timestamptz,
    CONSTRAINT company_merge_not_self CHECK (from_company_id <> into_company_id)
);

CREATE INDEX idx_company_merge_from ON company_merge (from_company_id) WHERE undone_at IS NULL;

-- Retrieval predicates (blueprint §18): the candidate generator filters on these
-- before anything expensive runs.
CREATE INDEX idx_opp_retrieval ON opportunity (role_family, seniority_ordinal, work_mode)
    WHERE closed_at IS NULL AND pipeline_state = 'ready';
CREATE INDEX idx_opp_country ON opportunity (location_country)
    WHERE closed_at IS NULL AND pipeline_state = 'ready';
-- Dedup blocking: candidates are only ever compared within a block.
CREATE INDEX idx_opp_simhash ON opportunity (company_id, simhash)
    WHERE simhash IS NOT NULL;
