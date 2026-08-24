//go:build integration

// Package dbtest provides the database handle every integration test uses.
//
// It exists for one reason: the integration suite is not read-only. Queue tests
// call ClaimBatch and Advance, which are deliberately table-wide — a worker must
// be able to claim any due row, so the query cannot be scoped to rows a test
// happens to own. Pointed at a database with real data, those tests lease and
// advance production rows, and a developer who ran `make ingest` first would
// have their corpus silently mutated by running the tests.
//
// The guard below makes that impossible rather than merely unlikely.
package dbtest

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// suffix every test database must carry. `make test-integration` provisions one.
const requiredSuffix = "_test"

// Pool returns a pool against the integration test database, or skips.
//
// Skipping when DATABASE_URL is unset keeps `go test ./...` usable without a
// stack; failing when it points somewhere unsafe is deliberate, because the
// alternative is data loss that looks like a passing run.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL not set")
	}
	if name := databaseName(raw); !strings.HasSuffix(name, requiredSuffix) {
		t.Fatalf("refusing to run destructive tests against database %q: "+
			"this suite claims queue rows table-wide and erases user data, so it "+
			"would mutate real records. Use `make test-integration` or "+
			"`make check-erasure`, which provision a database named with a %q "+
			"suffix.", name, requiredSuffix)
	}

	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// databaseName pulls the database out of a libpq URL. An unparseable URL yields
// "", which fails the suffix check — the safe direction.
func databaseName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}
