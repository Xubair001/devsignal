-- Service level indicators derivable from stored data.
--
-- Latency and availability come from metrics, because the request that already
-- returned left no row behind. Everything here is a property of the corpus, which
-- the database can answer exactly rather than approximately.

-- name: SLIPipelineBacklog :one
-- Records stranded in a non-terminal state past the threshold.
--
-- state_entered_at, not updated_at: any write resets updated_at, so a record being
-- touched repeatedly while making no progress would look fresh. That distinction
-- was a real bug in the sweeper and it is the same one here.
SELECT count(*)::bigint AS stranded,
       coalesce(max(now() - o.state_entered_at), interval '0')::interval AS oldest
  FROM opportunity o
 WHERE o.pipeline_state <> 'ready'
   AND o.pipeline_state <> 'failed_permanent'
   AND o.merged_into IS NULL
   AND o.state_entered_at < now() - sqlc.arg(threshold)::interval;

-- name: SLIFreshness :one
-- Time from OUR first sight of a posting to it becoming visible.
--
-- first_seen_at is ours; the source's claimed publish date is not used, because
-- boards and employers refresh it so listings look fresh and it would make this
-- number a fiction we control. That means this measures pipeline latency rather
-- than true publish-to-visible, which is the honest thing we can measure.
--
-- state_entered_at is when the row reached 'ready', since ready is terminal and
-- nothing moves it afterwards.
SELECT count(*)::bigint AS sample,
       coalesce(
         percentile_disc(sqlc.arg(percentile)::float)
           WITHIN GROUP (ORDER BY (o.state_entered_at - o.first_seen_at)),
         interval '0')::interval AS observed
  FROM opportunity o
 WHERE o.pipeline_state = 'ready'
   AND o.merged_into IS NULL
   AND o.first_seen_at > now() - sqlc.arg(lookback)::interval
   AND o.state_entered_at >= o.first_seen_at;

-- name: SLIParseYield :many
-- Usable records over records seen, per source, over the window.
--
-- Per source rather than aggregated: parser rot affects one source at a time, and
-- an aggregate stays green while one board silently returns empty fields. That is
-- the failure blueprint §29 calls the largest ongoing operational cost.
SELECT s.id AS source_id, s.name,
       coalesce(sum(h.postings_seen), 0)::bigint   AS seen,
       coalesce(sum(h.postings_usable), 0)::bigint AS usable
  FROM source s
  LEFT JOIN source_health_daily h
    ON h.source_id = s.id AND h.day > (now() - sqlc.arg(lookback)::interval)::date
 WHERE s.status = 'active'
 GROUP BY s.id, s.name
 ORDER BY s.name;

-- name: SLIExtractionValidity :one
-- Extractions that produced a usable normalized document, over all attempts.
--
-- A row with a null `normalized` is one the model returned but validation
-- rejected, which is exactly what this objective is about. Counting only rows
-- that succeeded would make the ratio 1.0 by construction.
SELECT count(*)::bigint AS total,
       count(*) FILTER (WHERE e.normalized IS NOT NULL)::bigint AS valid
  FROM extraction e
 WHERE e.created_at > now() - sqlc.arg(lookback)::interval;

-- name: SLILivenessFreshness :one
-- How recently the visible corpus was verified.
--
-- NOT the liveness accuracy objective, and named so it cannot be mistaken for it.
-- Accuracy asks whether a role is genuinely open, which needs the employer's
-- answer. This asks when we last checked, which we do know. Reporting this under
-- the accuracy objective would be the invented-signal failure applied to our own
-- dashboard.
SELECT count(*)::bigint AS shown,
       count(*) FILTER (WHERE o.liveness_checked_at > now() - sqlc.arg(threshold)::interval)::bigint
         AS checked_recently,
       coalesce(max(now() - o.liveness_checked_at), interval '0')::interval AS oldest_check
  FROM opportunity o
 WHERE o.pipeline_state = 'ready' AND o.merged_into IS NULL AND o.closed_at IS NULL;

-- name: SLIPipelineStateDistribution :many
-- The state distribution IS the pipeline dashboard (CLAUDE.md). Returned with the
-- oldest entry per state, because a large count that is moving is healthy and a
-- small one that is not is an incident.
SELECT o.pipeline_state,
       count(*)::bigint AS records,
       coalesce(min(o.state_entered_at), now())::timestamptz AS oldest_entered
  FROM opportunity o
 WHERE o.merged_into IS NULL
 GROUP BY o.pipeline_state
 ORDER BY 2 DESC;
