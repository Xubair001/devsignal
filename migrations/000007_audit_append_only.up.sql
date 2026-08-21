-- Make audit_log append-only.
--
-- REVOKE alone is not enough: the application connects as the table OWNER in
-- development, and an owner retains its privileges implicitly — the REVOKE
-- silently does nothing. A trigger is enforced regardless of role, so it is the
-- mechanism that actually holds.
--
-- In production the app should ALSO run as a non-owner role with UPDATE/DELETE
-- revoked, as defence in depth. The trigger is the floor, not the ceiling.
CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (attempted %)', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_no_update
    BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();

CREATE TRIGGER audit_log_no_delete
    BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();

-- TRUNCATE bypasses row-level triggers entirely, so it needs its own statement
-- trigger. Without this the whole protection is one TRUNCATE away.
CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();
