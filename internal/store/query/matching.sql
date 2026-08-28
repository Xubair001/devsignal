-- name: PutFitScore :exec
-- Cache one score. Keyed on every version it depends on, so a version change
-- writes a new row rather than overwriting a score computed under other rules.
INSERT INTO fit_score (
    user_id, opportunity_id, weights_version, profile_version,
    opportunity_version, embedding_version, score, max_possible, factors
) VALUES (
    sqlc.arg(user_id), sqlc.arg(opportunity_id), sqlc.arg(weights_version),
    sqlc.arg(profile_version), sqlc.arg(opportunity_version),
    sqlc.arg(embedding_version), sqlc.arg(score), sqlc.arg(max_possible),
    sqlc.arg(factors)
)
ON CONFLICT (user_id, opportunity_id, weights_version, profile_version,
             opportunity_version, embedding_version)
DO UPDATE SET score = excluded.score,
              max_possible = excluded.max_possible,
              factors = excluded.factors,
              computed_at = now();

-- name: GetFitScores :many
-- Cached scores for one user under one set of versions. The profile and
-- opportunity versions are checked per row, so a stale score is a cache MISS
-- rather than a wrong answer served fast.
SELECT f.opportunity_id, f.score, f.max_possible, f.factors
  FROM fit_score f
  JOIN opportunity o ON o.id = f.opportunity_id
 WHERE f.user_id = sqlc.arg(user_id)
   AND f.weights_version = sqlc.arg(weights_version)
   AND f.profile_version = sqlc.arg(profile_version)
   AND f.embedding_version = sqlc.arg(embedding_version)
   AND f.opportunity_version = o.version;

-- name: PutEligibilityResult :exec
INSERT INTO eligibility_result (
    user_id, opportunity_id, profile_version, opportunity_version,
    eligible, failed_checks
) VALUES (
    sqlc.arg(user_id), sqlc.arg(opportunity_id), sqlc.arg(profile_version),
    sqlc.arg(opportunity_version), sqlc.arg(eligible), sqlc.arg(failed_checks)
)
ON CONFLICT (user_id, opportunity_id, profile_version, opportunity_version)
DO UPDATE SET eligible = excluded.eligible,
              failed_checks = excluded.failed_checks,
              evaluated_at = now();

-- name: CountEligibilityFailuresByCheck :many
-- Operational view: which gate is excluding the most for one user.
--
-- A gate that quietly empties a feed is indistinguishable from an empty market,
-- so this is the query that tells them apart.
SELECT unnest(failed_checks) AS check_name, count(*) AS failures
  FROM eligibility_result
 WHERE user_id = sqlc.arg(user_id) AND NOT eligible
 GROUP BY 1
 ORDER BY 2 DESC;

-- name: DeleteFitScores :execrows
-- Erasure. Enumerated rather than left to the app_user cascade so the report can
-- state a count for this store.
DELETE FROM fit_score WHERE user_id = sqlc.arg(user_id);

-- name: DeleteEligibilityResults :execrows
DELETE FROM eligibility_result WHERE user_id = sqlc.arg(user_id);

-- name: GetOpportunitySkills :many
-- Extracted skills for a set of postings, split by requirement level. Batched
-- over the candidate set rather than queried per posting: 500 candidates would
-- otherwise be 500 round trips inside one feed request.
SELECT os.opportunity_id, os.skill_id, os.requirement_level
  FROM opportunity_skill os
 WHERE os.opportunity_id = ANY (sqlc.arg(opportunity_ids)::uuid[])
   AND os.requirement_level IN ('required', 'preferred');

-- name: GetProfileSkillIDs :many
SELECT ps.skill_id FROM profile_skill ps WHERE ps.user_id = sqlc.arg(user_id);

-- name: GetOpportunitiesForScoring :many
-- The candidate rows plus their vector distance from the profile, in one query.
--
-- The distance comes from the same embedding version retrieval used, because a
-- cosine value from another model is not a smaller or larger number, it is a
-- meaningless one.
SELECT sqlc.embed(o),
       (e.embedding <=> sqlc.arg(query_vector)::vector)::double precision AS distance
  FROM opportunity o
  LEFT JOIN opportunity_embedding e
    ON e.opportunity_id = o.id
   AND e.embedding_version = sqlc.arg(embedding_version)
 WHERE o.id = ANY (sqlc.arg(opportunity_ids)::uuid[]);

-- name: GetCachedFitScore :one
-- One cached score, for the engagement decision record.
--
-- Read from the cache rather than recomputed: the record must say what was SHOWN,
-- and re-running the matcher on every save would also mean a full retrieval and
-- scoring pass on a request that should be a single insert.
SELECT f.score, f.max_possible, f.factors, f.weights_version,
       f.embedding_version, f.profile_version, f.opportunity_version
  FROM fit_score f
  JOIN opportunity o ON o.id = f.opportunity_id
 WHERE f.user_id = sqlc.arg(user_id)
   AND f.opportunity_id = sqlc.arg(opportunity_id)
   AND f.weights_version = sqlc.arg(weights_version)
   AND f.opportunity_version = o.version
 ORDER BY f.computed_at DESC
 LIMIT 1;


-- name: PutFitScoreBatch :batchexec
-- The batched form of PutFitScore.
--
-- :batchexec makes sqlc emit a pgx.Batch, which pipelines every statement into
-- ONE network round trip. The per-candidate version was an N+1 write: a feed
-- request over 188 candidates issued 188 INSERTs, one round trip each, and the
-- load test measured 842ms for a single request. Nothing about the SQL changes —
-- only how many times we wait for the network.
INSERT INTO fit_score (
    user_id, opportunity_id, weights_version, profile_version,
    opportunity_version, embedding_version, score, max_possible, factors
) VALUES (
    sqlc.arg(user_id), sqlc.arg(opportunity_id), sqlc.arg(weights_version),
    sqlc.arg(profile_version), sqlc.arg(opportunity_version),
    sqlc.arg(embedding_version), sqlc.arg(score), sqlc.arg(max_possible),
    sqlc.arg(factors)
)
ON CONFLICT (user_id, opportunity_id, weights_version, profile_version,
             opportunity_version, embedding_version)
DO UPDATE SET score = excluded.score,
              max_possible = excluded.max_possible,
              factors = excluded.factors,
              computed_at = now();

-- name: PutEligibilityResultBatch :batchexec
-- The same batching for the eligibility audit trail.
INSERT INTO eligibility_result (
    user_id, opportunity_id, profile_version, opportunity_version,
    eligible, failed_checks
) VALUES (
    sqlc.arg(user_id), sqlc.arg(opportunity_id), sqlc.arg(profile_version),
    sqlc.arg(opportunity_version), sqlc.arg(eligible), sqlc.arg(failed_checks)
)
ON CONFLICT (user_id, opportunity_id, profile_version, opportunity_version)
DO UPDATE SET eligible = excluded.eligible,
              failed_checks = excluded.failed_checks,
              evaluated_at = now();

-- name: SkillGapsForUser :many
-- Which skills the roles this user is eligible for ask for, that they do not have.
--
-- Question four of the product's four: "what should I learn to be more
-- competitive". Answered here as ARITHMETIC OVER OBSERVED DATA and nothing else
-- — a count of postings, not an estimate of anyone's chances. We have no
-- applicant counts, so there is no competitiveness figure to give and inventing
-- one would discredit the honest numbers beside it (blueprint §3).
--
-- Scoped to the user's ELIGIBLE set rather than the whole corpus. A skill that
-- half the market wants is useless advice if every posting needing it fails this
-- person's work-authorization or location gate; the only roles worth naming a
-- gap for are the ones they could actually take.
--
-- TWO things this query has to get right about eligibility_result, both of which
-- it got wrong first:
--
--   1. Its primary key is (user_id, opportunity_id, profile_version,
--      opportunity_version), so it holds one row PER VERSION PAIR. Counting rows
--      counted 502 "roles" against a 265-posting corpus. Every count is over
--      DISTINCT opportunities.
--   2. A result computed against an older profile_version describes a gate the
--      user no longer has. Restricting to the CURRENT version is what keeps this
--      an analysis of who they are rather than who they were.
SELECT s.canonical_slug::text AS slug,
       s.display_name,
       count(DISTINCT o.id) FILTER (WHERE os.requirement_level = 'required')::bigint
           AS required_by,
       count(DISTINCT o.id) FILTER (WHERE os.requirement_level = 'preferred')::bigint
           AS preferred_by,
       -- Whether the vocabulary knows this skill. An extraction-invented phrase
       -- is not advice anyone can act on, so the caller can exclude it.
       (s.ontology_version LIKE 'seed-%')::boolean AS in_vocabulary
  FROM eligibility_result er
  JOIN profile p            ON p.user_id = er.user_id
                           AND p.profile_version = er.profile_version
  JOIN opportunity o        ON o.id = er.opportunity_id
  JOIN opportunity_skill os ON os.opportunity_id = o.id
  JOIN skill s              ON s.id = os.skill_id
 WHERE er.user_id = sqlc.arg(user_id)
   AND er.eligible
   AND o.pipeline_state = 'ready'
   AND o.closed_at IS NULL
   AND o.merged_into IS NULL
   AND os.requirement_level IN ('required', 'preferred')
   -- The gap is what they do NOT have. Origin is irrelevant: a skill claimed by
   -- hand and one read off a resume are both "has it".
   AND NOT EXISTS (
        SELECT 1 FROM profile_skill ps
         WHERE ps.user_id = er.user_id AND ps.skill_id = s.id)
 GROUP BY s.id, s.canonical_slug, s.display_name, s.ontology_version
 ORDER BY required_by DESC, preferred_by DESC, s.display_name
 LIMIT sqlc.arg(max_rows)::int;

-- name: SkillGapCoverage :one
-- The denominator, without which every count above is unreadable.
--
-- A posting whose skills were never extracted contributes nothing to the gap
-- analysis, so reporting "23 roles want Kubernetes" out of an unstated total
-- silently understates by however much of the corpus is unenriched. This is the
-- honest denominator: eligible roles, and how many of them we could actually
-- read.
SELECT count(DISTINCT o.id)::bigint AS eligible,
       count(DISTINCT o.id) FILTER (
           WHERE EXISTS (SELECT 1 FROM opportunity_skill os
                          WHERE os.opportunity_id = o.id))::bigint AS with_skills
  FROM eligibility_result er
  JOIN profile p     ON p.user_id = er.user_id
                    AND p.profile_version = er.profile_version
  JOIN opportunity o ON o.id = er.opportunity_id
 WHERE er.user_id = sqlc.arg(user_id)
   AND er.eligible
   AND o.pipeline_state = 'ready'
   AND o.closed_at IS NULL
   AND o.merged_into IS NULL;

-- name: SkillsUserHasInDemand :many
-- The mirror: skills they DO have, and how many eligible roles ask for them.
--
-- Included because a gap list on its own reads as a deficit report. The same
-- arithmetic run the other way is what makes it a position rather than a verdict,
-- and it costs one query.
SELECT s.display_name,
       count(DISTINCT o.id) FILTER (WHERE os.requirement_level = 'required')::bigint
           AS required_by
  FROM profile_skill ps
  JOIN skill s              ON s.id = ps.skill_id
  JOIN opportunity_skill os ON os.skill_id = s.id
  JOIN eligibility_result er ON er.opportunity_id = os.opportunity_id
  JOIN profile p            ON p.user_id = er.user_id
                           AND p.profile_version = er.profile_version
  JOIN opportunity o        ON o.id = er.opportunity_id
 WHERE ps.user_id = sqlc.arg(user_id)
   AND er.user_id = sqlc.arg(user_id)
   AND er.eligible
   AND o.pipeline_state = 'ready'
   AND o.closed_at IS NULL
   AND o.merged_into IS NULL
   AND os.requirement_level = 'required'
 GROUP BY s.id, s.display_name
 ORDER BY required_by DESC, s.display_name
 LIMIT sqlc.arg(max_rows)::int;
