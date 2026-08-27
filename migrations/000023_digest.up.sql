-- Step 18: the daily digest.
--
-- Blueprint §4.3 calls email "the core retention loop and therefore
-- infrastructure, not a feature", and names four hard requirements: a per-user
-- daily and weekly cap, quiet hours in the user's timezone, a minimum fit bar
-- for interrupting anyone at all, and an explicit "nothing met your bar today"
-- state. All four need storage that does not exist yet.
--
-- The transport does not live here. Which provider sends the mail is an open
-- decision (docs/OPEN-DECISIONS.md §3) and it is deliberately not encoded in the
-- schema: `sender` records which one ran, so switching providers is a config
-- change and the history stays readable.

-- Notification settings, one row per user.
--
-- A separate table from `profile` rather than more columns on it, because the
-- two have different lifecycles and different meanings. `profile` is matching
-- input — changing it changes fit scores and bumps profile_version, which
-- invalidates a cache. Changing a quiet-hour window must not do that.
CREATE TABLE notification_setting (
    user_id     uuid PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    tenant_id   uuid NOT NULL REFERENCES tenant(id),

    -- IANA name, not an offset. An offset is wrong twice a year, and quiet hours
    -- that shift by an hour every spring are quiet hours nobody trusts.
    -- Validated in Go against the tzdata database; Postgres cannot check it.
    timezone    text NOT NULL DEFAULT 'UTC',

    -- Local hours, [0,24). A window may wrap midnight (21 -> 8), which is the
    -- normal case, so the comparison is deliberately not a BETWEEN.
    quiet_start smallint NOT NULL DEFAULT 21 CHECK (quiet_start BETWEEN 0 AND 23),
    quiet_end   smallint NOT NULL DEFAULT 8  CHECK (quiet_end   BETWEEN 0 AND 23),

    -- Opt-in, defaulting to OFF. A digest that arrives because we created the
    -- account is not a digest anyone consented to.
    digest_enabled boolean NOT NULL DEFAULT false,

    -- The weekly cap. The daily cap is structural instead of configurable — see
    -- the unique constraint on digest_send below.
    max_per_week smallint NOT NULL DEFAULT 5 CHECK (max_per_week BETWEEN 0 AND 7),

    -- The minimum bar for interrupting anyone at all.
    --
    -- A BAND, never a number. Hard rule 3: a numeric threshold on a score we
    -- have not calibrated would be a probability claim, and the bands are what
    -- the product actually asserts. 'strong' is the conservative default:
    -- interrupting someone is a cost, and the bar exists to make it worth it.
    min_band text NOT NULL DEFAULT 'strong'
        CHECK (min_band IN ('strong', 'worth_a_look')),

    -- Whether an empty digest is still delivered.
    --
    -- Defaults to false, and the reasoning is worth recording: blueprint §4.3
    -- requires the empty state to be EXPLICIT — never padded to a count — but
    -- that is a rule about honesty, not a requirement to mail someone every day
    -- to say nothing happened. A daily "nothing today" trains people to filter
    -- the sender, which loses the channel for the days it matters. The empty
    -- outcome is always RECORDED either way, so the day is accounted for.
    send_when_empty boolean NOT NULL DEFAULT false,

    -- Consent, per purpose, and evidenced.
    --
    -- Digest consent only. Transactional mail — password reset, erasure
    -- confirmation — needs no consent and must never be gated on this column: a
    -- user who withdraws digest consent still needs to be able to reset their
    -- password.
    --
    -- Wording version, not just a timestamp: "consent you cannot evidence is
    -- consent you do not have", and proving it means knowing what they agreed to.
    digest_consent_at              timestamptz,
    digest_consent_wording_version text,
    digest_consent_ip              inet,
    digest_consent_withdrawn_at    timestamptz,

    version    int         NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The send log: idempotency, the cap arithmetic, the SLO measurement, and the
-- ranking decision record for a surface the user did not visit.
CREATE TABLE digest_send (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id   uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES tenant(id),

    -- The user's LOCAL date, not ours. A UTC date sends twice in one local day
    -- to anyone far enough east, and skips a day for anyone far enough west.
    local_date date NOT NULL,

    -- Generation timing, for blueprint §28's "full user base inside a 30-minute
    -- window". Both ends, because the objective is about the SPREAD across users
    -- and a single timestamp cannot express it.
    generation_started_at timestamptz NOT NULL,
    generated_at          timestamptz NOT NULL,
    sent_at               timestamptz,

    outcome text NOT NULL CHECK (outcome IN (
        -- Delivered, with items.
        'sent',
        -- Nothing cleared the bar. A real outcome, not a failure.
        'empty',
        -- The weekly cap was already spent.
        'suppressed_cap',
        -- Consent absent or withdrawn. Recorded rather than skipped silently, so
        -- "why did this user get nothing" has an answer.
        'suppressed_no_consent',
        'suppressed_disabled',
        -- The sender failed. Distinct from 'empty' — one is the market being
        -- quiet, the other is us being broken, and a dashboard must not blend them.
        'failed'
    )),
    -- Why, in words, for whoever asks. Never a bare code.
    reason text,

    item_count int NOT NULL DEFAULT 0,
    -- What went in it. Two jobs: the next digest does not repeat these, and the
    -- decision is auditable after the fact.
    opportunity_ids uuid[] NOT NULL DEFAULT '{}',

    -- Everything a ranking decision depends on carries its version (hard rule
    -- 10). A digest is a ranking shown to a user without them asking, which
    -- makes the record more important here, not less.
    weights_version text,
    profile_version int,
    min_band        text,

    -- Which transport ran. 'log' is the development sender that writes the
    -- rendered digest to disk and delivers nothing.
    sender text NOT NULL,

    -- How many times this day was composed.
    --
    -- More than one is normal and not a problem: an 'empty' outcome is
    -- PROVISIONAL. Nothing cleared the bar at 08:00, but ingestion runs all day,
    -- and a Strong fit that appears at 10:00 should reach the user today rather
    -- than being lost because an earlier run had already written the day off. A
    -- later run upgrades the row in place; 'sent' is the only terminal outcome.
    attempts int NOT NULL DEFAULT 1,

    -- One digest per user per LOCAL day, enforced by the database rather than by
    -- a query the caller has to remember. This is the daily cap from blueprint
    -- §4.3, made structural: a cap implemented as an application check fails
    -- open the first time two workers run at once, and a duplicate digest is the
    -- most visible possible bug in a retention channel.
    --
    -- Quiet hours deliberately write NO row: they defer, they do not cancel, so
    -- a run inside the window must leave the day claimable when it reopens.
    UNIQUE (user_id, local_date)
);

-- The weekly cap counts sends in a trailing window per user.
CREATE INDEX idx_digest_send_user_date ON digest_send (user_id, local_date DESC);

-- The SLO reads the most recent run. Partial: only a delivered digest has a
-- generation window worth measuring.
CREATE INDEX idx_digest_send_generated ON digest_send (generated_at DESC)
    WHERE outcome IN ('sent', 'empty');
