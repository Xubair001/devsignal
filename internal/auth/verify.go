package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/mail"
	"github.com/Xubair001/devsignal/internal/store"
)

// Token purposes. The database CHECK constraint holds the same two values.
const (
	PurposeEmailVerify   = "email_verify"
	PurposePasswordReset = "password_reset"
)

// VerificationTTL is how long a verification link lives.
//
// Long enough that a link found the next morning still works, short enough that
// an address abandoned mid-signup does not stay claimable for a month.
const VerificationTTL = 48 * time.Hour

var (
	// ErrVerificationInvalid covers expired, already-used and unknown alike. The
	// caller has no use for the distinction, and telling them which it was is
	// free reconnaissance on whether an address is registered.
	ErrVerificationInvalid = errors.New("auth: verification link is invalid or expired")
	// ErrAlreadyVerified lets a resend be a no-op rather than an error.
	ErrAlreadyVerified = errors.New("auth: address is already verified")
)

// Mailer is the transport verification email goes through.
//
// Transactional mail, so it is deliberately NOT gated on digest consent: a user
// who withdraws digest consent still needs to verify an address and reset a
// password. The consent check lives in internal/digest and nowhere near here.
type Mailer interface {
	Send(ctx context.Context, m mail.Message) error
	Name() string
}

// WithMailer attaches a transport. Without one, verification tokens are still
// issued and recorded — they simply cannot be delivered, which is reported
// rather than hidden.
func (s *Service) WithMailer(m Mailer, baseURL string) *Service {
	s.mailer = m
	s.baseURL = baseURL
	return s
}

// IssueEmailVerification creates a link and tries to send it.
//
// Returns the PLAINTEXT token alongside the error. That is deliberate and it is
// only used by the CLI and by tests: with no transport configured there is
// otherwise no way to complete a signup locally, and a flow that cannot be
// exercised is a flow nobody has checked. It is never returned over HTTP —
// see the handler, which discards it.
func (s *Service) IssueEmailVerification(
	ctx context.Context, userID pgtype.UUID, email string,
) (plaintext string, err error) {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("auth: loading user: %w", err)
	}
	if user.EmailVerifiedAt.Valid {
		return "", ErrAlreadyVerified
	}

	secret, _, err := NewToken()
	if err != nil {
		return "", fmt.Errorf("auth: generating token: %w", err)
	}

	if err := s.inTx(ctx, func(q *store.Queries) error {
		// Supersede any outstanding link first, so a resend leaves exactly one
		// live token. Two working links means the older one is a second key
		// nobody knows is outstanding.
		if _, eerr := q.ExpireUserTokensOfPurpose(ctx,
			store.ExpireUserTokensOfPurposeParams{
				UserID: userID, Purpose: PurposeEmailVerify,
			}); eerr != nil {
			return fmt.Errorf("expiring old tokens: %w", eerr)
		}
		if _, cerr := q.CreateUserToken(ctx, store.CreateUserTokenParams{
			UserID: userID, Purpose: PurposeEmailVerify,
			TokenHash: HashToken(secret),
			ExpiresAt: pgtype.Timestamptz{
				Time: s.clock.Now().Add(VerificationTTL), Valid: true,
			},
		}); cerr != nil {
			return fmt.Errorf("creating token: %w", cerr)
		}
		return NewAuditor(q).Append(ctx, Event{
			ActorID: &userID, TenantID: &user.TenantID,
			Action:  "email.verification_issued",
			Subject: "user:" + userID.String(),
			// The address is NOT recorded. Hard rule 13: the audit log gets the
			// user id, never anything about the person.
			Metadata: map[string]any{"purpose": PurposeEmailVerify},
		})
	}); err != nil {
		return "", err
	}

	if s.mailer == nil {
		// Loud, and not an error: the token exists and is usable. A signup that
		// fails because mail is unconfigured would be worse than one that
		// completes with the link in a log.
		s.log.Warn("email verification issued but no transport is configured",
			"user_id", userID.String())
		return secret, nil
	}

	msg := verificationMessage(email, userID.String(), s.baseURL, secret)
	if serr := s.mailer.Send(ctx, msg); serr != nil {
		// Also not fatal to the signup. The address is unverified either way, and
		// a resend is one click; failing the registration would lose the account.
		s.log.Error("sending verification email",
			"user_id", userID.String(), "sender", s.mailer.Name(), "err", serr)
	}
	return secret, nil
}

// VerifyEmail consumes a token and marks the address verified.
func (s *Service) VerifyEmail(ctx context.Context, secret string) (pgtype.UUID, error) {
	var userID pgtype.UUID
	err := s.inTx(ctx, func(q *store.Queries) error {
		// The UPDATE is the claim: consumed_at IS NULL in its WHERE clause means
		// two concurrent requests cannot both succeed, and a replayed link finds
		// nothing.
		tok, cerr := q.ConsumeUserToken(ctx, store.ConsumeUserTokenParams{
			TokenHash: HashToken(secret), Purpose: PurposeEmailVerify,
		})
		if cerr != nil {
			if errors.Is(cerr, pgx.ErrNoRows) {
				return ErrVerificationInvalid
			}
			return fmt.Errorf("consuming token: %w", cerr)
		}
		if merr := q.MarkEmailVerified(ctx, tok.UserID); merr != nil {
			return fmt.Errorf("marking verified: %w", merr)
		}
		userID = tok.UserID
		uid := tok.UserID
		return NewAuditor(q).Append(ctx, Event{
			ActorID: &uid, Action: "email.verified",
			Subject: "user:" + uid.String(),
		})
	})
	return userID, err
}

// verificationMessage renders the email.
//
// Text and HTML both, and the URL appears as visible text in each. A link whose
// destination a reader cannot see is the shape of a phishing email, and a
// verification message is exactly the genre people are trained to distrust.
func verificationMessage(email, userID, baseURL, secret string) mail.Message {
	link := baseURL + "/verify?token=" + secret
	return mail.Message{
		To:      email,
		UserID:  userID,
		Kind:    "verify",
		Subject: "Confirm your email address",
		Text: "Confirm your email address to finish setting up DevSignal.\n\n" +
			link + "\n\n" +
			"The link works once and expires in 48 hours.\n\n" +
			"If you did not create an account, ignore this — the address is not\n" +
			"added to anything until it is confirmed.\n",
		HTML: `<div style="font-family:ui-sans-serif,system-ui,sans-serif;` +
			`max-width:520px;color:#0c1b26">` +
			`<h1 style="font-size:18px">Confirm your email address</h1>` +
			`<p style="font-size:14px;line-height:1.6">Confirm your address to finish ` +
			`setting up DevSignal.</p>` +
			`<p style="font-size:14px;line-height:1.6"><a href="` + link +
			`" style="color:#0b6fa4;font-weight:600">` + link + `</a></p>` +
			`<p style="font-size:13px;color:#46586a;line-height:1.6">The link works once ` +
			`and expires in 48 hours. If you did not create an account, ignore this — the ` +
			`address is not added to anything until it is confirmed.</p></div>`,
	}
}
