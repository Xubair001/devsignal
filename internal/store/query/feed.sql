-- name: ListOpportunities :many
-- Keyset pagination, never OFFSET: ingestion never stops, so an offset skips or
-- duplicates rows between pages as data shifts underneath. (first_seen_at, id)
-- is the sort key, with id as the tiebreaker so the order is total.
SELECT o.id, o.title_raw, o.role_family, o.seniority_ordinal, o.is_management,
       o.work_mode, o.location_country, o.location_city, o.remote_geo_scope,
       o.language, o.apply_method,
       o.salary_min_minor, o.salary_max_minor, o.salary_currency,
       o.salary_period, o.salary_is_estimated,
       o.visa_sponsorship,
       o.first_seen_at, o.last_seen_at, o.liveness_checked_at,
       o.source_reported_posted_at, o.repost_count,
       c.display_name AS company_name, c.canonical_domain, c.domain_confirmed,
       (SELECT s.apply_url FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.apply_url IS NOT NULL LIMIT 1) AS apply_url,
       (SELECT count(*) FROM opportunity_source s WHERE s.opportunity_id = o.id) AS source_count
  FROM opportunity o
  JOIN company c ON c.id = o.company_id
 WHERE o.pipeline_state = 'ready'
   -- Serving excludes anything closed or merged away. A closed posting must
   -- never appear, and a merged one is represented by its canonical row.
   AND o.closed_at IS NULL
   AND o.merged_into IS NULL
   AND (sqlc.narg(role_family)::text IS NULL OR o.role_family = sqlc.narg(role_family)::text)
   AND (sqlc.narg(work_mode)::text IS NULL OR o.work_mode = sqlc.narg(work_mode)::text)
   AND (sqlc.narg(country)::text IS NULL
        OR o.location_country = sqlc.narg(country)::text
        OR o.remote_geo_scope LIKE '%' || sqlc.narg(country)::text || '%')
   AND (sqlc.narg(after_seen)::timestamptz IS NULL
        OR (o.first_seen_at, o.id) < (sqlc.narg(after_seen)::timestamptz, sqlc.narg(after_id)::uuid))
 ORDER BY o.first_seen_at DESC, o.id DESC
 LIMIT sqlc.arg(page_size)::int;

-- name: GetOpportunityDetail :one
SELECT o.id, o.company_id, o.title_raw, o.description_text, o.role_family, o.seniority_ordinal,
       o.is_management, o.work_mode, o.location_country, o.location_city,
       o.remote_geo_scope, o.language, o.apply_method,
       o.salary_min_minor, o.salary_max_minor, o.salary_currency,
       o.salary_period, o.salary_is_estimated, o.visa_sponsorship,
       o.first_seen_at, o.last_seen_at, o.liveness_checked_at,
       o.source_reported_posted_at, o.repost_count, o.closed_at, o.close_reason,
       c.display_name AS company_name, c.canonical_domain, c.domain_confirmed,
       (SELECT s.apply_url FROM opportunity_source s
         WHERE s.opportunity_id = o.id AND s.apply_url IS NOT NULL LIMIT 1) AS apply_url,
       (SELECT count(*) FROM opportunity_source s WHERE s.opportunity_id = o.id) AS source_count
  FROM opportunity o
  JOIN company c ON c.id = o.company_id
 WHERE o.id = $1 AND o.merged_into IS NULL;

-- name: CompanyMedianDaysToClose :one
-- The company's own hiring pace, used as the baseline for ghost risk. Relative
-- beats absolute: a 90-day posting is unremarkable at a company that always
-- takes 90 days. Returns 0 when there is not enough history, and an unknown
-- baseline must never manufacture suspicion.
SELECT coalesce(
    percentile_cont(0.5) WITHIN GROUP (
        ORDER BY EXTRACT(EPOCH FROM (closed_at - first_seen_at)) / 86400.0
    ), 0)::int AS median_days
  FROM opportunity
 WHERE company_id = $1 AND closed_at IS NOT NULL AND merged_into IS NULL;

-- name: CountOpenSimilarRoles :one
-- Observable competition proxy. NOT an applicant count — we have none, and
-- inventing one would discredit every honest field beside it (blueprint §3).
SELECT count(*) FROM opportunity
 WHERE company_id = $1 AND role_family = $2
   AND closed_at IS NULL AND merged_into IS NULL AND pipeline_state = 'ready';
