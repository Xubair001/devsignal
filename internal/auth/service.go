package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
)

// statusActive is the only status permitted to authenticate.
const statusActive = "active"

var (
	ErrEmailTaken    = errors.New("email already registered")
	ErrAccountLocked = errors.New("account temporarily locked")
	ErrSuspended     = errors.New("account not active")
	ErrTokenInvalid  = errors.New("token invalid or expired")
	ErrTokenReplayed = errors.New("refresh token replayed")
	ErrWeakPassword  = errors.New("password too weak")
)

// Policy is stated, not defaulted, so it can be reviewed in one place.
type Policy struct {
	SessionTTL       time.Duration
	RefreshTTL       time.Duration
	LockoutThreshold int32
	LockoutDuration  time.Duration
	MinPasswordLen   int
}

func DefaultPolicy() Policy {
	return Policy{
		SessionTTL:       24 * time.Hour,
		RefreshTTL:       30 * 24 * time.Hour,
		LockoutThreshold: 10,
		LockoutDuration:  15 * time.Minute,
		MinPasswordLen:   12,
	}
}

// Clock is injected so expiry and lockout logic is testable. Never call
// time.Now() in this package (CLAUDE.md rule 14).
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	log    *slog.Logger
	policy Policy
	clock  Clock
}

func NewService(pool *pgxpool.Pool, log *slog.Logger, p Policy, c Clock) *Service {
	if c == nil {
		c = realClock{}
	}
	return &Service{pool: pool, q: store.New(pool), log: log, policy: p, clock: c}
}

// Tokens is what a client receives. The refresh secret is returned once and
// never retrievable again — only its hash is stored.
type Tokens struct {
	SessionToken string
	RefreshToken string
	ExpiresAt    time.Time
}

type Identity struct {
	UserID   pgtype.UUID
	TenantID pgtype.UUID
	// Role is read from the user row on every authentication, so revoking admin
	// takes effect on the very next request rather than when a session expires.
	// It costs nothing extra: Authenticate already loads that row.
	Role string
}

// Roles. The database CHECK constraint holds the same two values, and it is the
// authority — these constants exist so Go code cannot drift from it silently.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// IsAdmin reports whether this identity may reach the operations surface.
//
// A method rather than a comparison at each call site: an authorization check
// spelled out by hand in nine places is nine chances to spell it wrong, and the
// one that gets it wrong is always the destructive endpoint.
func (i Identity) IsAdmin() bool { return i.Role == RoleAdmin }

// inTx runs fn in a transaction, rolling back on error. Audit appends must be in
// the same transaction as the change they describe, or a crash can leave the log
// disagreeing with reality.
func (s *Service) inTx(ctx context.Context, fn func(*store.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Register(ctx context.Context, email, password, userAgent string) (*Tokens, *Identity, error) {
	email = strings.TrimSpace(email)
	if len(password) < s.policy.MinPasswordLen {
		return nil, nil, fmt.Errorf("%w: minimum %d characters", ErrWeakPassword, s.policy.MinPasswordLen)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, nil, err
	}

	var tokens *Tokens
	var ident *Identity
	err = s.inTx(ctx, func(q *store.Queries) error {
		// One tenant per user today; the column exists so organisations later
		// are a policy change rather than a migration across every table.
		tenant, err := q.CreateTenant(ctx, store.CreateTenantParams{
			Kind: "individual", DisplayName: &email,
		})
		if err != nil {
			return fmt.Errorf("create tenant: %w", err)
		}

		user, err := q.CreateUser(ctx, store.CreateUserParams{
			TenantID: tenant.ID, Email: email, PasswordHash: hash,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return ErrEmailTaken
			}
			return fmt.Errorf("create user: %w", err)
		}

		tokens, err = s.issue(ctx, q, user.ID, userAgent)
		if err != nil {
			return err
		}
		ident = &Identity{UserID: user.ID, TenantID: user.TenantID, Role: user.Role}

		return NewAuditor(q).Append(ctx, Event{
			ActorID: &user.ID, TenantID: &user.TenantID,
			Action: "user.registered", Subject: user.ID.String(),
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return tokens, ident, nil
}

func (s *Service) Login(ctx context.Context, email, password, userAgent string) (*Tokens, *Identity, error) {
	user, err := s.q.GetUserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		// Same error and comparable work for an unknown email as for a wrong
		// password: otherwise the endpoint enumerates accounts.
		_, _ = HashPassword("dummy-work-to-equalise-timing")
		return nil, nil, ErrInvalidCredentials
	}

	if user.LockedUntil.Valid && user.LockedUntil.Time.After(s.clock.Now()) {
		return nil, nil, ErrAccountLocked
	}
	if user.Status != statusActive {
		return nil, nil, ErrSuspended
	}

	if err := VerifyPassword(user.PasswordHash, password); err != nil {
		res, ferr := s.q.RecordLoginFailure(ctx, store.RecordLoginFailureParams{
			Threshold: s.policy.LockoutThreshold,
			Lockout:   pgInterval(s.policy.LockoutDuration),
			ID:        user.ID,
		})
		if ferr != nil {
			s.log.Warn("recording login failure", "err", ferr)
		} else if res.LockedUntil.Valid {
			s.log.Warn("account locked", "user_id", user.ID.String(), "failed", res.FailedLogins)
		}
		return nil, nil, ErrInvalidCredentials
	}

	var tokens *Tokens
	err = s.inTx(ctx, func(q *store.Queries) error {
		if err := q.RecordLoginSuccess(ctx, user.ID); err != nil {
			return err
		}
		// Transparent upgrade if policy has been raised since this hash was made.
		if NeedsRehash(user.PasswordHash) {
			if nh, herr := HashPassword(password); herr == nil {
				if err := q.UpdateUserPasswordHash(ctx, store.UpdateUserPasswordHashParams{
					ID: user.ID, PasswordHash: nh,
				}); err != nil {
					return err
				}
			}
		}
		var ierr error
		tokens, ierr = s.issue(ctx, q, user.ID, userAgent)
		if ierr != nil {
			return ierr
		}
		return NewAuditor(q).Append(ctx, Event{
			ActorID: &user.ID, TenantID: &user.TenantID,
			Action: "user.login", Subject: user.ID.String(),
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return tokens, &Identity{UserID: user.ID, TenantID: user.TenantID, Role: user.Role}, nil
}

// issue creates a session plus the first refresh token of a new family.
func (s *Service) issue(ctx context.Context, q *store.Queries, userID pgtype.UUID, userAgent string) (*Tokens, error) {
	now := s.clock.Now()

	sessSecret, sessHash, err := NewToken()
	if err != nil {
		return nil, err
	}
	sess, err := q.CreateSession(ctx, store.CreateSessionParams{
		UserID:    userID,
		TokenHash: sessHash,
		UserAgent: &userAgent,
		ExpiresAt: pgTime(now.Add(s.policy.SessionTTL)),
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	refSecret, refHash, err := NewToken()
	if err != nil {
		return nil, err
	}
	family := newUUID()
	if _, err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{
		FamilyID:  family,
		UserID:    userID,
		SessionID: sess.ID,
		TokenHash: refHash,
		ExpiresAt: pgTime(now.Add(s.policy.RefreshTTL)),
	}); err != nil {
		return nil, fmt.Errorf("create refresh: %w", err)
	}

	return &Tokens{
		SessionToken: sessSecret,
		RefreshToken: refSecret,
		ExpiresAt:    sess.ExpiresAt.Time,
	}, nil
}

// Refresh rotates a refresh token.
//
// Reuse detection is the whole point: presenting a token that already has
// used_at means either the client retried or an attacker replayed a stolen
// token. We cannot distinguish them, so we revoke the entire family and force a
// fresh login. Failing safe here is the only defensible choice.
func (s *Service) Refresh(ctx context.Context, refreshSecret, userAgent string) (*Tokens, *Identity, error) {
	rt, err := s.q.GetRefreshByHash(ctx, HashToken(refreshSecret))
	if err != nil {
		return nil, nil, ErrTokenInvalid
	}

	if rt.UsedAt.Valid {
		revoked := s.revokeFamily(ctx, rt.FamilyID, rt.UserID)
		s.log.Warn("refresh token replayed; family revoked",
			"user_id", rt.UserID.String(), "tokens_revoked", revoked)
		return nil, nil, ErrTokenReplayed
	}
	if rt.RevokedAt.Valid || !rt.ExpiresAt.Time.After(s.clock.Now()) {
		return nil, nil, ErrTokenInvalid
	}

	user, err := s.q.GetUserByID(ctx, rt.UserID)
	if err != nil {
		return nil, nil, ErrTokenInvalid
	}
	if user.Status != statusActive {
		return nil, nil, ErrSuspended
	}

	var tokens *Tokens
	err = s.inTx(ctx, func(q *store.Queries) error {
		now := s.clock.Now()

		sessSecret, sessHash, err := NewToken()
		if err != nil {
			return err
		}
		sess, err := q.CreateSession(ctx, store.CreateSessionParams{
			UserID: rt.UserID, TokenHash: sessHash, UserAgent: &userAgent,
			ExpiresAt: pgTime(now.Add(s.policy.SessionTTL)),
		})
		if err != nil {
			return err
		}

		newSecret, newHash, err := NewToken()
		if err != nil {
			return err
		}
		// Same family: rotation, not a new login.
		next, err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{
			FamilyID: rt.FamilyID, UserID: rt.UserID, SessionID: sess.ID,
			TokenHash: newHash, ExpiresAt: pgTime(now.Add(s.policy.RefreshTTL)),
		})
		if err != nil {
			return err
		}
		if err := q.MarkRefreshUsed(ctx, store.MarkRefreshUsedParams{
			ID: rt.ID, ReplacedBy: next.ID,
		}); err != nil {
			return err
		}
		// The superseded session goes away with its token.
		if err := q.RevokeSession(ctx, rt.SessionID); err != nil {
			return err
		}

		tokens = &Tokens{SessionToken: sessSecret, RefreshToken: newSecret, ExpiresAt: sess.ExpiresAt.Time}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return tokens, &Identity{UserID: user.ID, TenantID: user.TenantID, Role: user.Role}, nil
}

func (s *Service) revokeFamily(ctx context.Context, family, userID pgtype.UUID) int64 {
	var n int64
	err := s.inTx(ctx, func(q *store.Queries) error {
		var err error
		if n, err = q.RevokeRefreshFamily(ctx, family); err != nil {
			return err
		}
		if _, err = q.RevokeSessionsForFamily(ctx, family); err != nil {
			return err
		}
		return NewAuditor(q).Append(ctx, Event{
			ActorID: &userID, Action: "auth.refresh_replayed",
			Subject: family.String(),
			// Metadata is non-identifying by design.
			Metadata: map[string]any{"tokens_revoked": n},
		})
	})
	if err != nil {
		s.log.Error("revoking replayed family", "err", err)
	}
	return n
}

// Authenticate resolves a session token. Returns ErrTokenInvalid for expired,
// revoked and unknown alike — the caller has no use for the distinction.
func (s *Service) Authenticate(ctx context.Context, sessionSecret string) (*Identity, error) {
	sess, err := s.q.GetLiveSessionByHash(ctx, HashToken(sessionSecret))
	if err != nil {
		return nil, ErrTokenInvalid
	}
	user, err := s.q.GetUserByID(ctx, sess.UserID)
	if err != nil || user.Status != "active" {
		return nil, ErrTokenInvalid
	}
	if err := s.q.TouchSession(ctx, sess.ID); err != nil {
		s.log.Warn("touch session", "err", err)
	}
	return &Identity{UserID: user.ID, TenantID: user.TenantID, Role: user.Role}, nil
}

func (s *Service) Logout(ctx context.Context, sessionSecret string) error {
	sess, err := s.q.GetLiveSessionByHash(ctx, HashToken(sessionSecret))
	if err != nil {
		// Deliberate: logging out an already-dead session is success, not an
		// error. Reporting a failure here would let a caller probe which
		// tokens are live.
		//nolint:nilerr // idempotent by design
		return nil
	}
	return s.inTx(ctx, func(q *store.Queries) error {
		if err := q.RevokeSession(ctx, sess.ID); err != nil {
			return err
		}
		return NewAuditor(q).Append(ctx, Event{
			ActorID: &sess.UserID, Action: "user.logout", Subject: sess.ID.String(),
		})
	})
}
