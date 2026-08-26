-- Step 19: admin and operations tooling.
--
-- The blueprint's reason for building this before the product grows: the first
-- week of real data produces a wrongly merged pair, a source emitting garbage,
-- and a scam listing. Without tooling each becomes hand-written SQL against
-- production, which is how the second incident is caused by the fix for the first.

-- Admin is a role on the user, not a separate account type.
--
-- A separate admin table would mean a second authentication path, a second
-- session mechanism and a second place to get authorization wrong. One user
-- table with one session mechanism and one authorization check is fewer things to
-- get wrong, and the audit log already records who did what.
--
-- Nullable-free with an explicit default: a user whose role we cannot determine
-- must not be an admin, so the safe value is the default rather than something a
-- backfill has to supply.
ALTER TABLE app_user
    ADD COLUMN role text NOT NULL DEFAULT 'user'
        CHECK (role IN ('user', 'admin'));

-- Partial index: admins are a handful of rows in a table of users.
CREATE INDEX idx_app_user_admin ON app_user (id) WHERE role = 'admin';

-- User-reported listing flags, with a review queue.
--
-- This is the scam and fraud path. It is deliberately a first-class table rather
-- than a support inbox: a scam listing needs to be actionable by whoever is on
-- call, and its resolution needs to be recorded next to the posting it concerns.
CREATE TABLE opportunity_flag (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    opportunity_id uuid        NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,

    -- Nullable so a flag survives the erasure of the user who raised it. The
    -- report is about the POSTING, not about the reporter, and losing a scam
    -- report because someone deleted their account would be the wrong trade.
    reported_by    uuid        REFERENCES app_user(id) ON DELETE SET NULL,

    reason         text        NOT NULL CHECK (reason IN (
                       'scam_or_fraud', 'not_a_real_job', 'duplicate',
                       'expired', 'misleading_pay', 'discriminatory', 'other')),
    -- Free text IS allowed here, unlike dismiss reasons. A dismissal is a label
    -- fed back into scoring, where unusable text is worse than none; a flag is
    -- read by a person, and the detail is what makes it actionable.
    detail         text        CHECK (detail IS NULL OR length(detail) <= 2000),

    status         text        NOT NULL DEFAULT 'open'
                       CHECK (status IN ('open', 'upheld', 'rejected', 'duplicate')),
    resolution_note text       CHECK (resolution_note IS NULL OR length(resolution_note) <= 2000),
    resolved_by    uuid        REFERENCES app_user(id) ON DELETE SET NULL,
    resolved_at    timestamptz,

    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT flag_resolution_needs_status CHECK (
        (status = 'open') = (resolved_at IS NULL))
);

-- The review queue reads open flags oldest first: a scam report that has sat for
-- a day is more urgent than one raised a minute ago.
CREATE INDEX idx_flag_queue ON opportunity_flag (created_at) WHERE status = 'open';
CREATE INDEX idx_flag_opportunity ON opportunity_flag (opportunity_id);

-- One open flag per user per posting. Re-reporting the same listing is not more
-- signal, and it would let one person flood the queue.
CREATE UNIQUE INDEX idx_flag_one_open_per_user ON opportunity_flag (opportunity_id, reported_by)
    WHERE status = 'open' AND reported_by IS NOT NULL;

COMMENT ON TABLE opportunity_flag IS
    'User-reported listing problems with a review queue. reported_by is nullable so a flag survives the reporter''s erasure.';

-- Make un-merge actually possible.
--
-- Hard rule 11 says merges are reversible, and until now the schema could not
-- deliver it. A merge does three things: it moves the losing posting's
-- opportunity_source rows onto the canonical, marks the loser merged, and records
-- an opportunity_merge row. That record stored only the COUNT of rows moved, so
-- there was no way to know which rows to move back — and with two merges into the
-- same canonical, no way to infer it either. UndoMerge stamped undone_at and
-- changed nothing about the data.
--
-- Recording the ids makes the reversal exact rather than approximate. Nullable
-- because merges recorded before this migration genuinely do not have it, and a
-- reversal of one of those has to fail loudly rather than guess.
ALTER TABLE opportunity_merge
    ADD COLUMN moved_source_ids uuid[];

COMMENT ON COLUMN opportunity_merge.moved_source_ids IS
    'The opportunity_source rows this merge moved. Null for merges recorded before the column existed; those cannot be reversed automatically.';

-- A human decision must outrank a heuristic.
--
-- Clearing merged_into is not sufficient on its own: the posting becomes
-- claimable again, the dedupe stage re-examines the same block, finds the same
-- near-identical pair, and merges it straight back. The operator would watch their
-- un-merge quietly undo itself.
--
-- Dedup skips postings carrying this marker. It is deliberately one-way: someone
-- looked at two postings and said they are different roles, and a simhash is not
-- entitled to overrule that.
ALTER TABLE opportunity
    ADD COLUMN unmerged_at timestamptz;

COMMENT ON COLUMN opportunity.unmerged_at IS
    'Set when an administrator reversed a merge. Dedup never re-merges these: a human decision outranks a similarity score.';

CREATE INDEX idx_opp_unmerged ON opportunity (unmerged_at) WHERE unmerged_at IS NOT NULL;
