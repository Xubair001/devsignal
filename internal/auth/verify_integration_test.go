//go:build integration

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/mail"
	"github.com/Xubair001/devsignal/internal/store"
)

// captureMailer records what would have been sent.
type captureMailer struct {
	sent []mail.Message
	fail error
}

func (c *captureMailer) Name() string { return "capture" }

func (c *captureMailer) Send(_ context.Context, m mail.Message) error {
	if c.fail != nil {
		return c.fail
	}
	c.sent = append(c.sent, m)
	return nil
}

func newVerifyUser(t *testing.T, svc *Service) (pgtype.UUID, string) {
	t.Helper()
	email := "verify-" + uuid.NewString()[:12] + "@example.test"
	_, ident, err := svc.Register(context.Background(), email,
		"a-sufficiently-long-password", "test")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return ident.UserID, email
}

// TestVerificationLinkWorksExactlyOnce.
//
// The single-use property is the whole security model of a verification link:
// the token IS the authorization, it arrives over email, and email is forwarded,
// logged and archived. A replayable link is a permanent credential sitting in
// someone's inbox.
func TestVerificationLinkWorksExactlyOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mailer := &captureMailer{}
	svc := newSvc(t, pool).WithMailer(mailer, "http://console.test")

	userID, email := newVerifyUser(t, svc)
	secret, err := svc.IssueEmailVerification(ctx, userID, email)
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" {
		t.Fatal("no token was issued")
	}

	if _, err := svc.VerifyEmail(ctx, secret); err != nil {
		t.Fatalf("first verify failed: %v", err)
	}

	// The address is now verified.
	user, err := store.New(pool).GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !user.EmailVerifiedAt.Valid {
		t.Error("email_verified_at was not set")
	}

	// And the link is spent.
	if _, err := svc.VerifyEmail(ctx, secret); !errors.Is(err, ErrVerificationInvalid) {
		t.Errorf("replay gave %v, want ErrVerificationInvalid", err)
	}
}

// TestUnknownAndSpentTokensAreIndistinguishable.
//
// Different errors for "never existed" and "already used" would let a caller
// probe which links were real. One error for both, the same way every token
// failure in this package reports identically.
func TestUnknownAndSpentTokensAreIndistinguishable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := newSvc(t, pool).WithMailer(&captureMailer{}, "http://console.test")

	userID, email := newVerifyUser(t, svc)
	secret, err := svc.IssueEmailVerification(ctx, userID, email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyEmail(ctx, secret); err != nil {
		t.Fatal(err)
	}

	_, spent := svc.VerifyEmail(ctx, secret)
	_, unknown := svc.VerifyEmail(ctx, "a-token-that-was-never-issued")
	if spent.Error() != unknown.Error() {
		t.Errorf("spent (%v) and unknown (%v) report differently", spent, unknown)
	}
}

// TestResendSupersedesTheOutstandingLink.
//
// Two working links means the older one is a second key nobody knows is
// outstanding. A resend has to invalidate what it replaces.
func TestResendSupersedesTheOutstandingLink(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := newSvc(t, pool).WithMailer(&captureMailer{}, "http://console.test")

	userID, email := newVerifyUser(t, svc)
	first, err := svc.IssueEmailVerification(ctx, userID, email)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.IssueEmailVerification(ctx, userID, email)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the resend reissued the same token")
	}

	if _, err := svc.VerifyEmail(ctx, first); !errors.Is(err, ErrVerificationInvalid) {
		t.Error("the superseded link still works; a resend left two live keys")
	}
	if _, err := svc.VerifyEmail(ctx, second); err != nil {
		t.Errorf("the newest link does not work: %v", err)
	}
}

// TestExpiredLinkIsRefused. Expiry is enforced in SQL rather than in Go, so a
// clock difference between process and database cannot widen the window.
func TestExpiredLinkIsRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := newSvc(t, pool).WithMailer(&captureMailer{}, "http://console.test")
	userID, _ := newVerifyUser(t, svc)

	secret, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(pool).CreateUserToken(ctx, store.CreateUserTokenParams{
		UserID: userID, Purpose: PurposeEmailVerify, TokenHash: hash,
		ExpiresAt: pgtype.Timestamptz{
			Time: time.Now().UTC().Add(-time.Minute), Valid: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyEmail(ctx, secret); !errors.Is(err, ErrVerificationInvalid) {
		t.Errorf("an expired link was accepted: %v", err)
	}
}

// TestOnlyTheHashIsStored.
//
// A leaked database must not hand someone else's verification link to whoever
// read it. The plaintext exists only long enough to put in an email.
func TestOnlyTheHashIsStored(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := newSvc(t, pool).WithMailer(&captureMailer{}, "http://console.test")
	userID, email := newVerifyUser(t, svc)

	secret, err := svc.IssueEmailVerification(ctx, userID, email)
	if err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_token WHERE token_hash = $1::bytea`,
		[]byte(secret)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the plaintext token is stored in user_token.token_hash")
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_token WHERE token_hash = $1`,
		HashToken(secret)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected exactly one row keyed by the hash, found %d", n)
	}
}

// TestSendFailureDoesNotBlockTheSignup.
//
// Hard rule 7's spirit: one stage must not block another. The account exists and
// the address is unverified either way, and a resend is one click — failing the
// registration because mail is down would lose the account entirely.
func TestSendFailureDoesNotBlockTheSignup(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mailer := &captureMailer{fail: errors.New("smtp exploded")}
	svc := newSvc(t, pool).WithMailer(mailer, "http://console.test")

	userID, email := newVerifyUser(t, svc)
	secret, err := svc.IssueEmailVerification(ctx, userID, email)
	if err != nil {
		t.Fatalf("a send failure propagated to the caller: %v", err)
	}
	// And the token is still usable, so a resend or a logged link completes it.
	if _, err := svc.VerifyEmail(ctx, secret); err != nil {
		t.Errorf("the token issued alongside a failed send does not work: %v", err)
	}
}

// TestVerifiedAddressRefusesReissue: a resend for an already-verified address is
// a no-op, not an error, so the handler can be idempotent.
func TestVerifiedAddressRefusesReissue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := newSvc(t, pool).WithMailer(&captureMailer{}, "http://console.test")
	userID, email := newVerifyUser(t, svc)

	secret, err := svc.IssueEmailVerification(ctx, userID, email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyEmail(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IssueEmailVerification(ctx, userID, email); !errors.Is(err, ErrAlreadyVerified) {
		t.Errorf("reissue for a verified address gave %v, want ErrAlreadyVerified", err)
	}
}

// TestTheEmailContainsAVisibleLink.
//
// A link whose destination the reader cannot see is the shape of a phishing
// email, and a verification message is exactly the genre people are trained to
// distrust. The URL appears as text in both parts.
func TestTheEmailContainsAVisibleLink(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mailer := &captureMailer{}
	svc := newSvc(t, pool).WithMailer(mailer, "http://console.test")
	userID, email := newVerifyUser(t, svc)

	if _, err := svc.IssueEmailVerification(ctx, userID, email); err != nil {
		t.Fatal(err)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(mailer.sent))
	}
	m := mailer.sent[0]
	if m.To != email {
		t.Errorf("addressed to %q, want %q", m.To, email)
	}
	for _, part := range []string{m.Text, m.HTML} {
		if !contains(part, "http://console.test/verify?token=") {
			t.Errorf("the link is not visible as text in one part: %s", part)
		}
	}
}

func contains(h, n string) bool {
	return len(h) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(h); i++ {
			if h[i:i+len(n)] == n {
				return true
			}
		}
		return false
	})()
}
