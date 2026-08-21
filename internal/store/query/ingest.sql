-- name: UpsertCompanyByDomain :one
-- Resolution is on the registrable domain, never the name: "Google" /
-- "Google LLC" / "Alphabet" / "google.com" are one employer with four spellings.
INSERT INTO company (canonical_domain, display_name)
VALUES ($1, $2)
ON CONFLICT (canonical_domain) DO UPDATE
   SET display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), company.display_name)
RETURNING *;

-- name: FindSourceRow :one
SELECT * FROM opportunity_source
 WHERE source_id = $1 AND source_job_id = $2;

-- name: FindCanonicalByATS :one
-- For Tier-A sources this pair is a stable global identifier, which is what
-- makes dedup nearly free: the same posting seen through another source links to
-- the existing canonical row instead of creating a duplicate.
SELECT opportunity_id FROM opportunity_source
 WHERE ats_type = $1 AND ats_job_id = $2
 LIMIT 1;

-- name: InsertOpportunityFromPosting :one
INSERT INTO opportunity (
    company_id, title_raw, title_normalized, description_text,
    work_mode, location_region, language, apply_method, ats_type,
    source_reported_posted_at, content_hash, source_posted_at_at_last_change,
    liveness_checked_at, pipeline_state
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$10,
    -- Seeing a posting for the first time IS a verification. Leaving this NULL
    -- made "verified open, checked N ago" unanswerable for every new posting.
    now(),'parsed')
RETURNING *;

-- name: UpdateOpportunityFromPosting :execrows
-- Content changed, so the record re-enters the pipeline at 'parsed'. Version is
-- bumped, which is what invalidates any cached fit score for it.
UPDATE opportunity
   SET title_raw = $2, title_normalized = $3, description_text = $4,
       work_mode = $5, location_region = $6, language = $7,
       source_reported_posted_at = $8, content_hash = $9,
       -- Baseline for refresh detection: the date they claimed at the moment the
       -- content last actually changed.
       source_posted_at_at_last_change = $8,
       pipeline_state = 'parsed', next_attempt_at = now(),
       attempts = 0, last_error = NULL,
       version = version + 1,
       last_seen_at = now(), liveness_checked_at = now(),
       consecutive_misses = 0, closed_at = NULL, close_reason = NULL
 WHERE id = $1;

-- name: MarkOpportunitySeen :execrows
-- Unchanged content, but observed. Reopens a record that a flaky poll had
-- closed: seeing it again is stronger evidence than having missed it.
--
-- Also detects a refresh: identical content but the source moved its own
-- posted-at forward. That is the strongest observable ghost signal, and it is
-- only visible here — at the one moment we know the content did NOT change.
UPDATE opportunity
   SET last_seen_at = now(), liveness_checked_at = now(),
       consecutive_misses = 0, closed_at = NULL, close_reason = NULL,
       repost_count = repost_count + CASE
           WHEN sqlc.arg(source_posted_at)::timestamptz IS NOT NULL
            AND source_posted_at_at_last_change IS NOT NULL
            AND sqlc.arg(source_posted_at)::timestamptz > source_posted_at_at_last_change
           THEN 1 ELSE 0 END,
       source_reported_posted_at = COALESCE(sqlc.arg(source_posted_at)::timestamptz,
                                            source_reported_posted_at)
 WHERE id = sqlc.arg(id);

-- name: InsertSourceRow :one
INSERT INTO opportunity_source (
    opportunity_id, source_id, source_job_id, ats_type, ats_job_id,
    apply_url, raw_object_key, content_hash, etag, merge_reason, merge_confidence, merged_by
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
RETURNING *;

-- name: TouchSourceRow :exec
UPDATE opportunity_source
   SET last_seen_at = now(), content_hash = $2, etag = $3, raw_object_key = $4
 WHERE id = $1;

-- name: BumpMissesForAbsent :execrows
-- Absence-based liveness. Called ONLY after a successful poll: inferring closure
-- from a failed fetch would let one source outage delete the corpus.
UPDATE opportunity o
   SET consecutive_misses = o.consecutive_misses + 1,
       liveness_checked_at = now()
 WHERE o.closed_at IS NULL
   AND EXISTS (SELECT 1 FROM opportunity_source s
                WHERE s.opportunity_id = o.id AND s.source_id = sqlc.arg(source_id))
   AND NOT EXISTS (SELECT 1 FROM opportunity_source s
                    WHERE s.opportunity_id = o.id
                      AND s.source_id = sqlc.arg(source_id)
                      AND s.source_job_id = ANY(sqlc.arg(seen_ids)::text[]));

-- name: CloseMissedOpportunities :execrows
UPDATE opportunity
   SET closed_at = now(), close_reason = 'absent'
 WHERE closed_at IS NULL
   AND consecutive_misses >= sqlc.arg(max_misses)::int;

-- name: UpsertSource :one
INSERT INTO source (name, tier, type, legal_basis, poll_interval, etag_supported,
                    robots_checked_at, terms_reviewed_at, reviewed_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (name) DO UPDATE SET type = EXCLUDED.type
RETURNING *;

-- name: GetSourceByName :one
SELECT * FROM source WHERE name = $1;

-- name: RecordSourceSuccess :exec
UPDATE source
   SET last_success_at = now(), last_error = NULL,
       items_discovered = items_discovered + sqlc.arg(discovered)::bigint,
       items_processed  = items_processed  + sqlc.arg(processed)::bigint,
       parse_yield_7d   = sqlc.arg(yield)
 WHERE id = sqlc.arg(id);

-- name: RecordSourceFailure :exec
UPDATE source SET last_failure_at = now(), last_error = $2 WHERE id = $1;

-- name: QuarantineSource :exec
UPDATE source SET status = 'quarantined', last_error = $2 WHERE id = $1;

-- name: UpsertSourceSchedule :exec
INSERT INTO source_schedule (source_id, next_run_at, cursor)
VALUES ($1, now(), $2)
ON CONFLICT (source_id) DO UPDATE
   SET next_run_at = EXCLUDED.next_run_at, cursor = EXCLUDED.cursor;

-- name: SaveSourceCursor :exec
UPDATE source_schedule
   SET cursor = $2, last_run_at = now(), lease_until = NULL,
       next_run_at = now() + (SELECT poll_interval FROM source WHERE id = $1)
 WHERE source_id = $1;

-- name: ClaimDueSources :many
-- Same SKIP LOCKED pattern as the pipeline: no scheduler process, so no single
-- point of failure and no double-firing.
UPDATE source_schedule
   SET lease_until = now() + sqlc.arg(lease)::interval
 WHERE source_id IN (
   SELECT ss.source_id FROM source_schedule ss
     JOIN source s ON s.id = ss.source_id
    WHERE ss.next_run_at <= now()
      AND (ss.lease_until IS NULL OR ss.lease_until < now())
      AND s.status = 'active'
    ORDER BY ss.next_run_at
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch)::int
 )
 RETURNING source_id, cursor;

-- name: CountLiveOpportunitiesForSource :one
SELECT count(*) FROM opportunity o
 WHERE o.closed_at IS NULL
   AND EXISTS (SELECT 1 FROM opportunity_source s
                WHERE s.opportunity_id = o.id AND s.source_id = $1);

-- name: GetSourceByID :one
SELECT * FROM source WHERE id = $1;

-- name: GetSourceCursor :one
-- Read the cursor directly, independent of whether the source is due. A manual
-- re-run must still send If-None-Match, or it re-downloads the whole board.
SELECT cursor FROM source_schedule WHERE source_id = $1;
