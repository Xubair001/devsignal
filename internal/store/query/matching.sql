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
