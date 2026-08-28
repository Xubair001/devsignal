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

-- name: SeedSkill :one
-- Idempotent insert of a canonical skill. The ontology version is updated on an
-- existing row so a re-seed after a vocabulary change is visible rather than
-- silently identical.
INSERT INTO skill (canonical_slug, display_name, ontology_version)
VALUES (sqlc.arg(slug)::citext, sqlc.arg(display_name), sqlc.arg(ontology_version))
ON CONFLICT (canonical_slug) DO UPDATE SET
    display_name     = excluded.display_name,
    ontology_version = excluded.ontology_version
RETURNING id;

-- name: SeedSkillAlias :exec
-- Binds a normalized alias to a canonical skill.
--
-- ON CONFLICT (alias) rather than DO NOTHING: the unique index on alias is what
-- makes normalization a function, and a re-seed that moves an alias from one
-- skill to another must actually move it. Silently keeping the old binding would
-- leave the database disagreeing with the committed ontology.
INSERT INTO skill_alias (skill_id, alias)
VALUES (sqlc.arg(skill_id), sqlc.arg(alias)::citext)
ON CONFLICT (alias) DO UPDATE SET skill_id = excluded.skill_id;

-- name: SeedSkillEdge :exec
INSERT INTO skill_edge (from_skill_id, to_skill_id, relation)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: SkillIDBySlug :one
SELECT id FROM skill WHERE canonical_slug = sqlc.arg(slug)::citext;

-- name: CountSkillsAndAliases :one
SELECT (SELECT count(*) FROM skill)::bigint       AS skills,
       (SELECT count(*) FROM skill_alias)::bigint AS aliases,
       (SELECT count(*) FROM skill_edge)::bigint  AS edges;

-- name: UnresolvedExtractedSkills :many
-- Skills created by extraction that the committed ontology does not know.
--
-- The review queue for the vocabulary. Ordered by how often they appear, because
-- a phrase seen on forty postings is worth adding and one seen once is usually
-- noise. This is how the ontology grows from evidence instead of guesswork.
SELECT s.canonical_slug::text AS slug, s.display_name,
       count(DISTINCT os.opportunity_id)::bigint AS postings
  FROM skill s
  JOIN opportunity_skill os ON os.skill_id = s.id
 WHERE s.ontology_version <> sqlc.arg(ontology_version)
 GROUP BY s.id, s.canonical_slug, s.display_name
 ORDER BY postings DESC, s.canonical_slug
 LIMIT sqlc.arg(max_rows)::int;

-- name: SnapshotSkillDemand :execrows
-- THE MOAT, written daily. Blueprint §9: this cannot be backfilled, because you
-- cannot retroactively observe what the market wanted last quarter.
--
-- A recomputed SNAPSHOT of what is live on `day`, not an incrementing counter.
-- A counter would double-count every re-extraction and drift with no way to
-- audit it; a snapshot is idempotent, so running the job twice in a day is
-- harmless and a missed day is a visible gap rather than a silent
-- under-count.
--
-- Grouped by country and work_mode because demand is not global: "Go, remote,
-- hiring US-only" and "Go, onsite, Berlin" are different markets, and averaging
-- them answers no question anyone has. An unstated country becomes '??' rather
-- than being dropped, so the total stays reconcilable against the corpus.
INSERT INTO skill_demand_daily (skill_id, day, country, work_mode, posting_count)
SELECT os.skill_id,
       sqlc.arg(day)::date,
       coalesce(nullif(o.location_country, ''), '??')::char(2),
       coalesce(nullif(o.work_mode, ''), 'unknown'),
       count(DISTINCT o.id)::int
  FROM opportunity_skill os
  JOIN opportunity o ON o.id = os.opportunity_id
 WHERE o.pipeline_state = 'ready'
   AND o.closed_at IS NULL
   AND o.merged_into IS NULL
 GROUP BY os.skill_id, coalesce(nullif(o.location_country, ''), '??'),
          coalesce(nullif(o.work_mode, ''), 'unknown')
ON CONFLICT (skill_id, day, country, work_mode) DO UPDATE
   SET posting_count = excluded.posting_count;

-- name: TopSkillDemand :many
-- What the market is asking for on one day, most-demanded first.
SELECT s.display_name, sum(d.posting_count)::bigint AS postings,
       count(DISTINCT d.country)::int AS countries
  FROM skill_demand_daily d
  JOIN skill s ON s.id = d.skill_id
 WHERE d.day = sqlc.arg(day)::date
 GROUP BY s.id, s.display_name
 ORDER BY postings DESC, s.display_name
 LIMIT sqlc.arg(max_rows)::int;

-- name: SkillDemandDays :one
SELECT count(DISTINCT day)::bigint AS days,
       coalesce(max(day)::text, '')::text AS latest
  FROM skill_demand_daily;
