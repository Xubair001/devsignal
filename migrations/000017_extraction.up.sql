-- The extraction cache.
--
-- The composite key IS the determinism guarantee, not merely a cost saving.
-- Run the same description through the same model twice and the skill list can
-- differ slightly; those skills feed the fit score, so without this the score
-- moves for a posting that did not change. Re-extraction is only ever triggered
-- by one of these four things changing (blueprint M-05).
CREATE TABLE extraction (
    content_hash   bytea NOT NULL,
    prompt_version text  NOT NULL,
    model_id       text  NOT NULL,
    schema_version text  NOT NULL,

    -- Kept verbatim. AI systems change, and reproducing why a score was what it
    -- was later requires the model's actual words, not our interpretation of
    -- them (blueprint §37).
    raw_output jsonb NOT NULL,
    -- The validated, normalized view the rest of the system reads.
    normalized jsonb NOT NULL,

    -- Cost accounting. Enrichment is the only component billed per token, so it
    -- is the only one where spend has to be observable rather than inferred.
    input_tokens      integer NOT NULL DEFAULT 0,
    output_tokens     integer NOT NULL DEFAULT 0,
    cache_read_tokens integer NOT NULL DEFAULT 0,
    -- 'hot' is synchronous and keeps the freshness SLO; 'cold' goes through the
    -- Batch API at half price where a 24-hour turnaround is irrelevant.
    lane text NOT NULL DEFAULT 'hot' CHECK (lane IN ('hot','cold')),

    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (content_hash, prompt_version, model_id, schema_version)
);

-- Spend reporting by day and model, without scanning the table.
CREATE INDEX idx_extraction_cost ON extraction (created_at DESC, model_id);

-- Opportunity-level record of WHICH extraction produced its current enrichment,
-- so a row's derived skills are always traceable to one cache entry.
ALTER TABLE opportunity ADD COLUMN extraction_content_hash bytea;
ALTER TABLE opportunity ADD COLUMN enriched_at timestamptz;

-- Extraction failures are recorded per opportunity rather than globally: one
-- posting the model cannot parse must not look like a broken model.
ALTER TABLE opportunity ADD COLUMN extraction_error text;
