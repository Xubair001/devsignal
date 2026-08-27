-- name: GetOpportunityForNormalize :one
SELECT id, version, title_raw, location_region, description_text, company_id
  FROM opportunity WHERE id = $1;

-- name: ApplyNormalization :execrows
-- Sets the derived fields AND advances the state in ONE statement.
--
-- These cannot be separate: any write bumps version, which would invalidate a
-- follow-up version-guarded advance and livelock the stage. This is the
-- blueprint's rule that state advances in the same transaction as the work.
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
       pipeline_state        = sqlc.arg(next_state),
       next_attempt_at       = now(),
       attempts              = 0,
       last_error            = NULL,
       lease_until           = NULL,
       version               = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND pipeline_state = sqlc.arg(current_state);

-- name: FindBlockCandidates :many
-- Only ever compares within a block, and never against a row already merged
-- away or closed.
SELECT o.id, o.company_id, o.ats_type, o.title_normalized, o.content_hash,
       o.simhash, o.location_country, o.remote_geo_scope,
       coalesce(length(o.description_text), 0)::int AS text_len,
       -- Deterministic scalar subqueries, NOT a join.
       --
       -- opportunity_source is one-to-MANY and hard rule 11 keeps every row on a
       -- merge, so a canonical posting accumulates them. A LEFT JOIN therefore
       -- returns one row per source row, and that broke three different ways:
       -- the block sweeper compared a posting against itself and tripped
       -- opp_merge_not_self; the candidate LIMIT below was consumed by duplicates
       -- so genuine duplicates went unseen; and the verdict depended on whichever
       -- source row the planner happened to return. Ordering by id makes the
       -- chosen row stable, which dedup needs to be reproducible.
       (SELECT s.apply_url FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.apply_url IS NOT NULL
         ORDER BY s.id LIMIT 1) AS apply_url,
       (SELECT s.ats_job_id FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.ats_job_id IS NOT NULL
         ORDER BY s.id LIMIT 1) AS ats_job_id
  FROM opportunity o
 WHERE o.block_key = sqlc.arg(block_key)
   AND o.id <> sqlc.arg(exclude_id)
   AND o.merged_into IS NULL
   -- A human already said these are different roles. A simhash is not entitled
   -- to overrule that, so an un-merged posting is never a merge candidate again.
   AND o.unmerged_at IS NULL
   AND o.closed_at IS NULL
 LIMIT sqlc.arg(max_candidates)::int;

-- name: MoveSourceRows :many
-- Returns the ids it moved, not just how many.
--
-- The count alone made un-merge impossible: with two merges into one canonical
-- there is no way to infer which rows came from where. Hard rule 11 says merges
-- are reversible, and this is what makes that true rather than aspirational.
UPDATE opportunity_source
   SET opportunity_id = sqlc.arg(into_id),
       merge_reason = sqlc.arg(reason),
       merge_confidence = sqlc.arg(confidence),
       merged_by = 'dedupe'
 WHERE opportunity_id = sqlc.arg(from_id)
RETURNING id;

-- name: MarkMerged :execrows
UPDATE opportunity SET merged_into = sqlc.arg(into_id), version = version + 1
 WHERE id = sqlc.arg(from_id) AND merged_into IS NULL;

-- name: RecordMerge :one
INSERT INTO opportunity_merge (from_opportunity_id, into_opportunity_id, reason,
                               confidence, source_rows_moved, merged_by,
                               moved_source_ids)
VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING *;

-- name: FindLatestMergeFor :one
-- The merge that hid this posting, so it can be reversed.
SELECT * FROM opportunity_merge
 WHERE from_opportunity_id = sqlc.arg(from_opportunity_id) AND undone_at IS NULL
 ORDER BY merged_at DESC
 LIMIT 1;

-- name: UndoMerge :one
-- Marks the merge record reversed. One of three statements that make up an
-- un-merge; see internal/admin.Unmerge for the whole operation, which must run in
-- one transaction or a crash leaves a posting neither merged nor visible.
UPDATE opportunity_merge SET undone_at = now()
 WHERE id = $1 AND undone_at IS NULL
RETURNING *;

-- name: RestoreSourceRows :execrows
-- Moves exactly the rows a merge moved back to the posting they came from, and
-- clears the merge provenance from them.
UPDATE opportunity_source
   SET opportunity_id = sqlc.arg(back_to_id),
       merge_reason = NULL, merge_confidence = NULL, merged_by = NULL
 WHERE id = ANY (sqlc.arg(source_row_ids)::uuid[]);

-- name: RestoreMergedOpportunity :execrows
-- Makes the posting visible again and marks it as human-unmerged.
--
-- unmerged_at is what stops dedup re-merging it on the next pass. Without it the
-- operator watches their un-merge undo itself: the posting becomes claimable, the
-- same block yields the same near-identical pair, and the heuristic wins.
--
-- pipeline_state moves to 'deduped' rather than staying where it was, so the
-- posting resumes AFTER the dedupe stage and continues to enrichment on its own.
UPDATE opportunity
   SET merged_into = NULL, unmerged_at = now(), pipeline_state = 'deduped',
       version = version + 1, attempts = 0, last_error = NULL,
       next_attempt_at = now(), lease_until = NULL
 WHERE id = sqlc.arg(opportunity_id) AND merged_into IS NOT NULL;

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
       -- Deterministic scalar subqueries, NOT a join.
       --
       -- opportunity_source is one-to-MANY and hard rule 11 keeps every row on a
       -- merge, so a canonical posting accumulates them. A LEFT JOIN therefore
       -- returns one row per source row, and that broke three different ways:
       -- the block sweeper compared a posting against itself and tripped
       -- opp_merge_not_self; the candidate LIMIT below was consumed by duplicates
       -- so genuine duplicates went unseen; and the verdict depended on whichever
       -- source row the planner happened to return. Ordering by id makes the
       -- chosen row stable, which dedup needs to be reproducible.
       (SELECT s.apply_url FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.apply_url IS NOT NULL
         ORDER BY s.id LIMIT 1) AS apply_url,
       (SELECT s.ats_job_id FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.ats_job_id IS NOT NULL
         ORDER BY s.id LIMIT 1) AS ats_job_id
  FROM opportunity o
 WHERE o.id = $1;

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
       -- Deterministic scalar subqueries, NOT a join.
       --
       -- opportunity_source is one-to-MANY and hard rule 11 keeps every row on a
       -- merge, so a canonical posting accumulates them. A LEFT JOIN therefore
       -- returns one row per source row, and that broke three different ways:
       -- the block sweeper compared a posting against itself and tripped
       -- opp_merge_not_self; the candidate LIMIT below was consumed by duplicates
       -- so genuine duplicates went unseen; and the verdict depended on whichever
       -- source row the planner happened to return. Ordering by id makes the
       -- chosen row stable, which dedup needs to be reproducible.
       (SELECT s.apply_url FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.apply_url IS NOT NULL
         ORDER BY s.id LIMIT 1) AS apply_url,
       (SELECT s.ats_job_id FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.ats_job_id IS NOT NULL
         ORDER BY s.id LIMIT 1) AS ats_job_id
  FROM opportunity o
 WHERE o.block_key = $1
   AND o.merged_into IS NULL
   AND o.unmerged_at IS NULL
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
