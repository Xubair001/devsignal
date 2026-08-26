-- name: GetNotificationSetting :one
SELECT * FROM notification_setting WHERE user_id = $1;

-- name: UpsertNotificationSetting :one
-- version bumps on every write so a concurrent editor is detectable. Consent
-- columns are deliberately NOT touched here: consent is recorded by its own
-- statement with its own evidence, and folding it into a settings save is how
-- an unrelated preference change ends up looking like a consent event.
INSERT INTO notification_setting (
    user_id, tenant_id, timezone, quiet_start, quiet_end,
    digest_enabled, max_per_week, min_band, send_when_empty
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (user_id) DO UPDATE SET
    timezone        = excluded.timezone,
    quiet_start     = excluded.quiet_start,
    quiet_end       = excluded.quiet_end,
    digest_enabled  = excluded.digest_enabled,
    max_per_week    = excluded.max_per_week,
    min_band        = excluded.min_band,
    send_when_empty = excluded.send_when_empty,
    version         = notification_setting.version + 1,
    updated_at      = now()
RETURNING *;

-- name: RecordDigestConsent :execrows
-- Evidenced consent: when, what wording, from where. Clearing withdrawn_at is
-- how a re-subscribe works, and it is explicit rather than implied.
UPDATE notification_setting
   SET digest_consent_at              = sqlc.arg(consented_at),
       digest_consent_wording_version = sqlc.arg(wording_version),
       digest_consent_ip              = sqlc.narg(ip),
       digest_consent_withdrawn_at    = NULL,
       digest_enabled                 = true,
       version                        = version + 1,
       updated_at                     = now()
 WHERE user_id = sqlc.arg(user_id);

-- name: WithdrawDigestConsent :execrows
-- The consent record is kept, not erased: proving we HAD consent and that it was
-- withdrawn on a date is the point of recording it. digest_enabled goes false in
-- the same statement so a withdrawal cannot half-apply.
UPDATE notification_setting
   SET digest_consent_withdrawn_at = sqlc.arg(withdrawn_at),
       digest_enabled              = false,
       version                     = version + 1,
       updated_at                  = now()
 WHERE user_id = sqlc.arg(user_id) AND digest_consent_withdrawn_at IS NULL;

-- name: DigestCandidateUsers :many
-- Everyone a digest run should consider.
--
-- The consent and enabled predicates are HERE rather than in the loop, so a
-- caller cannot forget them. A profile is required by join: the digest is a
-- ranking, and there is nothing to rank for a user who has not told us anything.
SELECT n.user_id, n.tenant_id, n.timezone, n.quiet_start, n.quiet_end,
       n.max_per_week, n.min_band, n.send_when_empty,
       p.profile_version
  FROM notification_setting n
  JOIN profile p   ON p.user_id = n.user_id
  JOIN app_user u  ON u.id      = n.user_id
 WHERE n.digest_enabled
   AND n.digest_consent_at IS NOT NULL
   AND n.digest_consent_withdrawn_at IS NULL
   -- A suspended or deleted account is not a recipient.
   AND u.status = 'active'
   -- Unverified addresses are never mailed: sending to one is how a domain's
   -- reputation is lost, and it may not even be the user's address.
   AND u.email_verified_at IS NOT NULL
 ORDER BY n.user_id;

-- name: CountDigestSendsInWindow :one
-- The weekly cap. Counts DELIVERED digests only: a day we correctly stayed
-- quiet did not spend the user's attention, so it must not spend their budget.
SELECT count(*) AS sends
  FROM digest_send
 WHERE user_id = sqlc.arg(user_id)
   AND outcome = 'sent'
   AND local_date > sqlc.arg(since_date)::date;

-- name: RecentDigestOpportunityIDs :many
-- What we have already shown this user by email, so today's digest does not
-- repeat it. A retention channel that resends yesterday's list is worse than
-- silence.
SELECT DISTINCT unnest(opportunity_ids)::uuid AS opportunity_id
  FROM digest_send
 WHERE user_id = sqlc.arg(user_id)
   AND local_date > sqlc.arg(since_date)::date;

-- name: ClaimDigestDay :one
-- Claims the user's local day, or returns nothing if it is already claimed.
--
-- The insert IS the lock. Two workers running the same day race here and
-- exactly one wins, because the unique constraint decides rather than a
-- read-then-write in application code.
INSERT INTO digest_send (
    user_id, tenant_id, local_date,
    generation_started_at, generated_at,
    outcome, reason, item_count, opportunity_ids,
    weights_version, profile_version, min_band, sender
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (user_id, local_date) DO NOTHING
RETURNING *;

-- name: MarkDigestSent :execrows
UPDATE digest_send
   SET sent_at = sqlc.arg(sent_at), outcome = 'sent'
 WHERE id = sqlc.arg(id);

-- name: MarkDigestFailed :execrows
UPDATE digest_send
   SET outcome = 'failed', reason = sqlc.arg(reason)
 WHERE id = sqlc.arg(id);

-- name: DeleteNotificationSetting :execrows
DELETE FROM notification_setting WHERE user_id = $1;

-- name: DeleteDigestSends :execrows
DELETE FROM digest_send WHERE user_id = $1;

-- name: SLIDigestGeneration :one
-- Blueprint §28: the full user base inside a 30-minute window.
--
-- Measures the SPREAD of the most recent run, not any single user's duration:
-- the objective is that everybody's digest is ready in time, so the number that
-- matters is first-start to last-finish. Scoped to the latest local_date that
-- produced anything, because averaging across days hides a bad one.
WITH latest AS (
    SELECT max(local_date) AS d FROM digest_send
     WHERE outcome IN ('sent', 'empty')
)
SELECT
    count(*)::bigint AS users,
    coalesce(max(generated_at) - min(generation_started_at),
             interval '0')::interval AS spread
  FROM digest_send, latest
 WHERE local_date = latest.d
   AND outcome IN ('sent', 'empty');

-- name: GetDigestDay :one
-- Today's row for this user, if a run already reached a decision.
--
-- Read before composing, so a day that is already SENT costs one indexed lookup
-- instead of a full retrieval and scoring pass. The unique constraint is still
-- what guarantees correctness under a race; this is the cheap path, not the
-- guarantee.
SELECT * FROM digest_send WHERE user_id = $1 AND local_date = $2;

-- name: UpgradeDigestDay :one
-- Turns a provisional day into a real one.
--
-- An 'empty' or 'suppressed_cap' row records what was true when it was written,
-- not a promise about the rest of the day. Guarded on outcome <> 'sent' so a
-- delivered digest can never be overwritten or double-counted — that guard is
-- the daily cap, and it lives in the WHERE clause rather than in a caller's
-- memory.
UPDATE digest_send
   SET outcome         = sqlc.arg(outcome),
       reason          = sqlc.narg(reason),
       item_count      = sqlc.arg(item_count),
       opportunity_ids = sqlc.arg(opportunity_ids),
       weights_version = sqlc.narg(weights_version),
       generated_at    = sqlc.arg(generated_at),
       attempts        = attempts + 1
 WHERE user_id = sqlc.arg(user_id)
   AND local_date = sqlc.arg(local_date)
   AND outcome <> 'sent'
RETURNING *;
