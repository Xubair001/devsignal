// Package digest builds and sends the daily email digest.
//
// Blueprint §4.3 calls email "the core retention loop and therefore
// infrastructure, not a feature", and names four hard requirements. All four are
// here, and each one is a place this could go wrong quietly:
//
//   - a per-user daily and weekly cap. The daily cap is structural — a unique
//     constraint on (user_id, local_date) — because a cap enforced by an
//     application check fails open the first time two workers overlap, and a
//     duplicate digest is the most visible possible bug in a retention channel.
//   - quiet hours in the USER'S timezone. Not ours. An IANA zone, not an offset.
//   - a minimum fit BAND for interrupting anyone at all. A band, never a numeric
//     threshold: hard rule 3 forbids treating an uncalibrated score as a
//     probability, and that applies to our own send decision as much as to
//     anything rendered.
//   - an explicit "nothing met your bar today" state, never padded to a count.
//
// The transport is deliberately not decided here. Which provider sends the mail
// is an open item (docs/OPEN-DECISIONS.md §3); Sender is the seam, and the
// development sender writes the rendered digest to disk and delivers nothing.
// This mirrors how extraction handles an absent model: a real interface with a
// real fake behind it, so every path except the last hop is exercised.
package digest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/opportunity"
	"github.com/Xubair001/devsignal/internal/store"
)

// MaxItems bounds one digest.
//
// Seven, matching the feed and the Precision@7 the eval harness measures. A
// digest longer than the product's daily promise is a different surface wearing
// the same name.
const MaxItems = 7

// RepeatWindow is how far back a digest looks to avoid resending a posting.
const RepeatWindow = 14 * 24 * time.Hour

// WeeklyWindow is the trailing period the weekly cap counts over.
const WeeklyWindow = 7

// Minimum bands a user can set. Stored as slugs; compared against the display
// bands the scorer produces.
const (
	BarStrong     = "strong"
	BarWorthALook = "worth_a_look"
)

// Outcome is what a run decided for one user. Mirrors the check constraint on
// digest_send.outcome.
type Outcome string

const (
	OutcomeSent          Outcome = "sent"
	OutcomeEmpty         Outcome = "empty"
	OutcomeSuppressedCap Outcome = "suppressed_cap"
	OutcomeFailed        Outcome = "failed"
	// OutcomeDeferred is NOT stored. Quiet hours defer, they do not cancel, so a
	// run inside the window must leave the day claimable when it reopens.
	OutcomeDeferred Outcome = "deferred_quiet_hours"
	// OutcomeAlreadySent is not stored either: the day was claimed by an earlier
	// run and the unique constraint said so.
	OutcomeAlreadySent Outcome = "already_sent"
)

// Clock is injected. Hard rule 14: quiet hours, local dates and caps are all
// time-dependent, and time.Now() inside them is untestable.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// Item is one posting in a digest.
type Item struct {
	Match   matching.Match
	Posting opportunity.Summary
}

// Result is what happened for one user, whether or not anything was sent.
type Result struct {
	UserID    string
	LocalDate time.Time
	Outcome   Outcome
	// Reason is written for a person, not a dashboard. Every non-sent outcome has
	// one: "why did this user get nothing today" must always have an answer.
	Reason string
	Items  []Item
	// Considered is how many matches the bar was applied to, so an empty digest
	// can distinguish "nothing was eligible" from "nothing was good enough".
	Considered int
	// BelowBar counts matches that were eligible but did not clear the band. This
	// is the honest content of an empty digest.
	BelowBar    int
	AlreadySent int
	GeneratedIn time.Duration
}

// Sent reports whether mail actually went out.
func (r Result) Sent() bool { return r.Outcome == OutcomeSent }

// Logger is the subset of slog this package needs.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Service builds digests.
type Service struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	matcher *matching.Service
	opps    *opportunity.Service
	sender  Sender
	clock   Clock
	log     Logger
}

// NewService builds one.
func NewService(
	pool *pgxpool.Pool, matcher *matching.Service, opps *opportunity.Service,
	sender Sender, clock Clock, log Logger,
) *Service {
	if clock == nil {
		clock = realClock{}
	}
	return &Service{
		pool: pool, q: store.New(pool), matcher: matcher, opps: opps,
		sender: sender, clock: clock, log: log,
	}
}

// RunReport summarizes a whole run.
type RunReport struct {
	StartedAt time.Time
	Results   []Result
}

// Counts tallies outcomes.
func (r RunReport) Counts() map[Outcome]int {
	out := map[Outcome]int{}
	for _, res := range r.Results {
		out[res.Outcome]++
	}
	return out
}

// Run builds and sends a digest for every consenting user.
//
// Sequential on purpose at this size. The SLO is "the full user base inside a
// 30-minute window", and the honest way to meet that at scale is to shard by
// user, not to fan out unbounded goroutines against one connection pool — the
// load test already showed feed cost is per-request CPU proportional to the
// candidate set, so concurrency here would contend on exactly that.
func (s *Service) Run(ctx context.Context) (*RunReport, error) {
	started := s.clock.Now()
	rep := &RunReport{StartedAt: started}

	users, err := s.q.DigestCandidateUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("digest: loading candidates: %w", err)
	}

	for _, u := range users {
		res, err := s.ForUser(ctx, u)
		if err != nil {
			// One user's failure must not end the run: the remaining users are
			// unaffected and a run that stops at the first error delivers nothing
			// to everybody because of one person's data.
			s.log.Error("digest: user failed",
				"user_id", u.UserID.String(), "err", err)
			res = Result{
				UserID: u.UserID.String(), Outcome: OutcomeFailed,
				Reason: err.Error(),
			}
		}
		rep.Results = append(rep.Results, res)
	}
	return rep, nil
}

// ForUser builds and sends one user's digest.
func (s *Service) ForUser(
	ctx context.Context, u store.DigestCandidateUsersRow,
) (Result, error) {
	startedAt := s.clock.Now()

	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		// A timezone we cannot resolve is a data error, not a reason to guess.
		// Guessing UTC would send at the wrong local hour, which is exactly the
		// failure quiet hours exist to prevent.
		return Result{}, fmt.Errorf("unknown timezone %q: %w", u.Timezone, err)
	}
	local := startedAt.In(loc)
	res := Result{UserID: u.UserID.String(), LocalDate: truncateDay(local)}

	// Quiet hours first, before any work. Deferring costs nothing; composing a
	// digest we are not allowed to send costs a full retrieval and scoring pass.
	if InQuietHours(local, int(u.QuietStart), int(u.QuietEnd)) {
		res.Outcome = OutcomeDeferred
		res.Reason = fmt.Sprintf(
			"local time %s is inside the user's quiet hours (%02d:00–%02d:00 %s); "+
				"deferred, not cancelled", local.Format("15:04"),
			u.QuietStart, u.QuietEnd, u.Timezone)
		return res, nil
	}

	// Already delivered today. Checked BEFORE composing, because a full
	// retrieval and scoring pass for a day that is finished is pure waste — an
	// hourly cron would pay it every hour after the first send.
	existing, err := s.q.GetDigestDay(ctx, store.GetDigestDayParams{
		UserID: u.UserID, LocalDate: pgDate(res.LocalDate),
	})
	switch {
	case err == nil && existing.Outcome == string(OutcomeSent):
		res.Outcome = OutcomeAlreadySent
		res.Reason = "already delivered on " + res.LocalDate.Format("2006-01-02")
		return res, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return res, fmt.Errorf("reading today's digest: %w", err)
	}
	provisional := err == nil

	// The weekly cap, counted over delivered digests only: a day we correctly
	// stayed quiet did not spend the user's attention, so it must not spend
	// their budget.
	sends, err := s.q.CountDigestSendsInWindow(ctx, store.CountDigestSendsInWindowParams{
		UserID:    u.UserID,
		SinceDate: pgDate(truncateDay(local).AddDate(0, 0, -WeeklyWindow)),
	})
	if err != nil {
		return res, fmt.Errorf("counting sends: %w", err)
	}
	if sends >= int64(u.MaxPerWeek) {
		res.Outcome = OutcomeSuppressedCap
		res.Reason = fmt.Sprintf("weekly cap reached: %d of %d in the last %d days",
			sends, u.MaxPerWeek, WeeklyWindow)
		// Recorded, so "why did this user get nothing" is answerable, and so the
		// cap's effect is visible rather than inferred.
		s.record(ctx, u, res, startedAt, nil, provisional)
		return res, nil
	}

	items, considered, belowBar, alreadySent, err := s.compose(ctx, u, local)
	if err != nil {
		return res, err
	}
	res.Items, res.Considered = items, considered
	res.BelowBar, res.AlreadySent = belowBar, alreadySent

	if len(items) == 0 {
		res.Outcome = OutcomeEmpty
		res.Reason = emptyReason(considered, belowBar, alreadySent, u.MinBand)
		if !u.SendWhenEmpty {
			// Recorded so "why did this user get nothing" is answerable. The row
			// is PROVISIONAL: ingestion runs all day, and a later run can still
			// upgrade it if something clears the bar.
			s.record(ctx, u, res, startedAt, nil, provisional)
			res.GeneratedIn = s.clock.Now().Sub(startedAt)
			return res, nil
		}
	}

	row, claimed, err := s.write(ctx, u, res, startedAt, items, provisional)
	if err != nil {
		return res, err
	}
	if !claimed {
		// Another run took this day between our read and our write. Not an error
		// and not a suppression — the work simply belongs to that run, and the
		// unique constraint is what settled it rather than either of us guessing.
		res.Outcome = OutcomeAlreadySent
		res.Reason = "another run claimed " + res.LocalDate.Format("2006-01-02")
		return res, nil
	}

	msg := Render(u, res)
	if err := s.sender.Send(ctx, msg); err != nil {
		if _, merr := s.q.MarkDigestFailed(ctx, store.MarkDigestFailedParams{
			ID: row.ID, Reason: ptr(err.Error()),
		}); merr != nil {
			s.log.Warn("digest: recording failure", "err", merr)
		}
		res.Outcome, res.Reason = OutcomeFailed, err.Error()
		return res, nil
	}

	if _, err := s.q.MarkDigestSent(ctx, store.MarkDigestSentParams{
		ID: row.ID, SentAt: pgTime(s.clock.Now()),
	}); err != nil {
		// The mail went out. Failing here would be reported as "not sent", which
		// is the wrong error in the wrong direction: the user has it.
		s.log.Error("digest: sent but not recorded",
			"user_id", u.UserID.String(), "digest_id", row.ID.String(), "err", err)
	}
	res.Outcome, res.Reason = OutcomeSent, ""
	res.GeneratedIn = s.clock.Now().Sub(startedAt)
	return res, nil
}

// Preview composes a digest and renders it WITHOUT claiming the day or sending.
//
// Exists so a run can be inspected before it consumes anyone's daily slot. It
// deliberately does not touch digest_send: a preview that claims a day would
// silently suppress the real digest, and a consumed day cannot be given back.
// Quiet hours and the weekly cap are reported rather than enforced, since the
// point of a preview is to see what WOULD be sent.
func (s *Service) Preview(
	ctx context.Context, u store.DigestCandidateUsersRow,
) (Message, Result, error) {
	startedAt := s.clock.Now()
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return Message{}, Result{}, fmt.Errorf("unknown timezone %q: %w", u.Timezone, err)
	}
	local := startedAt.In(loc)
	res := Result{UserID: u.UserID.String(), LocalDate: truncateDay(local)}

	items, considered, belowBar, alreadySent, err := s.compose(ctx, u, local)
	if err != nil {
		return Message{}, res, err
	}
	res.Items, res.Considered = items, considered
	res.BelowBar, res.AlreadySent = belowBar, alreadySent

	switch {
	case InQuietHours(local, int(u.QuietStart), int(u.QuietEnd)):
		res.Outcome = OutcomeDeferred
		res.Reason = fmt.Sprintf("would defer: local time %s is inside quiet hours "+
			"(%02d:00-%02d:00 %s)", local.Format("15:04"),
			u.QuietStart, u.QuietEnd, u.Timezone)
	case len(items) == 0:
		res.Outcome = OutcomeEmpty
		res.Reason = emptyReason(considered, belowBar, alreadySent, u.MinBand)
	default:
		res.Outcome = OutcomeSent
	}
	return Render(u, res), res, nil
}

// compose selects what goes in the digest.
func (s *Service) compose(
	ctx context.Context, u store.DigestCandidateUsersRow, local time.Time,
) (items []Item, considered, belowBar, alreadySent int, err error) {
	mres, err := s.matcher.MatchForUser(ctx, u.UserID, 0)
	if err != nil {
		if errors.Is(err, matching.ErrNoProfile) {
			return nil, 0, 0, 0, nil
		}
		return nil, 0, 0, 0, fmt.Errorf("matching: %w", err)
	}
	considered = len(mres.Matches)

	seen, err := s.q.RecentDigestOpportunityIDs(ctx,
		store.RecentDigestOpportunityIDsParams{
			UserID:    u.UserID,
			SinceDate: pgDate(truncateDay(local).Add(-RepeatWindow)),
		})
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("recent digest ids: %w", err)
	}
	already := make(map[string]struct{}, len(seen))
	for _, id := range seen {
		already[id.String()] = struct{}{}
	}

	// Apply the bar first, then the repeat filter, so belowBar counts what the
	// bar rejected rather than being polluted by repeats.
	picked := make([]matching.Match, 0, MaxItems)
	for _, m := range mres.Matches {
		if !ClearsBar(m.Fit.Band(), u.MinBand) {
			belowBar++
			continue
		}
		if _, dup := already[m.Opportunity.ID.String()]; dup {
			alreadySent++
			continue
		}
		if len(picked) >= MaxItems {
			break
		}
		picked = append(picked, m)
	}
	if len(picked) == 0 {
		return nil, considered, belowBar, alreadySent, nil
	}

	// The posting, for the same reason the feed needs it: a digest entry with no
	// company, salary or apply link is not something anyone can act on, and the
	// display rules forbid presenting a role whose open state is unknown.
	ids := make([]pgtype.UUID, 0, len(picked))
	for _, m := range picked {
		ids = append(ids, m.Opportunity.ID)
	}
	postings, err := s.opps.SummariesByID(ctx, ids)
	if err != nil {
		return nil, considered, belowBar, alreadySent,
			fmt.Errorf("digest postings: %w", err)
	}
	for _, m := range picked {
		p, ok := postings[m.Opportunity.ID.String()]
		if !ok {
			// Closed between scoring and now. Never mail a dead link.
			continue
		}
		items = append(items, Item{Match: m, Posting: p})
	}
	return items, considered, belowBar, alreadySent, nil
}

// record writes an outcome that sends nothing, and never fails the run for it.
func (s *Service) record(
	ctx context.Context, u store.DigestCandidateUsersRow, res Result,
	startedAt time.Time, items []Item, provisional bool,
) {
	if _, _, err := s.write(ctx, u, res, startedAt, items, provisional); err != nil {
		s.log.Warn("digest: recording outcome",
			"user_id", u.UserID.String(), "outcome", string(res.Outcome), "err", err)
	}
}

// write claims the user's local day, or upgrades a day already held
// provisionally by an earlier run.
//
// Two statements rather than one upsert, because they mean different things and
// only one of them may overwrite: the insert is guarded by the unique
// constraint, and the update is guarded by outcome <> 'sent'. An ON CONFLICT
// UPDATE would happily overwrite a delivered digest, which is the daily cap
// failing silently.
func (s *Service) write(
	ctx context.Context, u store.DigestCandidateUsersRow, res Result,
	startedAt time.Time, items []Item, provisional bool,
) (store.DigestSend, bool, error) {
	ids := make([]pgtype.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.Match.Opportunity.ID)
	}
	weights := ""
	if len(items) > 0 {
		weights = items[0].Match.Fit.WeightsVersion
	}

	if provisional {
		row, err := s.q.UpgradeDigestDay(ctx, store.UpgradeDigestDayParams{
			UserID:         u.UserID,
			LocalDate:      pgDate(res.LocalDate),
			Outcome:        string(res.Outcome),
			Reason:         ptr(res.Reason),
			ItemCount:      int32(len(items)),
			OpportunityIds: ids,
			WeightsVersion: ptr(weights),
			GeneratedAt:    pgTime(s.clock.Now()),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The row turned terminal under us: it was 'empty' when we read it and
			// is 'sent' now. Another run delivered; ours must not.
			return store.DigestSend{}, false, nil
		}
		if err != nil {
			return store.DigestSend{}, false, fmt.Errorf("upgrading digest day: %w", err)
		}
		return row, true, nil
	}

	row, err := s.q.ClaimDigestDay(ctx, store.ClaimDigestDayParams{
		UserID:              u.UserID,
		TenantID:            u.TenantID,
		LocalDate:           pgDate(res.LocalDate),
		GenerationStartedAt: pgTime(startedAt),
		GeneratedAt:         pgTime(s.clock.Now()),
		Outcome:             string(res.Outcome),
		Reason:              ptr(res.Reason),
		ItemCount:           int32(len(items)),
		OpportunityIds:      ids,
		WeightsVersion:      ptr(weights),
		ProfileVersion:      &u.ProfileVersion,
		MinBand:             ptr(u.MinBand),
		Sender:              s.sender.Name(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.DigestSend{}, false, nil
	}
	if err != nil {
		return store.DigestSend{}, false, fmt.Errorf("claiming digest day: %w", err)
	}
	return row, true, nil
}

// InQuietHours reports whether a local time falls inside a quiet window.
//
// Pure, and separate from everything else, because the wrap-around case is the
// one that breaks: a window of 21:00–08:00 is the normal configuration and it is
// NOT expressible as start <= h < end. Writing it as a BETWEEN silently inverts
// it and mails people at 3am.
func InQuietHours(local time.Time, start, end int) bool {
	h := local.Hour()
	if start == end {
		// A zero-width window means no quiet hours, not a whole quiet day.
		return false
	}
	if start < end {
		return h >= start && h < end
	}
	return h >= start || h < end
}

// ClearsBar reports whether a band meets the user's minimum.
//
// Deliberately does not treat "Not enough information" as a low score. It is not
// a score at all — it says we could observe less than 60% of the model — and a
// digest built on it would be interrupting someone on the strength of data we
// admit we do not have. It never clears any bar, including the lowest.
func ClearsBar(band matching.Band, minBand string) bool {
	switch minBand {
	case BarStrong:
		return band == matching.BandStrong
	case BarWorthALook:
		return band == matching.BandStrong || band == matching.BandWorth
	default:
		// An unrecognized bar sends nothing rather than everything. Failing closed
		// is the only safe direction for a rule about interrupting people.
		return false
	}
}

// emptyReason writes the "nothing met your bar today" state in words.
//
// Blueprint §4.3 requires this state to be explicit, and §3 forbids padding to a
// count. The distinction it has to carry is between a quiet market and a bar set
// high — those need opposite responses from the reader, and a single "no matches
// today" cannot tell them apart.
func emptyReason(considered, belowBar, alreadySent int, minBand string) string {
	bar := "Strong fit"
	if minBand == BarWorthALook {
		bar = "Worth a look"
	}
	switch {
	case considered == 0:
		return "nothing was eligible today: no posting in the corpus passed this " +
			"user's own filters, so there was nothing to score"
	case belowBar > 0 && alreadySent > 0:
		return fmt.Sprintf(
			"%d roles were eligible; none reached %q. %d cleared the bar but were "+
				"already sent in the last %d days", considered, bar, alreadySent,
			int(RepeatWindow.Hours()/24))
	case alreadySent > 0:
		return fmt.Sprintf(
			"%d roles cleared the bar but were all sent in the last %d days",
			alreadySent, int(RepeatWindow.Hours()/24))
	default:
		return fmt.Sprintf("%d roles were eligible; none reached %q",
			considered, bar)
	}
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func ptr[T any](v T) *T { return &v }
