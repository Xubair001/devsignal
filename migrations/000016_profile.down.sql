DROP TABLE IF EXISTS erasure_step;
DROP TABLE IF EXISTS erasure_request;
DROP TABLE IF EXISTS resume;
DROP INDEX IF EXISTS idx_profile_skill_skill;
DROP TABLE IF EXISTS profile_skill;
DROP TRIGGER IF EXISTS profile_version_bump ON profile;
DROP FUNCTION IF EXISTS bump_profile_version();
DROP TABLE IF EXISTS profile;
