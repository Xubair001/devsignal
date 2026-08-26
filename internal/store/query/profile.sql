-- name: UpsertProfile :one
INSERT INTO profile (
    user_id, tenant_id, headline, years_experience, seniority_ordinal, is_management,
    target_role_families, target_countries, work_mode_preference, languages,
    min_salary_minor, salary_currency, salary_period, work_authorization,
    target_employment_types
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (user_id) DO UPDATE SET
    headline             = EXCLUDED.headline,
    years_experience     = EXCLUDED.years_experience,
    seniority_ordinal    = EXCLUDED.seniority_ordinal,
    is_management        = EXCLUDED.is_management,
    target_role_families = EXCLUDED.target_role_families,
    target_countries     = EXCLUDED.target_countries,
    work_mode_preference = EXCLUDED.work_mode_preference,
    languages            = EXCLUDED.languages,
    min_salary_minor     = EXCLUDED.min_salary_minor,
    salary_currency      = EXCLUDED.salary_currency,
    salary_period        = EXCLUDED.salary_period,
    work_authorization   = EXCLUDED.work_authorization,
    target_employment_types = EXCLUDED.target_employment_types
RETURNING *;

-- name: GetProfile :one
SELECT * FROM profile WHERE user_id = $1;

-- name: ListProfileSkills :many
SELECT ps.skill_id, ps.origin, ps.proficiency, ps.years,
       s.canonical_slug, s.display_name
  FROM profile_skill ps
  JOIN skill s ON s.id = ps.skill_id
 WHERE ps.user_id = $1
 ORDER BY s.display_name;

-- name: UpsertProfileSkill :exec
INSERT INTO profile_skill (user_id, skill_id, origin, proficiency, years)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (user_id, skill_id) DO UPDATE SET
    -- A manual claim outranks a parsed one: the user typing it is stronger
    -- evidence than a parser guessing it from a document.
    origin      = CASE WHEN EXCLUDED.origin = 'manual' THEN 'manual' ELSE profile_skill.origin END,
    proficiency = COALESCE(EXCLUDED.proficiency, profile_skill.proficiency),
    years       = COALESCE(EXCLUDED.years, profile_skill.years);

-- name: DeleteProfileSkill :exec
DELETE FROM profile_skill WHERE user_id = $1 AND skill_id = $2;

-- name: TouchProfileVersion :exec
-- Skills live in their own table, so editing them does not fire the profile
-- trigger. Called explicitly, because a skill change absolutely can move a fit
-- score and a stale cached score is worse than a slow one.
UPDATE profile SET updated_at = now() WHERE user_id = $1;

-- name: CreateResume :one
INSERT INTO resume (user_id, object_key, filename, content_type, size_bytes, sha256)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING *;

-- name: SetResumeText :exec
UPDATE resume
   SET text_object_key = $2, text_chars = $3, parse_state = 'text_extracted', parse_error = NULL
 WHERE id = $1;

-- name: FailResume :exec
UPDATE resume SET parse_state = 'failed', parse_error = $2 WHERE id = $1;

-- name: GetResume :one
SELECT * FROM resume WHERE id = $1 AND deleted_at IS NULL;

-- name: ListUserResumes :many
SELECT * FROM resume WHERE user_id = $1 AND deleted_at IS NULL ORDER BY uploaded_at DESC;

-- name: SoftDeleteResume :exec
UPDATE resume SET deleted_at = now() WHERE id = $1 AND user_id = $2;

-- name: ListAllUserResumeKeys :many
-- Includes soft-deleted rows: erasure must remove objects for resumes the user
-- already asked to delete, not just the live ones.
SELECT object_key, text_object_key FROM resume WHERE user_id = $1;

-- ---------------------------------------------------------------- erasure

-- name: CreateErasureRequest :one
INSERT INTO erasure_request (user_id) VALUES ($1) RETURNING *;

-- name: RecordErasureStep :exec
INSERT INTO erasure_step (request_id, location, status, items, detail, completed_at)
VALUES ($1,$2,$3,$4,$5, now())
ON CONFLICT (request_id, location) DO UPDATE SET
    status = EXCLUDED.status, items = EXCLUDED.items,
    detail = EXCLUDED.detail, completed_at = now();

-- name: CompleteErasureRequest :exec
-- Only when every step succeeded. A partial erasure must stay visibly
-- incomplete rather than being marked done.
UPDATE erasure_request SET completed_at = now()
 WHERE id = sqlc.arg(id)
   AND NOT EXISTS (
     SELECT 1 FROM erasure_step
      WHERE request_id = sqlc.arg(id)
        AND status NOT IN ('done','not_applicable'));

-- name: GetErasureRequest :one
SELECT * FROM erasure_request WHERE id = $1;

-- name: ListErasureSteps :many
SELECT location, status, items, detail FROM erasure_step
 WHERE request_id = $1 ORDER BY location;

-- name: DeleteProfileData :execrows
DELETE FROM profile WHERE user_id = $1;

-- name: DeleteProfileSkills :execrows
DELETE FROM profile_skill WHERE user_id = $1;

-- name: DeleteResumeRows :execrows
DELETE FROM resume WHERE user_id = $1;

-- name: DeleteUserSessions :execrows
DELETE FROM user_session WHERE user_id = $1;

-- name: DeleteUserRefreshTokens :execrows
DELETE FROM refresh_token WHERE user_id = $1;

-- name: DeleteUserTokens :execrows
DELETE FROM user_token WHERE user_id = $1;

-- name: DeleteUserRow :execrows
DELETE FROM app_user WHERE id = $1;

-- name: CountUserTraces :one
-- Verification: after erasure this must be zero everywhere. Counting rather than
-- trusting the deletes is what makes the guarantee checkable.
-- Every column is table-qualified: unqualified user_id across seven subqueries
-- is ambiguous to the planner.
SELECT (SELECT count(*) FROM profile p        WHERE p.user_id  = sqlc.arg(user_id))
     + (SELECT count(*) FROM profile_skill ps WHERE ps.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM profile_embedding pe WHERE pe.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM fit_score fsc WHERE fsc.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM eligibility_result er WHERE er.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM engagement_event ee WHERE ee.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM resume r         WHERE r.user_id  = sqlc.arg(user_id))
     -- Step 18. A notification setting holds a timezone and quiet hours; a
     -- digest_send row records which postings were mailed to this person and
     -- when. Both cascade from app_user, but a cascade is not a verification:
     -- this count is what makes the erasure guarantee checkable rather than
     -- assumed, and a table missing from HERE is the specific way a derived
     -- artifact gets quietly left behind.
     + (SELECT count(*) FROM notification_setting ns WHERE ns.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM digest_send ds  WHERE ds.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM user_session us  WHERE us.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM refresh_token rt WHERE rt.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM user_token ut    WHERE ut.user_id = sqlc.arg(user_id))
     + (SELECT count(*) FROM app_user au      WHERE au.id      = sqlc.arg(user_id))
       AS traces;

-- name: DeleteProfileEmbedding :execrows
-- Erasure. The app_user cascade would remove these anyway, but an enumerated
-- delete is what lets the report state a count for this store instead of
-- attributing it to the user row.
DELETE FROM profile_embedding WHERE user_id = sqlc.arg(user_id);

-- name: PutProfileEmbedding :exec
-- Upsert per (user, version) so a profile edit refreshes the vector in place and
-- a version migration can dual-write.
INSERT INTO profile_embedding (
    user_id, embedding_model, embedding_version, embedding_dim, embedding, profile_version
) VALUES (
    sqlc.arg(user_id), sqlc.arg(embedding_model), sqlc.arg(embedding_version),
    sqlc.arg(embedding_dim), sqlc.arg(embedding), sqlc.arg(profile_version)
)
ON CONFLICT (user_id, embedding_version) DO UPDATE
   SET embedding       = excluded.embedding,
       embedding_model = excluded.embedding_model,
       embedding_dim   = excluded.embedding_dim,
       profile_version = excluded.profile_version,
       updated_at      = now();

-- name: GetProfileEmbedding :one
-- Returns the vector with the profile version it was built from, so the caller
-- can tell a current vector from one that predates the latest profile edit.
SELECT pe.embedding, pe.embedding_model, pe.profile_version AS embedded_profile_version,
       p.profile_version AS current_profile_version
  FROM profile_embedding pe
  JOIN profile p ON p.user_id = pe.user_id
 WHERE pe.user_id = sqlc.arg(user_id)
   AND pe.embedding_version = sqlc.arg(embedding_version);

-- name: AnonymizeUserFlags :execrows
-- Erasure. A flag is about the POSTING, not the reporter, so the report survives
-- with its author removed rather than being deleted.
--
-- Done explicitly rather than left to the ON DELETE SET NULL cascade, so the
-- erasure report can state a count for it. A scam report vanishing because
-- someone closed their account would be the wrong trade — the listing is still
-- a problem for everyone else.
UPDATE opportunity_flag SET reported_by = NULL
 WHERE reported_by = sqlc.arg(user_id);
