-- name: GetExtraction :one
SELECT * FROM extraction
 WHERE content_hash = $1 AND prompt_version = $2
   AND model_id = $3 AND schema_version = $4;

-- name: PutExtraction :exec
-- Idempotent on the cache key. Two workers racing on the same posting must not
-- both pay, and the second write must not fail the record.
INSERT INTO extraction (
    content_hash, prompt_version, model_id, schema_version,
    raw_output, normalized, input_tokens, output_tokens, cache_read_tokens, lane
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (content_hash, prompt_version, model_id, schema_version) DO NOTHING;

-- name: AttachExtractionToOpportunity :execrows
UPDATE opportunity
   SET extraction_content_hash = sqlc.arg(content_hash),
       enriched_at = now(),
       extraction_error = NULL,
       version = version + 1,
       pipeline_state = sqlc.arg(next_state),
       next_attempt_at = now(),
       attempts = 0,
       last_error = NULL,
       lease_until = NULL
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND pipeline_state = sqlc.arg(current_state);

-- name: RecordExtractionFailure :exec
-- Per-opportunity, so one unparseable posting does not look like a broken model.
UPDATE opportunity SET extraction_error = $2 WHERE id = $1;

-- name: ReplaceOpportunitySkills :exec
DELETE FROM opportunity_skill WHERE opportunity_id = $1;

-- name: InsertOpportunitySkill :exec
INSERT INTO opportunity_skill (
    opportunity_id, skill_id, requirement_level, extraction_confidence,
    ontology_version, model_id, prompt_version
) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (opportunity_id, skill_id, requirement_level) DO NOTHING;

-- name: UpsertSkillByAlias :one
-- Resolves an extracted name against our own ontology. The model is never asked
-- to guess our slugs; it names the technology as the posting wrote it and the
-- mapping happens here, where it is versioned and reviewable.
WITH existing AS (
    SELECT s.id FROM skill_alias a JOIN skill s ON s.id = a.skill_id
     WHERE a.alias = sqlc.arg(alias)::citext
     LIMIT 1
), created AS (
    INSERT INTO skill (canonical_slug, display_name, ontology_version)
    SELECT sqlc.arg(slug)::citext, sqlc.arg(display_name), sqlc.arg(ontology_version)
     WHERE NOT EXISTS (SELECT 1 FROM existing)
    ON CONFLICT (canonical_slug) DO NOTHING
    RETURNING id
)
SELECT id FROM existing
UNION ALL SELECT id FROM created
UNION ALL SELECT s.id FROM skill s WHERE s.canonical_slug = sqlc.arg(slug)::citext
LIMIT 1;

-- name: LinkSkillAlias :exec
INSERT INTO skill_alias (skill_id, alias) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ExtractionSpendReport :many
-- What enrichment actually costs, by day and model. Spend on the only
-- per-token component has to be observable rather than inferred.
SELECT created_at::date AS day, model_id, lane,
       count(*)::int AS calls,
       sum(input_tokens)::bigint AS input_tokens,
       sum(output_tokens)::bigint AS output_tokens,
       sum(cache_read_tokens)::bigint AS cache_read_tokens
  FROM extraction
 GROUP BY 1,2,3
 ORDER BY 1 DESC, 2;

-- name: GetOpportunityForEnrichment :one
SELECT id, version, content_hash, description_text, title_raw
  FROM opportunity WHERE id = $1;
