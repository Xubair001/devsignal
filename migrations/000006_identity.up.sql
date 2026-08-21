-- Identity. Every rule here is in CLAUDE.md's hard rules or blueprint §31.1;
-- the parts that are usually got wrong are called out inline.

CREATE TABLE tenant (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 'individual' now; 'organization' is what the column exists for, so that
    -- org scoping later is a policy addition rather than a migration across
    -- every table.
    kind         text NOT NULL DEFAULT 'individual'
                   CHECK (kind IN ('individual','organization')),
    display_name text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app_user (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenant(id),
    email             citext NOT NULL UNIQUE,       -- citext: nobody expects case-sensitive login
    -- argon2id encoded string, parameters embedded. Never a bare hash: the
    -- parameters must travel with it so they can be raised later per-user.
    password_hash     text NOT NULL,
    email_verified_at timestamptz,
    status            text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','suspended','deleted')),
    -- Lockout state is persisted, not held in Redis: a restart must not clear
    -- an active lockout, or it becomes a trivial bypass.
    failed_logins     integer NOT NULL DEFAULT 0,
    locked_until      timestamptz,
    last_login_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER app_user_updated_at BEFORE UPDATE ON app_user
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX idx_app_user_tenant ON app_user (tenant_id);

-- Opaque server-side sessions, not JWTs: revocation is then real and instant.
-- We store only a hash — a leaked database must not yield usable tokens.
CREATE TABLE user_session (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash    bytea NOT NULL UNIQUE,
    user_agent    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz
);

CREATE INDEX idx_user_session_user ON user_session (user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_user_session_expiry ON user_session (expires_at) WHERE revoked_at IS NULL;

-- Rotating refresh tokens with reuse detection.
--
-- family_id ties every rotation of one login together. On rotation the old row
-- gets used_at + replaced_by. If a token that already has used_at is presented
-- again, that is a replay: revoke the WHOLE family, because we cannot tell
-- whether the attacker or the legitimate client is holding the newer token.
CREATE TABLE refresh_token (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id   uuid NOT NULL,
    user_id     uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    session_id  uuid NOT NULL REFERENCES user_session(id) ON DELETE CASCADE,
    token_hash  bytea NOT NULL UNIQUE,
    used_at     timestamptz,
    replaced_by uuid REFERENCES refresh_token(id),
    revoked_at  timestamptz,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_refresh_family ON refresh_token (family_id);
CREATE INDEX idx_refresh_user ON refresh_token (user_id);

-- Single-use tokens for verification and reset. Hashed, expiring, consumed.
CREATE TABLE user_token (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    purpose     text NOT NULL CHECK (purpose IN ('email_verify','password_reset')),
    token_hash  bytea NOT NULL UNIQUE,
    consumed_at timestamptz,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_token_user ON user_token (user_id, purpose) WHERE consumed_at IS NULL;

-- Append-only audit log. The application role loses UPDATE and DELETE in
-- migration 000007; a log the app can rewrite is useless in the one
-- investigation it exists for.
CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    actor_id    uuid,                 -- NULL for system actions
    tenant_id   uuid,
    action      text NOT NULL,        -- 'profile.updated', 'resume.deleted', ...
    subject     text,                 -- what it happened to
    -- Metadata only. NEVER PII: this table outlives an erasure request by design.
    metadata    jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Hash chain: prev_hash = digest of the previous row. Detects deletion or
    -- alteration even by someone with direct database access.
    prev_hash   bytea,
    entry_hash  bytea NOT NULL
);

CREATE INDEX idx_audit_actor ON audit_log (actor_id, occurred_at DESC);
CREATE INDEX idx_audit_action ON audit_log (action, occurred_at DESC);
