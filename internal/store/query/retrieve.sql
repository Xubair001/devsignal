-- Stage 1 of the two-stage matcher (blueprint §18). Bounds the work for
-- everything downstream: without it, scoring is the full user x opportunity
-- cross product.
--
-- Two recall channels, both carrying the same hard predicates. The predicates
-- are inside each channel rather than applied to its output, because filtering
-- after a kNN walk discards candidates the walk already spent its budget on:
-- measured on 50k vectors with a 1%-selective predicate, asking for 100 returned
-- 4. The caller sets hnsw.iterative_scan for the same reason.
--
-- Nothing here ranks. Ordering inside a channel only decides what survives the
-- cap; the fit score in step 15 is the only thing that ranks.

-- name: RetrieveByVector :many
-- Channel 1: nearest neighbours to the profile vector among eligible postings.
--
-- The planner is deliberately left to choose its own scan. Forcing the index
-- makes selective predicates under-return; left alone it uses an exact scan when
-- the eligible set is small (correct, and cheap precisely because it is small)
-- and the HNSW index when it is large.
SELECT o.id,
       o.title_raw,
       o.company_id,
       o.first_seen_at,
       -- Cast is load-bearing: without it sqlc cannot infer the operator's
       -- return type and emits interface{}.
       (e.embedding <=> sqlc.arg(query_vector)::vector)::double precision AS distance
  FROM opportunity o
  JOIN opportunity_embedding e
    ON e.opportunity_id = o.id
   AND e.embedding_version = sqlc.arg(embedding_version)
 WHERE o.pipeline_state = 'ready'
   AND o.merged_into IS NULL
   AND o.closed_at IS NULL
   -- Empty array means the user set no constraint, which must not exclude
   -- everything. Postgres has no "match anything" array, so each predicate is
   -- written as "unconstrained OR matches".
   AND (cardinality(sqlc.arg(countries)::char(2)[]) = 0
        OR o.location_country = ANY (sqlc.arg(countries)::char(2)[])
        -- A remote posting is not excluded by a country filter: its location is
        -- a formality, and dropping it would hide the roles remote-seeking users
        -- most want.
        OR o.work_mode = 'remote')
   AND (sqlc.narg(work_mode)::text IS NULL
        OR o.work_mode = sqlc.narg(work_mode)::text
        -- Asking for remote accepts hybrid; asking for hybrid does not accept
        -- onsite. The asymmetry is the point: hybrid is a superset of remote days.
        OR (sqlc.narg(work_mode)::text = 'hybrid' AND o.work_mode = 'remote'))
   AND (cardinality(sqlc.arg(employment_types)::text[]) = 0
        OR o.employment_type IS NULL
        OR o.employment_type = ANY (sqlc.arg(employment_types)::text[]))
   AND (cardinality(sqlc.arg(languages)::char(2)[]) = 0
        OR o.language IS NULL
        OR o.language = ANY (sqlc.arg(languages)::char(2)[]))
   AND o.last_seen_at > now() - sqlc.arg(max_staleness)::interval
 ORDER BY e.embedding <=> sqlc.arg(query_vector)::vector
 LIMIT sqlc.arg(max_candidates);

-- name: RetrieveByKeyword :many
-- Channel 2: full-text recall on the user's target-role terms, over the TITLE.
--
-- It exists because the vector channel and this one fail differently. A lexical
-- embedding scores shared boilerplate as similarity, so a posting whose title
-- carries the user's exact role words can still rank below unrelated postings
-- from the same company; an exact term match on the title cannot miss it.
--
-- Title only, deliberately. Measured on 199 real GitLab postings with the terms
-- "backend OR platform": matching title plus description returned all 199,
-- because the company names its own platform in every description. Matching the
-- title returned 34, all genuine backend or platform roles. A channel that
-- returns the whole corpus bounds nothing, and bounding is what stage 1 is for.
--
-- Description text is not ignored by retrieval — the vector channel embeds it.
-- The division of labour is deliberate: exact matching for the broad, boilerplate
-- prone role words, and the vector for everything the description implies.
SELECT o.id,
       o.title_raw,
       o.company_id,
       o.first_seen_at,
       ts_rank(to_tsvector('english', coalesce(o.title_normalized, '')),
               websearch_to_tsquery('english', sqlc.arg(terms)::text))::double precision AS rank
  FROM opportunity o
 WHERE o.pipeline_state = 'ready'
   AND o.merged_into IS NULL
   AND o.closed_at IS NULL
   AND to_tsvector('english', coalesce(o.title_normalized, ''))
       @@ websearch_to_tsquery('english', sqlc.arg(terms)::text)
   AND (cardinality(sqlc.arg(countries)::char(2)[]) = 0
        OR o.location_country = ANY (sqlc.arg(countries)::char(2)[])
        OR o.work_mode = 'remote')
   AND (sqlc.narg(work_mode)::text IS NULL
        OR o.work_mode = sqlc.narg(work_mode)::text
        OR (sqlc.narg(work_mode)::text = 'hybrid' AND o.work_mode = 'remote'))
   AND (cardinality(sqlc.arg(employment_types)::text[]) = 0
        OR o.employment_type IS NULL
        OR o.employment_type = ANY (sqlc.arg(employment_types)::text[]))
   AND (cardinality(sqlc.arg(languages)::char(2)[]) = 0
        OR o.language IS NULL
        OR o.language = ANY (sqlc.arg(languages)::char(2)[]))
   AND o.last_seen_at > now() - sqlc.arg(max_staleness)::interval
 ORDER BY rank DESC, o.first_seen_at DESC
 LIMIT sqlc.arg(max_candidates);

-- name: CountEligibleOpportunities :one
-- The denominator for retrieval coverage (§21). Without it a channel returning
-- 12 candidates is indistinguishable from a corpus that only holds 12 eligible
-- postings, and silent under-return looks like a small market.
SELECT count(*)
  FROM opportunity o
 WHERE o.pipeline_state = 'ready'
   AND o.merged_into IS NULL
   AND o.closed_at IS NULL
   AND (cardinality(sqlc.arg(countries)::char(2)[]) = 0
        OR o.location_country = ANY (sqlc.arg(countries)::char(2)[])
        OR o.work_mode = 'remote')
   AND (sqlc.narg(work_mode)::text IS NULL
        OR o.work_mode = sqlc.narg(work_mode)::text
        OR (sqlc.narg(work_mode)::text = 'hybrid' AND o.work_mode = 'remote'))
   AND (cardinality(sqlc.arg(employment_types)::text[]) = 0
        OR o.employment_type IS NULL
        OR o.employment_type = ANY (sqlc.arg(employment_types)::text[]))
   AND (cardinality(sqlc.arg(languages)::char(2)[]) = 0
        OR o.language IS NULL
        OR o.language = ANY (sqlc.arg(languages)::char(2)[]))
   AND o.last_seen_at > now() - sqlc.arg(max_staleness)::interval;
