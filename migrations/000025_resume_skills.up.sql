-- Resume skill extraction: the record of what was sent, and to whom.
--
-- These columns live on `resume` rather than in the shared `extraction` table,
-- and that placement is the point.
--
-- `extraction` is keyed on (content_hash, prompt_version, model_id,
-- schema_version) and holds nothing user-derived — every row in it comes from a
-- public job posting. A resume extraction cached there WOULD be user-derived
-- data (privacy rule 1 says so explicitly), in a table with no user_id, which
-- means erasure could not scope a delete to one person. Putting the record here
-- makes the cache cascade with the resume row, which is already in the erasure
-- inventory, so this adds no new location to erase.
--
-- Expand-only: every column is nullable with no default, so an existing row is
-- untouched and old code ignores them.
ALTER TABLE resume ADD COLUMN skills_extracted_at timestamptz;

-- Everything a score depends on carries its version (hard rule 10). A skill on a
-- profile moves a fit score, so the provenance of that skill has to be datable:
-- which model read the document, under which prompt and schema, and — the part
-- that is a promise rather than a detail — which redaction ran before it left.
ALTER TABLE resume ADD COLUMN skills_model_id text;
ALTER TABLE resume ADD COLUMN skills_prompt_version text;
ALTER TABLE resume ADD COLUMN skills_schema_version text;
ALTER TABLE resume ADD COLUMN skills_redaction_version text;

-- What actually left our boundary, named. The blueprint's rule is that we DEFINE
-- what may leave; a column is what makes that checkable after the fact rather
-- than a claim in a document.
ALTER TABLE resume ADD COLUMN skills_field_set text;

-- Counts, never values. "3 email addresses removed" is auditable; recording
-- which ones would put the PII back into the trail we built to prove we did not
-- keep it.
ALTER TABLE resume ADD COLUMN skills_redacted_chars integer;
ALTER TABLE resume ADD COLUMN skills_sent_chars integer;

-- How many skills the extraction produced, and how many the ontology could place.
-- The gap is the review signal: a resume yielding twenty skills of which two
-- resolve means the vocabulary is missing something, not that the person has two.
ALTER TABLE resume ADD COLUMN skills_found integer;
ALTER TABLE resume ADD COLUMN skills_resolved integer;

-- The claim the extraction made about experience, kept separate from the
-- profile's own field. A resume's reading is evidence; it is not the user's
-- stated preference, and overwriting what they typed with what a model read
-- would be the same category error as an imputed salary.
ALTER TABLE resume ADD COLUMN skills_years_claimed smallint;
ALTER TABLE resume ADD COLUMN skills_seniority_claimed text;

-- Finding the work: resumes whose text is parsed but whose skills are not
-- extracted yet, newest first. Partial, because the answered ones are the
-- majority once this has run and indexing them would be indexing the complement.
-- 'text_extracted' is the state a successful upload reaches; 'parsed' is
-- reserved for a structured parse that does not exist yet. Both are included so
-- the index keeps matching if that ever lands.
CREATE INDEX idx_resume_needs_skills ON resume (uploaded_at DESC)
    WHERE parse_state IN ('text_extracted', 'parsed')
      AND skills_extracted_at IS NULL AND deleted_at IS NULL;
