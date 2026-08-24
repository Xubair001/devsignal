DROP INDEX IF EXISTS idx_opp_title_fts;

DROP TABLE IF EXISTS profile_embedding;

ALTER TABLE profile DROP CONSTRAINT IF EXISTS profile_employment_types_known;
ALTER TABLE profile DROP COLUMN IF EXISTS target_employment_types;
