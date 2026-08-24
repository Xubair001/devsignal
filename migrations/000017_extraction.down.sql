ALTER TABLE opportunity DROP COLUMN IF EXISTS extraction_error;
ALTER TABLE opportunity DROP COLUMN IF EXISTS enriched_at;
ALTER TABLE opportunity DROP COLUMN IF EXISTS extraction_content_hash;
DROP TABLE IF EXISTS extraction;
