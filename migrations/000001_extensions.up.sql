-- vector: kNN for profile<->description similarity. v1 keeps rows, FTS and
--         vectors in one database (blueprint §22).
-- pg_trgm: fuzzy company-name matching, which is the LAST resort in resolution
--          order and always queued for human confirmation, never auto-merged.
-- citext:  case-insensitive natural keys (domains, aliases, emails).
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS citext;

-- Every table with updated_at uses this. The sweeper reads min(updated_at) to
-- detect stranded records, so a stale updated_at hides a real problem.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
