//go:build integration

package digest

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/store"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// recordingSender counts deliveries without touching a network.
type recordingSender struct {
	sent []Message
	fail error
}

func (r *recordingSender) Name() string { return "recording" }

func (r *recordingSender) Send(_ context.Context, m Message) error {
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, m)
	return nil
}

// seedRecipient creates a consenting, verified user with a profile.
func seedRecipient(
	t *testing.T, pool *pgxpool.Pool, tz string, maxPerWeek int,
) (pgtype.UUID, store.DigestCandidateUsersRow) {
	t.Helper()
	ctx := context.Background()

	var tenantID, userID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (display_name) VALUES ('Digest Test') RETURNING id`).
		Scan(&tenantID); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO app_user (tenant_id, email, password_hash, email_verified_at, status)
		VALUES ($1,$2,'x',now(),'active') RETURNING id`,
		tenantID, "digest-"+uuid.NewString()[:8]+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO profile (user_id, tenant_id, headline, seniority_ordinal,
		                     target_role_families, profile_version)
		VALUES ($1,$2,'Senior backend engineer',4,ARRAY['backend'],1)`,
		userID, tenantID); err != nil {
		t.Fatalf("profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_setting (user_id, tenant_id, timezone, quiet_start,
		    quiet_end, digest_enabled, max_per_week, min_band, send_when_empty,
		    digest_consent_at, digest_consent_wording_version)
		VALUES ($1,$2,$3,21,8,true,$4,'strong',false,now(),'test-v1')`,
		userID, tenantID, tz, maxPerWeek); err != nil {
		t.Fatalf("notification_setting: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM app_user WHERE id=$1`, userID)
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id=$1`, tenantID)
	})

	return userID, store.DigestCandidateUsersRow{
		UserID: userID, TenantID: tenantID, Timezone: tz,
		QuietStart: 21, QuietEnd: 8, MaxPerWeek: int16(maxPerWeek),
		MinBand: BarStrong, SendWhenEmpty: false, ProfileVersion: 1,
	}
}

func newTestService(pool *pgxpool.Pool, s Sender, clock Clock) *Service {
	// A nil matcher is fine for the tests below: every one of them exercises a
	// gate that fires BEFORE composition — quiet hours, the cap, an
	// already-delivered day. Composition itself is covered by the unit tests and
	// by a live run, and wiring a real matcher here would test the matcher.
	return &Service{
		pool: pool, q: store.New(pool), sender: s, clock: clock, log: quietLog(),
	}
}

// TestQuietHoursDeferAndWriteNoRow is the distinction the schema depends on.
//
// Quiet hours DEFER; they do not cancel. If a run inside the window claimed the
// day, the digest would be lost entirely for anyone whose cron happened to fire
// at 3am — and the unique constraint would then block the run that should have
// sent it. Writing no row is what keeps the day claimable.
func TestQuietHoursDeferAndWriteNoRow(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	// 02:30 UTC is inside a 21:00–08:00 window.
	clock := fixedClock{t: time.Date(2026, 8, 26, 2, 30, 0, 0, time.UTC)}
	_, row := seedRecipient(t, pool, "UTC", 5)
	sender := &recordingSender{}
	svc := newTestService(pool, sender, clock)

	res, err := svc.ForUser(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDeferred {
		t.Fatalf("outcome %q, want %q", res.Outcome, OutcomeDeferred)
	}
	if len(sender.sent) != 0 {
		t.Error("a digest was sent during quiet hours")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM digest_send WHERE user_id=$1`, row.UserID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("quiet hours wrote %d digest_send rows; the day must stay claimable", n)
	}
}

// TestQuietHoursUseTheUsersTimezoneNotOurs is the bug a UTC-only implementation
// ships with and never notices in one office.
func TestQuietHoursUseTheUsersTimezoneNotOurs(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	// 14:30 UTC. Loud in UTC, but 02:30 the next day in Auckland (UTC+12).
	clock := fixedClock{t: time.Date(2026, 8, 26, 14, 30, 0, 0, time.UTC)}

	_, utcUser := seedRecipient(t, pool, "UTC", 5)
	_, nzUser := seedRecipient(t, pool, "Pacific/Auckland", 5)
	svc := newTestService(pool, &recordingSender{}, clock)

	nz, err := svc.ForUser(ctx, nzUser)
	if err != nil {
		t.Fatal(err)
	}
	if nz.Outcome != OutcomeDeferred {
		t.Errorf("Auckland user at 02:30 local: outcome %q, want deferred", nz.Outcome)
	}

	// The UTC user is awake, so this one gets as far as composition. With a nil
	// matcher that would panic, so only the timezone branch is asserted: it must
	// NOT be the deferred one.
	func() {
		defer func() { _ = recover() }()
		u, _ := svc.ForUser(ctx, utcUser)
		if u.Outcome == OutcomeDeferred {
			t.Error("UTC user at 14:30 local was treated as inside quiet hours")
		}
	}()
}

// TestWeeklyCapCountsDeliveredDigestsOnly guards the arithmetic that decides how
// often we interrupt someone. A day we correctly stayed quiet did not spend
// their attention, so counting it against the cap would silence a user for
// having a quiet week.
func TestWeeklyCapCountsDeliveredDigestsOnly(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	_, row := seedRecipient(t, pool, "UTC", 2)

	// Three empty days and one delivered day inside the window.
	for i, outcome := range []string{"empty", "empty", "empty", "sent"} {
		d := now.AddDate(0, 0, -(i + 1))
		if _, err := pool.Exec(ctx, `
			INSERT INTO digest_send (user_id, tenant_id, local_date,
			    generation_started_at, generated_at, outcome, sender)
			VALUES ($1,$2,$3,$4,$4,$5,'test')`,
			row.UserID, row.TenantID, d, d, outcome); err != nil {
			t.Fatal(err)
		}
	}

	q := store.New(pool)
	sends, err := q.CountDigestSendsInWindow(ctx, store.CountDigestSendsInWindowParams{
		UserID:    row.UserID,
		SinceDate: pgDate(now.AddDate(0, 0, -WeeklyWindow)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatalf("counted %d sends, want 1: only delivered digests spend the cap", sends)
	}
}

// TestCapSuppressionIsRecordedWithAReason: "why did this user get nothing" must
// always have an answer, and a suppression that leaves no trace is
// indistinguishable from a crash.
func TestCapSuppressionIsRecordedWithAReason(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	_, row := seedRecipient(t, pool, "UTC", 1)

	yesterday := now.AddDate(0, 0, -1)
	if _, err := pool.Exec(ctx, `
		INSERT INTO digest_send (user_id, tenant_id, local_date,
		    generation_started_at, generated_at, outcome, sender)
		VALUES ($1,$2,$3,$4,$4,'sent','test')`,
		row.UserID, row.TenantID, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}

	sender := &recordingSender{}
	svc := newTestService(pool, sender, fixedClock{t: now})
	res, err := svc.ForUser(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuppressedCap {
		t.Fatalf("outcome %q, want %q", res.Outcome, OutcomeSuppressedCap)
	}
	if len(sender.sent) != 0 {
		t.Error("a digest went out past the weekly cap")
	}

	var reason *string
	if err := pool.QueryRow(ctx, `
		SELECT reason FROM digest_send WHERE user_id=$1 AND local_date=$2`,
		row.UserID, now).Scan(&reason); err != nil {
		t.Fatalf("the suppression left no row: %v", err)
	}
	if reason == nil || *reason == "" {
		t.Error("a suppressed digest was recorded with no reason")
	}
}

// TestDeliveredDayIsNeverRecomposed checks the cheap path AND the daily cap.
//
// A delivered day must short-circuit before composition: an hourly cron would
// otherwise pay a full retrieval and scoring pass every hour after the first
// send. The nil matcher is what proves it — if composition ran, this panics.
func TestDeliveredDayIsNeverRecomposed(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	_, row := seedRecipient(t, pool, "UTC", 5)

	if _, err := pool.Exec(ctx, `
		INSERT INTO digest_send (user_id, tenant_id, local_date,
		    generation_started_at, generated_at, outcome, item_count, sender)
		VALUES ($1,$2,$3,$4,$4,'sent',3,'test')`,
		row.UserID, row.TenantID, now, now); err != nil {
		t.Fatal(err)
	}

	sender := &recordingSender{}
	svc := newTestService(pool, sender, fixedClock{t: now})
	res, err := svc.ForUser(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAlreadySent {
		t.Fatalf("outcome %q, want %q", res.Outcome, OutcomeAlreadySent)
	}
	if len(sender.sent) != 0 {
		t.Error("a second digest went out on a day already delivered")
	}
}

// TestOneDigestPerLocalDayIsEnforcedByTheDatabase is the daily cap from
// blueprint §4.3, and it must not depend on application code being careful.
// Two workers overlapping is the normal case, not the exotic one.
func TestOneDigestPerLocalDayIsEnforcedByTheDatabase(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	_, row := seedRecipient(t, pool, "UTC", 5)

	insert := func() error {
		_, err := pool.Exec(ctx, `
			INSERT INTO digest_send (user_id, tenant_id, local_date,
			    generation_started_at, generated_at, outcome, sender)
			VALUES ($1,$2,$3,$4,$4,'sent','test')`,
			row.UserID, row.TenantID, now, now)
		return err
	}
	if err := insert(); err != nil {
		t.Fatal(err)
	}
	if err := insert(); err == nil {
		t.Fatal("the database allowed two digests for one user on one local day")
	}
}

// TestProvisionalEmptyDayIsUpgradedNotDuplicated.
//
// An 'empty' outcome records what was true when it was written, not a promise
// about the rest of the day: ingestion runs continuously, and a Strong fit that
// appears at 10:00 should reach the user today. The upgrade is guarded on
// outcome <> 'sent', so a delivered digest can never be overwritten.
func TestProvisionalEmptyDayIsUpgradedNotDuplicated(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	_, row := seedRecipient(t, pool, "UTC", 5)
	q := store.New(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO digest_send (user_id, tenant_id, local_date,
		    generation_started_at, generated_at, outcome, sender)
		VALUES ($1,$2,$3,$4,$4,'empty','test')`,
		row.UserID, row.TenantID, now, now); err != nil {
		t.Fatal(err)
	}

	up, err := q.UpgradeDigestDay(ctx, store.UpgradeDigestDayParams{
		UserID: row.UserID, LocalDate: pgDate(now),
		Outcome: string(OutcomeSent), ItemCount: 2,
		OpportunityIds: []pgtype.UUID{}, GeneratedAt: pgTime(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Outcome != string(OutcomeSent) || up.Attempts != 2 {
		t.Errorf("upgraded to outcome=%q attempts=%d, want sent/2", up.Outcome, up.Attempts)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM digest_send WHERE user_id=$1`, row.UserID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rows for one day; the upgrade duplicated instead of replacing", n)
	}

	// And a delivered day must now refuse a further upgrade.
	if _, err := q.UpgradeDigestDay(ctx, store.UpgradeDigestDayParams{
		UserID: row.UserID, LocalDate: pgDate(now),
		Outcome: string(OutcomeEmpty), OpportunityIds: []pgtype.UUID{},
		GeneratedAt: pgTime(now),
	}); err == nil {
		t.Error("a delivered digest was overwritten; the daily cap is not holding")
	}
}

// TestWithdrawnConsentRemovesTheRecipient. A withdrawal that leaves someone on
// the list is the most expensive possible bug in this package.
func TestWithdrawnConsentRemovesTheRecipient(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	q := store.New(pool)
	userID, _ := seedRecipient(t, pool, "UTC", 5)

	found := func() bool {
		rows, err := q.DigestCandidateUsers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.UserID == userID {
				return true
			}
		}
		return false
	}

	if !found() {
		t.Fatal("a consenting, verified user is not a candidate")
	}

	if _, err := q.WithdrawDigestConsent(ctx, store.WithdrawDigestConsentParams{
		UserID:      userID,
		WithdrawnAt: pgTime(time.Now().UTC()),
	}); err != nil {
		t.Fatal(err)
	}
	if found() {
		t.Error("a user who withdrew consent is still a digest candidate")
	}

	// The consent record survives the withdrawal: proving we HAD consent, and
	// that it was withdrawn on a date, is the point of recording it.
	var consentAt, withdrawnAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT digest_consent_at, digest_consent_withdrawn_at
		  FROM notification_setting WHERE user_id=$1`, userID).
		Scan(&consentAt, &withdrawnAt); err != nil {
		t.Fatal(err)
	}
	if consentAt == nil {
		t.Error("the original consent record was erased by the withdrawal")
	}
	if withdrawnAt == nil {
		t.Error("the withdrawal was not recorded")
	}
}

// TestUnverifiedEmailIsNeverACandidate. Mailing an unverified address is how a
// sending domain's reputation is lost, and it may not even be the user's address.
func TestUnverifiedEmailIsNeverACandidate(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	q := store.New(pool)
	userID, _ := seedRecipient(t, pool, "UTC", 5)

	if _, err := pool.Exec(ctx,
		`UPDATE app_user SET email_verified_at=NULL WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	rows, err := q.DigestCandidateUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.UserID == userID {
			t.Fatal("a user with an unverified email address is a digest candidate")
		}
	}
}
