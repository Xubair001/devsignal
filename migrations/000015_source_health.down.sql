ALTER TABLE source DROP COLUMN IF EXISTS platform_review_ref;
ALTER TABLE source DROP COLUMN IF EXISTS last_health_note;
ALTER TABLE source DROP COLUMN IF EXISTS consecutive_degraded;
DROP TABLE IF EXISTS source_health_daily;
