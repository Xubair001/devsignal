-- name: CreateTenant :one
INSERT INTO tenant (kind, display_name) VALUES ($1, $2) RETURNING *;

-- name: CreateUser :one
INSERT INTO app_user (tenant_id, email, password_hash)
VALUES ($1, $2, $3) RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM app_user WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM app_user WHERE id = $1;

-- name: UpdateUserPasswordHash :exec
UPDATE app_user SET password_hash = $2 WHERE id = $1;

-- name: RecordLoginSuccess :exec
UPDATE app_user
   SET failed_logins = 0, locked_until = NULL, last_login_at = now()
 WHERE id = $1;

-- name: RecordLoginFailure :one
-- Lockout is computed in SQL so concurrent attempts cannot race past the
-- threshold by each reading a stale counter.
UPDATE app_user
   SET failed_logins = failed_logins + 1,
       locked_until = CASE WHEN failed_logins + 1 >= sqlc.arg(threshold)::int
                           THEN now() + sqlc.arg(lockout)::interval
                           ELSE locked_until END
 WHERE id = sqlc.arg(id)
 RETURNING failed_logins, locked_until;

-- name: MarkEmailVerified :exec
UPDATE app_user SET email_verified_at = now() WHERE id = $1;

-- name: CreateSession :one
INSERT INTO user_session (user_id, token_hash, user_agent, expires_at)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetLiveSessionByHash :one
SELECT * FROM user_session
 WHERE token_hash = $1
   AND revoked_at IS NULL
   AND expires_at > now();

-- name: TouchSession :exec
UPDATE user_session SET last_seen_at = now() WHERE id = $1;

-- name: RevokeSession :exec
UPDATE user_session SET revoked_at = now()
 WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeAllUserSessions :execrows
UPDATE user_session SET revoked_at = now()
 WHERE user_id = $1 AND revoked_at IS NULL;

-- name: CreateRefreshToken :one
INSERT INTO refresh_token (family_id, user_id, session_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetRefreshByHash :one
SELECT * FROM refresh_token WHERE token_hash = $1;

-- name: MarkRefreshUsed :exec
UPDATE refresh_token SET used_at = now(), replaced_by = $2 WHERE id = $1;

-- name: RevokeRefreshFamily :execrows
-- Reuse detection: a replayed token revokes the entire family, because we
-- cannot tell whether the attacker or the legitimate client holds the newer one.
UPDATE refresh_token SET revoked_at = now()
 WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeSessionsForFamily :execrows
UPDATE user_session SET revoked_at = now()
 WHERE id IN (SELECT session_id FROM refresh_token WHERE family_id = $1)
   AND revoked_at IS NULL;

-- name: LockAuditChain :exec
-- Serializes audit inserts for the duration of the transaction so two writers
-- cannot both chain off the same previous hash. Audit volume is low; the
-- contention is irrelevant and the chain integrity is not.
SELECT pg_advisory_xact_lock(hashtext('devsignal.audit_log'));

-- name: GetLastAuditHash :one
SELECT entry_hash FROM audit_log ORDER BY id DESC LIMIT 1;

-- name: InsertAudit :one
INSERT INTO audit_log (actor_id, tenant_id, action, subject, metadata, prev_hash, entry_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: CountAuditEntries :one
SELECT count(*) FROM audit_log;

-- name: ListAuditForChainCheck :many
SELECT id, prev_hash, entry_hash, action, occurred_at FROM audit_log ORDER BY id;

-- name: CreateUserToken :one
-- A single-use token for a transactional flow: email verification or a password
-- reset.
--
-- Only the HASH is stored, like a session token. A leaked database must not hand
-- someone else's verification link to whoever read it, and the plaintext exists
-- only long enough to put in an email.
INSERT INTO user_token (user_id, purpose, token_hash, expires_at)
VALUES (sqlc.arg(user_id), sqlc.arg(purpose), sqlc.arg(token_hash), sqlc.arg(expires_at))
RETURNING *;

-- name: ConsumeUserToken :one
-- Claims a token, atomically, exactly once.
--
-- The UPDATE is the claim: consumed_at IS NULL in the WHERE clause means two
-- concurrent requests cannot both succeed, and a replayed link finds nothing.
-- Expiry is checked here too rather than in Go, so a clock difference between
-- process and database cannot widen the window.
UPDATE user_token
   SET consumed_at = now()
 WHERE token_hash = sqlc.arg(token_hash)
   AND purpose = sqlc.arg(purpose)
   AND consumed_at IS NULL
   AND expires_at > now()
RETURNING *;

-- name: ExpireUserTokensOfPurpose :execrows
-- Invalidates a user's outstanding tokens for one purpose.
--
-- Called when a new one is issued, so "resend" cannot leave two live links: the
-- older one stops working the moment a newer is created, which is what makes the
-- most recent email the only one that matters.
UPDATE user_token SET consumed_at = now()
 WHERE user_id = sqlc.arg(user_id) AND purpose = sqlc.arg(purpose)
   AND consumed_at IS NULL;

