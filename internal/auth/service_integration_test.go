//go:build integration

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
)

// Real Postgres, never a mock: every interesting bug at this layer is a
// constraint, an index or a concurrency interaction that a mock cannot show.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newSvc(t *testing.T, pool *pgxpool.Pool) *Service {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewService(pool, log, DefaultPolicy(), nil)
}

func uniqueEmail(p string) string {
	return fmt.Sprintf("%s-%d@example.test", p, time.Now().UnixNano())
}

func TestRegisterLoginAuthenticate(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	s := newSvc(t, pool)
	email := uniqueEmail("basic")

	tok, ident, err := s.Register(ctx, email, "a-sufficiently-long-password", "test-agent")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if tok.SessionToken == "" || tok.RefreshToken == "" {
		t.Fatal("register returned empty tokens")
	}

	got, err := s.Authenticate(ctx, tok.SessionToken)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got.UserID != ident.UserID {
		t.Fatal("authenticate returned a different user")
	}
	// Tenant must be populated from day one, even though nothing scopes on it yet.
	if !got.TenantID.Valid {
		t.Fatal("tenant_id not set")
	}

	if _, _, err := s.Register(ctx, email, "another-long-password", "x"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate email: got %v, want ErrEmailTaken", err)
	}

	if _, _, err := s.Login(ctx, email, "wrong-password-entirely", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("bad password: got %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := s.Login(ctx, email, "a-sufficiently-long-password", "x"); err != nil {
		t.Fatalf("login: %v", err)
	}

	// Unknown email must be indistinguishable from a wrong password.
	if _, _, err := s.Login(ctx, uniqueEmail("nobody"), "whatever-long-enough", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown email: got %v, want ErrInvalidCredentials", err)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t, testPool(t))
	tok, _, err := s.Register(ctx, uniqueEmail("logout"), "a-sufficiently-long-password", "x")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.Logout(ctx, tok.SessionToken); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := s.Authenticate(ctx, tok.SessionToken); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("session still valid after logout: %v", err)
	}
	// Logout is idempotent.
	if err := s.Logout(ctx, tok.SessionToken); err != nil {
		t.Fatalf("second logout: %v", err)
	}
}

func TestRefreshRotates(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t, testPool(t))
	tok, _, err := s.Register(ctx, uniqueEmail("rotate"), "a-sufficiently-long-password", "x")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	next, _, err := s.Refresh(ctx, tok.RefreshToken, "x")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if next.RefreshToken == tok.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if next.SessionToken == tok.SessionToken {
		t.Fatal("session token was not rotated")
	}
	// The superseded session must be dead.
	if _, err := s.Authenticate(ctx, tok.SessionToken); !errors.Is(err, ErrTokenInvalid) {
		t.Fatal("old session still valid after rotation")
	}
	if _, err := s.Authenticate(ctx, next.SessionToken); err != nil {
		t.Fatalf("new session invalid: %v", err)
	}
}

// The security property that matters: replaying a used refresh token must kill
// the whole family, not just fail the one request.
func TestRefreshReplayRevokesEntireFamily(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	s := newSvc(t, pool)
	q := store.New(pool)

	tok, _, err := s.Register(ctx, uniqueEmail("replay"), "a-sufficiently-long-password", "x")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Rotate a few times: a real client would.
	cur := tok.RefreshToken
	var chain []string
	for i := 0; i < 3; i++ {
		next, _, err := s.Refresh(ctx, cur, "x")
		if err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
		chain = append(chain, next.RefreshToken)
		cur = next.RefreshToken
	}

	live := chain[len(chain)-1]

	// An attacker replays the ORIGINAL, long-since-rotated token.
	if _, _, err := s.Refresh(ctx, tok.RefreshToken, "attacker"); !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("replay: got %v, want ErrTokenReplayed", err)
	}

	// The legitimate client's current token must now also be dead — we cannot
	// tell which party holds it, so failing safe is the only option.
	if _, _, err := s.Refresh(ctx, live, "legit"); err == nil {
		t.Fatal("family was not revoked: the current token still works")
	}

	rt, err := q.GetRefreshByHash(ctx, HashToken(live))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !rt.RevokedAt.Valid {
		t.Fatal("current token not marked revoked")
	}

	// And the replay must be on the record.
	entries, err := q.ListAuditForChainCheck(ctx)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "auth.refresh_replayed" {
			found = true
		}
	}
	if !found {
		t.Fatal("replay was not audited")
	}
}

func TestLockoutAfterRepeatedFailures(t *testing.T) {
	ctx := context.Background()
	s := newSvc(t, testPool(t))
	email := uniqueEmail("lockout")
	if _, _, err := s.Register(ctx, email, "a-sufficiently-long-password", "x"); err != nil {
		t.Fatalf("register: %v", err)
	}

	p := DefaultPolicy()
	for i := int32(0); i < p.LockoutThreshold; i++ {
		if _, _, err := s.Login(ctx, email, "definitely-wrong-password", "x"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: got %v", i, err)
		}
	}
	// Threshold reached: even the CORRECT password must now be refused.
	if _, _, err := s.Login(ctx, email, "a-sufficiently-long-password", "x"); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("after threshold: got %v, want ErrAccountLocked", err)
	}
}

func TestAuditChainIsIntact(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	s := newSvc(t, pool)
	q := store.New(pool)

	if _, _, err := s.Register(ctx, uniqueEmail("chain"), "a-sufficiently-long-password", "x"); err != nil {
		t.Fatalf("register: %v", err)
	}

	entries, err := q.ListAuditForChainCheck(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) < 1 {
		t.Fatal("no audit entries")
	}
	// Each entry must chain onto its predecessor's hash.
	for i := 1; i < len(entries); i++ {
		if string(entries[i].PrevHash) != string(entries[i-1].EntryHash) {
			t.Fatalf("chain broken between id %d and %d", entries[i-1].ID, entries[i].ID)
		}
	}
}
