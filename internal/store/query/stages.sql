-- name: GetOpportunityForNormalize :one
SELECT id, version, title_raw, location_region, description_text, company_id
  FROM opportunity WHERE id = $1;

-- name: ApplyNormalization :execrows
-- Version-guarded: another stage may have written first, in which case the
-- caller reloads rather than clobbering.
UPDATE opportunity
   SET title_normalized      = sqlc.arg(title_normalized),
       role_family           = sqlc.arg(role_family),
       seniority_ordinal     = sqlc.arg(seniority_ordinal),
       is_management         = sqlc.arg(is_management),
       work_mode             = sqlc.arg(work_mode),
       location_country      = sqlc.arg(location_country),
       location_city         = sqlc.arg(location_city),
       remote_geo_scope      = sqlc.arg(remote_geo_scope),
       simhash               = sqlc.arg(simhash),
       block_key             = sqlc.arg(block_key),
       normalization_version = sqlc.arg(normalization_version),
       version               = version + 1
 WHERE id = sqlc.arg(id) AND version = sqlc.arg(version);

-- name: FindBlockCandidates :many
-- Only ever compares within a block, and never against a row already merged
-- away or closed.
SELECT o.id, o.company_id, o.ats_type, o.title_normalized, o.content_hash,
       o.simhash, o.location_country, o.remote_geo_scope,
       coalesce(length(o.description_text), 0)::int AS text_len,
       s.apply_url, s.ats_job_id
  FROM opportunity o
  LEFT JOIN opportunity_source s ON s.opportunity_id = o.id
 WHERE o.block_key = sqlc.arg(block_key)
   AND o.id <> sqlc.arg(exclude_id)
   AND o.merged_into IS NULL
   AND o.closed_at IS NULL
 LIMIT sqlc.arg(max_candidates)::int;

-- name: MoveSourceRows :execrows
UPDATE opportunity_source
   SET opportunity_id = sqlc.arg(into_id),
       merge_reason = sqlc.arg(reason),
       merge_confidence = sqlc.arg(confidence),
       merged_by = 'dedupe'
 WHERE opportunity_id = sqlc.arg(from_id);

-- name: MarkMerged :execrows
UPDATE opportunity SET merged_into = sqlc.arg(into_id), version = version + 1
 WHERE id = sqlc.arg(from_id) AND merged_into IS NULL;

-- name: RecordMerge :one
INSERT INTO opportunity_merge (from_opportunity_id, into_opportunity_id, reason,
                               confidence, source_rows_moved, merged_by)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING *;

-- name: UndoMerge :one
-- Reversal is a first-class operation, not a recovery script: a false merge
-- hides a real job, and someone will need to undo one under time pressure.
UPDATE opportunity_merge SET undone_at = now()
 WHERE id = $1 AND undone_at IS NULL
RETURNING *;

-- name: NormalizationStats :many
SELECT normalization_version,
       count(*) AS total,
       count(role_family) AS with_family,
       count(seniority_ordinal) AS with_seniority,
       count(location_country) AS with_country,
       count(*) FILTER (WHERE is_management) AS management
  FROM opportunity
 WHERE merged_into IS NULL
 GROUP BY normalization_version;

-- name: GetOpportunityForDedupe :one
SELECT o.id, o.version, o.company_id, o.title_normalized, o.content_hash,
       o.simhash, o.block_key, o.location_country, o.remote_geo_scope, o.ats_type,
       coalesce(length(o.description_text), 0)::int AS text_len,
       s.apply_url, s.ats_job_id
  FROM opportunity o
  LEFT JOIN opportunity_source s ON s.opportunity_id = o.id
 WHERE o.id = $1
 LIMIT 1;

-- name: LockBlock :exec
-- Serializes merge decisions within one block for the duration of the
-- transaction. Without it two workers in the same block can each merge into the
-- other, producing an A->B->A cycle that no later read can untangle.
SELECT pg_advisory_xact_lock(hashtext(sqlc.arg(block_key)::text));

-- name: IsUnmerged :one
SELECT (merged_into IS NULL)::boolean AS unmerged FROM opportunity WHERE id = $1;

-- name: FindMultiMemberBlocks :many
-- Dedup is order-dependent: on a bulk first ingest two identical postings can
-- each complete before the other's block_key is visible, so neither ever sees
-- the other. This makes dedup eventually consistent instead.
SELECT block_key, count(*) AS members
  FROM opportunity
 WHERE block_key IS NOT NULL
   AND merged_into IS NULL
   AND closed_at IS NULL
 GROUP BY block_key
HAVING count(*) > 1
 ORDER BY count(*) DESC
 LIMIT sqlc.arg(batch)::int;

-- name: ListBlockMembers :many
SELECT o.id, o.version, o.company_id, o.title_normalized, o.content_hash,
       o.simhash, o.location_country, o.remote_geo_scope, o.ats_type, o.first_seen_at,
       coalesce(length(o.description_text), 0)::int AS text_len,
       s.apply_url, s.ats_job_id
  FROM opportunity o
  LEFT JOIN opportunity_source s ON s.opportunity_id = o.id
 WHERE o.block_key = $1
   AND o.merged_into IS NULL
   AND o.closed_at IS NULL
 ORDER BY o.first_seen_at, o.id;

-- name: QueueMergeCandidate :exec
-- Idempotent: re-running the sweep must not queue the same pair twice.
INSERT INTO merge_candidate (left_opportunity_id, right_opportunity_id, reason,
                             confidence, withheld_because)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (left_opportunity_id, right_opportunity_id) DO NOTHING;

-- name: CountOpenMergeCandidates :one
SELECT count(*) FROM merge_candidate WHERE resolution IS NULL;
