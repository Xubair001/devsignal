-- name: RecordSourceHealth :exec
-- Accumulates within the day. Counters rather than snapshots, so a source polled
-- every five minutes builds one comparable row per day.
INSERT INTO source_health_daily (
    source_id, day, polls, poll_failures, not_modified,
    postings_seen, postings_usable,
    with_company, with_location, with_apply_url, with_language, with_salary
) VALUES (
    sqlc.arg(source_id), CURRENT_DATE, sqlc.arg(polls), sqlc.arg(poll_failures),
    sqlc.arg(not_modified), sqlc.arg(postings_seen), sqlc.arg(postings_usable),
    sqlc.arg(with_company), sqlc.arg(with_location), sqlc.arg(with_apply_url),
    sqlc.arg(with_language), sqlc.arg(with_salary)
)
ON CONFLICT (source_id, day) DO UPDATE SET
    polls           = source_health_daily.polls + EXCLUDED.polls,
    poll_failures   = source_health_daily.poll_failures + EXCLUDED.poll_failures,
    not_modified    = source_health_daily.not_modified + EXCLUDED.not_modified,
    postings_seen   = source_health_daily.postings_seen + EXCLUDED.postings_seen,
    postings_usable = source_health_daily.postings_usable + EXCLUDED.postings_usable,
    with_company    = source_health_daily.with_company + EXCLUDED.with_company,
    with_location   = source_health_daily.with_location + EXCLUDED.with_location,
    with_apply_url  = source_health_daily.with_apply_url + EXCLUDED.with_apply_url,
    with_language   = source_health_daily.with_language + EXCLUDED.with_language,
    with_salary     = source_health_daily.with_salary + EXCLUDED.with_salary,
    updated_at      = now();

-- name: TodaySourceHealth :one
SELECT postings_seen, postings_usable, with_company, with_location,
       with_apply_url, with_language, with_salary
  FROM source_health_daily
 WHERE source_id = $1 AND day = CURRENT_DATE;

-- name: BaselineSourceHealth :one
-- The source's OWN recent past, excluding today. Summed across the window rather
-- than averaged per day: a quiet day would otherwise carry the same weight as a
-- busy one and make the baseline swing.
SELECT coalesce(sum(postings_seen), 0)::int   AS postings_seen,
       coalesce(sum(postings_usable), 0)::int AS postings_usable,
       coalesce(sum(with_company), 0)::int    AS with_company,
       coalesce(sum(with_location), 0)::int   AS with_location,
       coalesce(sum(with_apply_url), 0)::int  AS with_apply_url,
       coalesce(sum(with_language), 0)::int   AS with_language,
       coalesce(sum(with_salary), 0)::int     AS with_salary
  FROM source_health_daily
 WHERE source_id = sqlc.arg(source_id)
   AND day <  CURRENT_DATE
   AND day >= CURRENT_DATE - sqlc.arg(window_days)::int;

-- name: SetSourceDegraded :exec
UPDATE source
   SET consecutive_degraded = consecutive_degraded + 1,
       last_health_note = sqlc.arg(note)
 WHERE id = sqlc.arg(id);

-- name: ClearSourceDegraded :exec
UPDATE source
   SET consecutive_degraded = 0, last_health_note = NULL
 WHERE id = sqlc.arg(id) AND consecutive_degraded <> 0;

-- name: QuarantineDegradedSource :exec
UPDATE source
   SET status = 'quarantined', last_health_note = sqlc.arg(note)
 WHERE id = sqlc.arg(id) AND status = 'active';

-- name: SourceHealthReport :many
-- Operator view: one row per source with today against its baseline window.
SELECT s.name, s.tier, s.status, s.consecutive_degraded, s.last_health_note,
       s.last_success_at, s.last_failure_at,
       coalesce(t.postings_seen, 0)::int   AS today_seen,
       coalesce(t.postings_usable, 0)::int AS today_usable,
       coalesce(t.with_location, 0)::int   AS today_with_location,
       coalesce(b.seen, 0)::int            AS baseline_seen,
       coalesce(b.usable, 0)::int          AS baseline_usable,
       coalesce(b.loc, 0)::int             AS baseline_with_location
  FROM source s
  LEFT JOIN source_health_daily t ON t.source_id = s.id AND t.day = CURRENT_DATE
  LEFT JOIN (
      SELECT source_id,
             sum(postings_seen) AS seen,
             sum(postings_usable) AS usable,
             sum(with_location) AS loc
        FROM source_health_daily
       WHERE day < CURRENT_DATE AND day >= CURRENT_DATE - 7
       GROUP BY source_id
  ) b ON b.source_id = s.id
 ORDER BY s.status, s.name;

-- name: ListActiveSourceIDs :many
SELECT id, name FROM source WHERE status = 'active' ORDER BY name;

-- name: SetSourceReview :exec
-- Records who reviewed the platform this source inherits its legal basis from.
UPDATE source
   SET reviewed_by = sqlc.arg(reviewed_by),
       terms_reviewed_at = now(),
       platform_review_ref = sqlc.arg(platform_review_ref)
 WHERE id = sqlc.arg(id);
