-- name: RecordEngagement :one
-- Append one event. Never an upsert: the log is the decision record and the
-- reversal of an action is itself a label worth keeping.
INSERT INTO engagement_event (
    user_id, opportunity_id, event_type, dismiss_reason,
    fit_score_at_event, max_possible_at_event, factor_breakdown,
    weights_version, embedding_version, profile_version, opportunity_version
) VALUES (
    sqlc.arg(user_id), sqlc.arg(opportunity_id), sqlc.arg(event_type),
    sqlc.narg(dismiss_reason), sqlc.narg(fit_score_at_event),
    sqlc.narg(max_possible_at_event), sqlc.narg(factor_breakdown),
    sqlc.narg(weights_version), sqlc.narg(embedding_version),
    sqlc.narg(profile_version), sqlc.narg(opportunity_version)
)
RETURNING id, occurred_at;

-- name: GetEngagementState :many
-- Current state per opportunity for one user, derived from the log.
--
-- Derived rather than stored separately: a second table holding "is_saved" would
-- be a cache of this, and a cache that can disagree with its source is how a user
-- ends up seeing a role they dismissed.
--
-- Saving is reversible, so its state is the LATEST of saved/unsaved. Applying and
-- dismissing are not reversible in v1, so any such event stands.
SELECT e.opportunity_id,
       -- Casts are load-bearing: without them sqlc cannot infer these
       -- expressions' types and emits interface{}.
       coalesce(
         (array_agg(e.event_type ORDER BY e.occurred_at DESC)
            FILTER (WHERE e.event_type IN ('saved', 'unsaved')))[1] = 'saved',
         false)::boolean AS saved,
       bool_or(e.event_type = 'applied')   AS applied,
       bool_or(e.event_type = 'dismissed') AS dismissed,
       (max(e.occurred_at) FILTER (WHERE e.event_type = 'applied'))::timestamptz AS applied_at
  FROM engagement_event e
 WHERE e.user_id = sqlc.arg(user_id)
 GROUP BY e.opportunity_id;

-- name: CountShownDays :many
-- Distinct days each posting was shown to this user, for the saturation term in
-- priority.
--
-- Distinct DAYS, not raw impressions: a user who refreshes the feed twenty times
-- has not ignored a role twenty times, and counting events would bury everything
-- they looked at.
SELECT opportunity_id, count(DISTINCT date_trunc('day', occurred_at))::int AS days_shown
  FROM engagement_event
 WHERE user_id = sqlc.arg(user_id) AND event_type = 'shown'
 GROUP BY opportunity_id;

-- name: ListSavedOpportunities :many
-- Saved postings, most recently saved first. Keyset-paginated on (occurred_at, id).
WITH latest AS (
    SELECT opportunity_id,
           (max(occurred_at) FILTER (WHERE event_type = 'saved'))::timestamptz   AS saved_at,
           (max(occurred_at) FILTER (WHERE event_type = 'unsaved'))::timestamptz AS unsaved_at
      FROM engagement_event
     WHERE user_id = sqlc.arg(user_id) AND event_type IN ('saved','unsaved')
     GROUP BY opportunity_id
)
SELECT l.opportunity_id, l.saved_at
  FROM latest l
 WHERE l.saved_at IS NOT NULL
   AND (l.unsaved_at IS NULL OR l.unsaved_at < l.saved_at)
   AND (sqlc.narg(before)::timestamptz IS NULL OR l.saved_at < sqlc.narg(before)::timestamptz)
 ORDER BY l.saved_at DESC
 LIMIT sqlc.arg(page_size);

-- name: CountDismissReasons :many
-- The most valuable feedback in the system: a dismissal reason is a correction to
-- a specific factor, not a generic negative.
SELECT dismiss_reason, count(*)::int AS n
  FROM engagement_event
 WHERE event_type = 'dismissed' AND dismiss_reason IS NOT NULL
   AND (sqlc.narg(user_id)::uuid IS NULL
        OR engagement_event.user_id = sqlc.narg(user_id)::uuid)
 GROUP BY 1 ORDER BY 2 DESC;

-- name: ListEngagementLabels :many
-- The behavioural evaluation set. Replaces the rubric labels in internal/eval
-- once there is enough of it.
--
-- Returns the ATS identity rather than the opportunity id, because a label keyed
-- on a local UUID cannot travel between environments — the same reason the frozen
-- eval fixtures use (ats_type, ats_job_id).
SELECT e.user_id, os.ats_type, os.ats_job_id, e.event_type, e.dismiss_reason,
       e.fit_score_at_event, e.weights_version, e.occurred_at
  FROM engagement_event e
  JOIN opportunity_source os ON os.opportunity_id = e.opportunity_id
 WHERE e.event_type IN ('saved','applied','dismissed')
   AND e.occurred_at >= sqlc.arg(since)
 ORDER BY e.occurred_at DESC
 LIMIT sqlc.arg(page_size);

-- name: DeleteEngagementEvents :execrows
-- Erasure. Enumerated so the report states a count for this store.
DELETE FROM engagement_event WHERE user_id = sqlc.arg(user_id);
