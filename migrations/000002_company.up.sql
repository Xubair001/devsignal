-- Company is a resolved entity, never a free-text string: every aggregate in
-- the market-intelligence product is keyed on it (blueprint §8).
CREATE TABLE company (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- eTLD+1 of the careers page / apply URL. The only stable identity;
    -- names are not ("Google" / "Google LLC" / "Alphabet" / "google.com").
    canonical_domain  citext NOT NULL UNIQUE,
    display_name      text   NOT NULL,
    -- Agencies post on behalf of unnamed clients. Excluded from every
    -- employer-level statistic, so this must be explicit, not inferred later.
    is_agency         boolean NOT NULL DEFAULT false,
    industry          text,
    size_band         text,
    stage             text,
    hq_country        char(2),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER company_updated_at BEFORE UPDATE ON company
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Names seen in the wild, for resolution step 3 (exact alias match).
CREATE TABLE company_alias (
    company_id  uuid   NOT NULL REFERENCES company(id) ON DELETE CASCADE,
    alias       citext NOT NULL,
    source_id   uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, alias)
);

CREATE INDEX idx_company_alias_alias ON company_alias (alias);
-- trigram index supports resolution step 4 only (fuzzy, human-confirmed).
CREATE INDEX idx_company_alias_trgm ON company_alias USING gin (alias gin_trgm_ops);
