DROP INDEX IF EXISTS idx_opp_unmerged;
ALTER TABLE opportunity DROP COLUMN IF EXISTS unmerged_at;
ALTER TABLE opportunity_merge DROP COLUMN IF EXISTS moved_source_ids;

DROP TABLE IF EXISTS opportunity_flag;
DROP INDEX IF EXISTS idx_app_user_admin;
ALTER TABLE app_user DROP COLUMN IF EXISTS role;
