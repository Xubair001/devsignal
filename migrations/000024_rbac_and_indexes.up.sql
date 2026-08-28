-- Indexes for the surfaces added in steps 18-19 and the skill ontology.
--
-- Not CONCURRENTLY, for the reason recorded in 000018: golang-migrate runs each
-- migration in a transaction and CREATE INDEX CONCURRENTLY cannot run inside
-- one. At the volumes here the build is instant. On a large table, build the
-- index out-of-band with CONCURRENTLY and then record it here.
--
-- Each index below is the SHAPE of a query that already exists, and the comment
-- says which. Two of them will be ignored by the planner today — a partial index
-- over one row is never worth a lookup — and that is stated rather than hidden,
-- because an index nobody can justify is an index nobody dares remove.

-- 1. The digest recipient scan.
--
-- DigestCandidateUsers runs on a cron and filters exactly these three
-- predicates. Today notification_setting holds ONE row, so the planner will
-- sequentially scan it and this index earns nothing. It is here because the
-- query is per-run over the whole user base: the scan is O(users) every time the
-- digest runs, and the eligible set is a small fraction of it once opt-in is
-- normal rather than universal. A partial index over the eligible rows is
-- proportional to the answer instead of to the table.
CREATE INDEX idx_notification_eligible
    ON notification_setting (user_id)
 WHERE digest_enabled
   AND digest_consent_at IS NOT NULL
   AND digest_consent_withdrawn_at IS NULL;

-- 2. NOT an index on the unresolved-skill queue, and the measurement is why.
--
-- The obvious next index was a partial one over `ontology_version NOT LIKE
-- 'seed-%'`, on the reasoning that the seeded vocabulary is bounded at a few
-- hundred while the unresolved set grows with every posting, so unresolved is
-- the minority. That reasoning is backwards.
--
-- Measured: 1,333 unresolved of 1,597 total skills — 83% of the table. A partial
-- index covering 83% of rows is more expensive to walk than the table is to
-- scan, and the planner correctly ignored it (Seq Scan, with the index present).
-- It grows the WRONG way too: every new posting adds unresolved phrases faster
-- than the seed grows, so the share only rises.
--
-- The fix for that query is not an index, it is a smaller unresolved set — which
-- is what `--role=skills --unresolved` exists to drive. Recorded here rather
-- than left out so nobody re-derives the same wrong index.

-- 3. The digest-generation SLO.
--
-- SLIDigestGeneration takes max(local_date) over the delivered-or-empty rows,
-- then reads that day's spread. idx_digest_send_generated orders by generated_at,
-- which answers a different question. One row per user per day means this grows
-- forever while the query only ever wants the newest day.
CREATE INDEX idx_digest_send_local_date
    ON digest_send (local_date DESC)
 WHERE outcome IN ('sent', 'empty');

-- 4. Role lookups beyond the admin check.
--
-- idx_app_user_admin already covers "is this user an admin". This covers
-- "list the admins", which --role=list-admins does and which currently scans
-- every user. Included rather than left out because granting and auditing admin
-- is exactly the operation that must stay cheap enough to do often.
CREATE INDEX idx_app_user_role ON app_user (role, email);
