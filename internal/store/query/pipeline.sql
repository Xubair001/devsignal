-- name: CreateOpportunity :one
INSERT INTO opportunity (company_id, title_raw, title_normalized, pipeline_state, content_hash)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ClaimBatch :many
-- FOR UPDATE SKIP LOCKED is what makes this safe with N workers and no
-- coordinator: each worker skips rows another has locked instead of blocking.
--
-- The lease means a hard-killed worker self-heals: once lease_until passes, the
-- row becomes claimable again without any cleanup step.
UPDATE opportunity
   SET lease_until = now() + sqlc.arg(lease)::interval,
       attempts    = attempts + 1
 WHERE id IN (
   SELECT o.id FROM opportunity o
    WHERE o.pipeline_state = sqlc.arg(state)
      -- A merged row has left the pipeline: it is represented by its canonical
      -- row and must never be claimed or swept again.
      AND o.merged_into IS NULL
      AND o.next_attempt_at <= now()
      AND (o.lease_until IS NULL OR o.lease_until < now())
    ORDER BY o.next_attempt_at
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch)::int
 )
 RETURNING id, version, attempts, pipeline_state;

-- name: AdvanceState :execrows
-- Optimistic concurrency: the version must match what the worker read. Zero rows
-- affected means another stage wrote first, so the caller reloads and retries
-- rather than clobbering.
UPDATE opportunity
   SET pipeline_state = sqlc.arg(next_state),
       version        = version + 1,
       lease_until    = NULL,
       attempts       = 0,
       last_error     = NULL,
       next_attempt_at = now()
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND pipeline_state = sqlc.arg(current_state);

-- name: FailAttempt :execrows
-- Backoff, then park. A parked record is visible in a queue; a lost one is not.
UPDATE opportunity
   SET last_error      = sqlc.arg(err),
       lease_until     = NULL,
       next_attempt_at = now() + sqlc.arg(backoff)::interval,
       pipeline_state  = CASE WHEN attempts >= sqlc.arg(max_attempts)::int
                              THEN 'failed_permanent'
                              ELSE pipeline_state END
 WHERE id = sqlc.arg(id);

-- name: ReleaseClaim :exec
-- Called on graceful shutdown so a clean deploy is fast instead of waiting out
-- the lease. The lease is the safety net; this is the courtesy.
UPDATE opportunity
   SET lease_until = NULL, attempts = GREATEST(attempts - 1, 0)
 WHERE id = $1 AND lease_until IS NOT NULL;

-- name: SweepStranded :many
-- Anything sitting in a non-terminal state past its threshold with no live
-- lease. This is the query that turns a lost event into latency instead of
-- silent data loss.
SELECT id, pipeline_state, attempts, state_entered_at
  FROM opportunity
 WHERE pipeline_state NOT IN ('ready','failed_permanent')
   AND merged_into IS NULL
   AND (lease_until IS NULL OR lease_until < now())
   AND state_entered_at < now() - sqlc.arg(threshold)::interval
 -- swept_at is the progress cursor: never-swept rows first, so a backlog larger
 -- than one batch is worked through instead of the same head being requeued.
 ORDER BY swept_at NULLS FIRST, state_entered_at
 LIMIT sqlc.arg(batch)::int;

-- name: RequeueStranded :execrows
UPDATE opportunity
   SET next_attempt_at = now(), lease_until = NULL, swept_at = now()
 WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: PipelineStats :many
-- The pipeline dashboard. An old min(updated_at) in any row means something is
-- stranded — alert on it.
SELECT pipeline_state, count(*) AS total, min(state_entered_at) AS oldest
  FROM opportunity
 WHERE merged_into IS NULL
 GROUP BY pipeline_state
 ORDER BY pipeline_state;

-- name: GetOpportunityState :one
SELECT id, pipeline_state, version, attempts, last_error, lease_until, next_attempt_at
  FROM opportunity WHERE id = $1;

-- name: DeleteOpportunitiesForCompany :execrows
DELETE FROM opportunity WHERE company_id = $1;

-- name: DeferItem :exec
-- Put an item back WITHOUT spending an attempt. ClaimBatch already incremented
-- attempts on the way in, so it is decremented here: a systemic failure must not
-- consume a budget that exists for individually-bad records.
UPDATE opportunity
   SET next_attempt_at = now() + sqlc.arg(delay)::interval,
       lease_until = NULL,
       attempts = GREATEST(attempts - 1, 0)
 WHERE id = sqlc.arg(id);
