-- Step 17: the engagement log.
--
-- One table with three jobs, which is deliberate rather than lazy:
--
--   1. The product's save/apply state. What the user has acted on.
--   2. The growing evaluation set. The rubric labels in internal/eval are a
--      stopgap; these are the real ones, because they record what a person
--      actually did rather than what a rule predicted they would want.
--   3. The ranking decision record. Every row carries the fit score and the full
--      factor breakdown AS SHOWN, plus every version that produced it. Blueprint
--      §32 requires being able to answer "why was this ranked here for this user
--      on this date" after the fact, and a score recomputed later under different
--      weights is not an answer to that question.
--
-- Append-only. There is no UPDATE path and no unique constraint that would force
-- one: un-saving writes an 'unsaved' row rather than deleting the 'saved' row.
-- A decision record you can edit is not a record, and the eval set needs the
-- reversal as much as the original action — a save the user took back is a
-- different signal from one they kept.
CREATE TABLE engagement_event (
    id             bigserial   PRIMARY KEY,
    user_id        uuid        NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    opportunity_id uuid        NOT NULL REFERENCES opportunity(id) ON DELETE CASCADE,

    event_type     text        NOT NULL CHECK (event_type IN (
                       'shown', 'opened', 'saved', 'unsaved', 'applied', 'dismissed')),

    -- Only meaningful for 'dismissed'. The reason is the most valuable label in
    -- the system: "wrong level" and "comp too low" are corrections to specific
    -- factors, not a generic negative.
    dismiss_reason text        CHECK (dismiss_reason IS NULL OR dismiss_reason IN (
                       'wrong_stack', 'wrong_level', 'wrong_location',
                       'comp_too_low', 'not_interested', 'already_applied')),

    -- The score AS SHOWN, not as recomputed. Null for events that did not come
    -- from a ranked surface.
    fit_score_at_event smallint CHECK (fit_score_at_event IS NULL
                                       OR fit_score_at_event BETWEEN 0 AND 100),
    max_possible_at_event smallint CHECK (max_possible_at_event IS NULL
                                          OR max_possible_at_event BETWEEN 0 AND 100),
    factor_breakdown   jsonb,

    -- Every version behind the decision (hard rule 10). Without these the row
    -- records that something was ranked, not why.
    weights_version    text,
    embedding_version  text,
    profile_version    integer,
    opportunity_version integer,

    occurred_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT engagement_dismiss_reason_only_on_dismiss CHECK (
        dismiss_reason IS NULL OR event_type = 'dismissed')
);

-- The feed reads current state per user: latest event per opportunity.
CREATE INDEX idx_engagement_user_opp ON engagement_event (user_id, opportunity_id, occurred_at DESC);

-- Saturation counts distinct DAYS a posting was shown and ignored, so refreshing
-- the feed twenty times does not bury a role.
CREATE INDEX idx_engagement_shown ON engagement_event (user_id, opportunity_id, occurred_at)
    WHERE event_type = 'shown';

-- The eval set is read by event type across users.
CREATE INDEX idx_engagement_labels ON engagement_event (event_type, occurred_at DESC)
    WHERE event_type IN ('saved', 'applied', 'dismissed');

COMMENT ON TABLE engagement_event IS
    'Append-only engagement log: product state, evaluation labels, and the ranking decision record. User-derived: erasure deletes these.';
