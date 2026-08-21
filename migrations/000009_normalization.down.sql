DROP INDEX IF EXISTS idx_opp_simhash;
DROP INDEX IF EXISTS idx_opp_country;
DROP INDEX IF EXISTS idx_opp_retrieval;
DROP TABLE IF EXISTS company_merge;
ALTER TABLE company DROP COLUMN IF EXISTS domain_confirmed;
ALTER TABLE opportunity DROP COLUMN IF EXISTS normalization_version;
ALTER TABLE opportunity DROP COLUMN IF EXISTS is_management;
