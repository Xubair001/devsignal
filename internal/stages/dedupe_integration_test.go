//go:build integration

package stages

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dedupe"
	"github.com/Xubair001/devsignal/internal/store"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// Realistic lengths: real job descriptions run to thousands of characters, and
// dedupe.MinTextForFuzzy deliberately refuses to fuzzy-match stubs.
const (
	sharedIntro = "About the company. We are a global technology organisation with teams " +
		"distributed across more than sixty countries. We believe in transparency, " +
		"iteration and results. Benefits include health cover, parental leave, a home " +
		"office budget and a generous learning stipend. We are an equal opportunity " +
		"employer and welcome applicants from every background. "

	backendRoleBody = sharedIntro +
		"What you will do: design, build and operate backend services written in Go, " +
		"backed by PostgreSQL, serving millions of requests per day. You will own " +
		"service reliability end to end, including on-call, capacity planning and " +
		"incident review. You will profile and optimise hot paths, design schemas and " +
		"migrations, and mentor other backend engineers through design review."

	differentRoleBody = sharedIntro +
		"What you will do: own our demand generation programme across paid search, " +
		"paid social and lifecycle email. You will build the campaign calendar, manage " +
		"agency relationships and the media budget, and report pipeline contribution " +
		"to the executive team. You will partner with sales on account-based plays and " +
		"with content on messaging, and you will run quarterly brand studies."
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// seedPair inserts two postings that are the same real-world job, already
// normalized into the same block — the exact state the per-item pass leaves
// behind when both rows race through before either block_key is visible.
func seedPair(t *testing.T, pool *pgxpool.Pool, title, body string, identical bool) (string, []pgtype.UUID) {
	t.Helper()
	ctx := context.Background()

	var companyID pgtype.UUID
	domain := "sweep-" + uuid.NewString()[:8] + ".example"
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Sweep Co') RETURNING id`,
		domain).Scan(&companyID); err != nil {
		t.Fatalf("company: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity_merge WHERE into_opportunity_id IN
		  (SELECT id FROM opportunity WHERE company_id=$1)`, companyID)
		_, _ = pool.Exec(c, `UPDATE opportunity SET merged_into=NULL WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})

	block := dedupe.BlockKey(companyID.String(), title, "US")
	sig := int64(dedupe.SimHash(title + " " + body))

	var ids []pgtype.UUID
	for i := 0; i < 2; i++ {
		// Only the SECOND row differs. Applying this to both rows would give them
		// identical text, which dedupe correctly merges — an earlier version of
		// this seed did exactly that and the "failure" was the test, not the code.
		bodyI := body
		if !identical && i == 1 {
			// A genuinely different role at realistic description length: short
			// stubs are excluded from fuzzy matching by MinTextForFuzzy, so the
			// test must use text long enough to exercise the real path.
			bodyI = differentRoleBody
		}
		s := int64(dedupe.SimHash(title + " " + bodyI))
		if identical {
			s = sig
		}
		var id pgtype.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO opportunity (company_id, title_raw, title_normalized, description_text,
			   location_country, pipeline_state, block_key, simhash, content_hash)
			 VALUES ($1,$2,$3,$4,'US','normalized',$5,$6,$7) RETURNING id`,
			companyID, title, title, bodyI, block, s,
			[]byte(uuid.NewString()), // distinct content hashes: force the SimHash path
		).Scan(&id); err != nil {
			t.Fatalf("opportunity %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return block, ids
}

// The regression this sweep exists for: two identical postings that the
// per-item pass never compared must be merged by the sweep.
func TestSweepMergesWhatPerItemMissed(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	d := NewDeduper(pool, quiet())

	block, ids := seedPair(t, pool, "Forward Deployed Engineer - EMEA", backendRoleBody, true)

	q := store.New(pool)
	before, err := q.IsUnmerged(ctx, ids[1])
	if err != nil || !before {
		t.Fatalf("second row should start unmerged (err=%v)", err)
	}

	// sweepOne, not SweepBlocks: the test must be isolated from whatever else is
	// in the database. SweepBlocks spans every block by design.
	n, err := d.sweepOne(ctx, block)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n < 1 {
		t.Fatal("sweep merged nothing; the duplicate survives")
	}

	// The LATER row must be the one merged away: first_seen_at is ours, so the
	// earlier sighting is canonical and its age must be preserved.
	keptFirst, err := q.IsUnmerged(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	goneSecond, err := q.IsUnmerged(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if !keptFirst {
		t.Error("the older row was merged away; age signal lost")
	}
	if goneSecond {
		t.Error("the newer duplicate was not merged")
	}

	// The decision must be recorded, or un-merge is guesswork.
	var reason string
	var moved int
	if err := pool.QueryRow(ctx,
		`SELECT reason, source_rows_moved FROM opportunity_merge
		  WHERE from_opportunity_id=$1 AND undone_at IS NULL`, ids[1]).Scan(&reason, &moved); err != nil {
		t.Fatalf("merge not recorded: %v", err)
	}
	if reason == "" {
		t.Error("merge recorded without a reason")
	}
}

// The sweep must not merge genuinely different roles that happen to share a
// block. A false merge hides a real job permanently.
func TestSweepDoesNotMergeDifferentRoles(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	d := NewDeduper(pool, quiet())

	block, ids := seedPair(t, pool, "Engineer", backendRoleBody, false)

	if _, err := d.sweepOne(ctx, block); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	q := store.New(pool)
	for i, id := range ids {
		ok, err := q.IsUnmerged(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("row %d was merged away: different roles must never merge", i)
		}
	}
}

// Running the sweep repeatedly must be safe: no double-recording, no cycles.
func TestSweepIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	d := NewDeduper(pool, quiet())

	block, ids := seedPair(t, pool, "Staff Platform Engineer", backendRoleBody, true)

	first, err := d.sweepOne(ctx, block)
	if err != nil {
		t.Fatal(err)
	}
	if first < 1 {
		t.Fatal("first sweep merged nothing")
	}
	second, err := d.sweepOne(ctx, block)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second != 0 {
		t.Errorf("second sweep merged %d more; the sweep is not idempotent", second)
	}

	var records int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity_merge WHERE from_opportunity_id=$1`, ids[1]).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 1 {
		t.Errorf("merge recorded %d times, want 1", records)
	}

	// No cycle: a merged row must not be pointed at by its own target.
	var cycles int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity a JOIN opportunity b ON a.merged_into = b.id
		  WHERE b.merged_into = a.id`).Scan(&cycles); err != nil {
		t.Fatal(err)
	}
	if cycles != 0 {
		t.Errorf("%d merge cycles present", cycles)
	}
}
