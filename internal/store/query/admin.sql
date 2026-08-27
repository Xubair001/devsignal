-- name: IsAdmin :one
-- Authorization is one query in one place. A per-handler check is how one handler
-- ends up missing it.
SELECT (role = 'admin')::boolean AS is_admin
  FROM app_user WHERE id = sqlc.arg(user_id) AND status = 'active';

-- name: GrantAdmin :execrows
-- Deliberately not exposed over HTTP. Granting admin is a bootstrap and recovery
-- operation, done from the binary with database access, so a compromised admin
-- session cannot mint more admins.
UPDATE app_user SET role = 'admin', updated_at = now()
 WHERE email = sqlc.arg(email) AND status = 'active';

-- name: RevokeAdmin :execrows
UPDATE app_user SET role = 'user', updated_at = now()
 WHERE email = sqlc.arg(email);

-- name: ListAdmins :many
SELECT id, email, created_at FROM app_user WHERE role = 'admin' ORDER BY email;

-- ------------------------------------------------------------------- sources

-- name: AdminListSources :many
-- The source list with the health columns the blueprint's §29 table names, so one
-- screen answers "which source is broken".
SELECT s.id, s.name, s.tier, s.type, s.status,
       s.last_success_at, s.last_failure_at,
       s.items_discovered, s.items_processed,
       s.legal_basis, s.robots_checked_at, s.terms_reviewed_at, s.reviewed_by,
       s.poll_interval, s.etag_supported,
       (SELECT count(*) FROM opportunity_source os WHERE os.source_id = s.id)::bigint
         AS postings_attributed
  FROM source s
 ORDER BY (s.status <> 'active') DESC, s.name;

-- name: AdminSetSourceStatus :one
-- Quarantine is reversible and never destructive: a quarantined source stops
-- being polled, and hard rule 9 means its postings are NOT closed as a result.
-- Inferring closure from a quarantined source is how one outage deletes the
-- corpus.
UPDATE source SET status = sqlc.arg(status), updated_at = now()
 WHERE id = sqlc.arg(source_id)
RETURNING id, name, status;

-- name: AdminSourceHealthHistory :many
-- Per-day history for one source, so parser rot is visible as a trend rather than
-- a single bad number.
SELECT day, polls, poll_failures, not_modified,
       postings_seen, postings_usable,
       with_company, with_location, with_apply_url, with_language, with_salary
  FROM source_health_daily
 WHERE source_id = sqlc.arg(source_id)
 ORDER BY day DESC
 LIMIT sqlc.arg(days);

-- ------------------------------------------------------------- provenance

-- name: AdminOpportunitySources :many
-- Every opportunity_source row for one posting, including merged ones.
--
-- Hard rule 11: dedup never deletes a source row, so this is the full provenance
-- of a canonical posting and the input to an un-merge decision.
SELECT os.id, os.source_id, s.name AS source_name,
       os.ats_type, os.ats_job_id, os.source_job_id, os.apply_url,
       os.merge_reason, os.merge_confidence, os.merged_by,
       os.first_seen_at, os.last_seen_at
  FROM opportunity_source os
  JOIN source s ON s.id = os.source_id
 WHERE os.opportunity_id = sqlc.arg(opportunity_id)
 ORDER BY os.first_seen_at;

-- name: AdminListMergedInto :many
-- Postings merged INTO this one. The un-merge candidates.
SELECT o.id, o.title_raw, o.merged_into, o.version,
       (SELECT count(*) FROM opportunity_source os WHERE os.opportunity_id = o.id)::bigint
         AS source_rows
  FROM opportunity o
 WHERE o.merged_into = sqlc.arg(canonical_id)
 ORDER BY o.first_seen_at;

-- name: AdminListMergeCandidates :many
-- The review queue for merges dedup declined to make automatically.
SELECT mc.id, mc.left_opportunity_id, mc.right_opportunity_id,
       mc.reason, mc.confidence, mc.withheld_because, mc.created_at,
       l.title_raw AS left_title, r.title_raw AS right_title
  FROM merge_candidate mc
  JOIN opportunity l ON l.id = mc.left_opportunity_id
  JOIN opportunity r ON r.id = mc.right_opportunity_id
 WHERE mc.resolution IS NULL
 ORDER BY mc.confidence DESC, mc.created_at
 LIMIT sqlc.arg(page_size);

-- name: AdminResolveMergeCandidate :one
UPDATE merge_candidate
   SET resolution = sqlc.arg(resolution),
       resolved_by = sqlc.arg(resolved_by),
       resolved_at = now()
 WHERE id = sqlc.arg(candidate_id) AND resolution IS NULL
RETURNING id, left_opportunity_id, right_opportunity_id, resolution;

-- ------------------------------------------------------------------ flags

-- name: CreateFlag :one
-- Raised by a user against a posting. One open flag per user per posting is
-- enforced by a partial unique index, so a duplicate is a conflict rather than
-- more signal.
INSERT INTO opportunity_flag (opportunity_id, reported_by, reason, detail)
VALUES (sqlc.arg(opportunity_id), sqlc.narg(reported_by), sqlc.arg(reason), sqlc.narg(detail))
RETURNING id, created_at;

-- name: AdminListFlags :many
-- The review queue, oldest first: a scam report that has sat for a day is more
-- urgent than one raised a minute ago.
SELECT f.id, f.opportunity_id, f.reason, f.detail, f.status, f.created_at,
       o.title_raw, o.closed_at,
       c.display_name AS company_name,
       (SELECT count(*) FROM opportunity_flag x
         WHERE x.opportunity_id = f.opportunity_id)::bigint AS flags_on_posting
  FROM opportunity_flag f
  JOIN opportunity o ON o.id = f.opportunity_id
  LEFT JOIN company c ON c.id = o.company_id
 WHERE (sqlc.narg(status)::text IS NULL OR f.status = sqlc.narg(status)::text)
 ORDER BY (f.status = 'open') DESC, f.created_at
 LIMIT sqlc.arg(page_size);

-- name: AdminResolveFlag :one
UPDATE opportunity_flag
   SET status = sqlc.arg(status), resolution_note = sqlc.narg(resolution_note),
       resolved_by = sqlc.arg(resolved_by), resolved_at = now()
 WHERE id = sqlc.arg(flag_id) AND status = 'open'
RETURNING id, opportunity_id, status;

-- name: CountOpenFlags :one
SELECT count(*)::bigint FROM opportunity_flag WHERE status = 'open';

-- --------------------------------------------------------------- re-runs

-- name: AdminRequeueSource :execrows
-- Re-run controls. Sends every posting from one source back to a chosen pipeline
-- state so it is re-parsed, re-extracted or re-embedded.
--
-- Resets attempts and clears the lease, because a record that already exhausted
-- its retries would otherwise sit in the new state and never be claimed. Merged
-- rows are excluded: they are not independently processed.
UPDATE opportunity o
   SET pipeline_state = sqlc.arg(target_state),
       attempts = 0, last_error = NULL, next_attempt_at = now(), lease_until = NULL
 WHERE o.merged_into IS NULL
   AND EXISTS (SELECT 1 FROM opportunity_source os
                WHERE os.opportunity_id = o.id AND os.source_id = sqlc.arg(source_id));

-- name: AdminRequeueOpportunity :execrows
UPDATE opportunity
   SET pipeline_state = sqlc.arg(target_state),
       attempts = 0, last_error = NULL, next_attempt_at = now(), lease_until = NULL
 WHERE id = sqlc.arg(opportunity_id) AND merged_into IS NULL;

-- ---------------------------------------------------------------- purge

-- name: AdminCountSourcePostings :one
-- What a source purge would remove, counted before it runs.
SELECT count(*)::bigint AS total,
       count(*) FILTER (WHERE o.merged_into IS NOT NULL)::bigint AS merged,
       count(*) FILTER (WHERE EXISTS (
           SELECT 1 FROM opportunity_source os2
            WHERE os2.opportunity_id = o.id AND os2.source_id <> sqlc.arg(source_id)
       ))::bigint AS also_seen_elsewhere
  FROM opportunity o
 WHERE EXISTS (SELECT 1 FROM opportunity_source os
                WHERE os.opportunity_id = o.id AND os.source_id = sqlc.arg(source_id));

-- name: AdminSourceOpportunityIDs :many
-- The postings this source contributed to, captured BEFORE its provenance rows
-- are deleted. Everything the purge may touch is bounded by this list.
SELECT DISTINCT os.opportunity_id
  FROM opportunity_source os
 WHERE os.source_id = sqlc.arg(source_id);

-- name: AdminDeleteSourceRows :execrows
-- Drops this source's provenance rows only.
DELETE FROM opportunity_source WHERE source_id = sqlc.arg(source_id);

-- name: AdminDeleteMergeRecordsFor :execrows
-- Merge records referencing postings the purge is about to remove.
--
-- Deleted explicitly rather than by cascade, and counted, so the purge report
-- says how much merge history went with the source. A foreign key with no ON
-- DELETE clause would otherwise block the purge entirely, which is how "retire
-- this source" turns into an afternoon of manual SQL.
DELETE FROM opportunity_merge
 WHERE from_opportunity_id = ANY (sqlc.arg(opportunity_ids)::uuid[])
    OR into_opportunity_id = ANY (sqlc.arg(opportunity_ids)::uuid[]);

-- name: AdminDeleteOrphanedAmong :execrows
-- Removes postings left with no provenance at all, from the bounded candidate set.
--
-- Scoped to the ids this source contributed to, NOT table-wide. A table-wide
-- orphan sweep would delete unrelated postings as a side effect of purging one
-- source: cleanup with unbounded blast radius, which is the kind of helpfulness
-- that causes the second incident.
--
-- Two steps rather than one cascading delete, because a posting seen on a second
-- source must SURVIVE the purge of the first. Deleting by source would take those
-- with it, which is data loss disguised as cleanup.
DELETE FROM opportunity o
 WHERE o.id = ANY (sqlc.arg(opportunity_ids)::uuid[])
   AND NOT EXISTS (SELECT 1 FROM opportunity_source os WHERE os.opportunity_id = o.id);
