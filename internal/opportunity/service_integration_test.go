//go:build integration

package opportunity

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dbtest"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var refNow = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.Pool(t)
}

type seedOpts struct {
	title       string
	family      string
	seniority   int16
	country     string
	workMode    string
	state       string
	closed      bool
	merged      bool
	repostCount int
	daysOld     int
	applyURL    string
	salaryMin   *int64
}

// seedOpp inserts one posting and returns its id. Every test scopes its queries
// to its own company so it is not affected by whatever else is in the database.
func seedOpp(t *testing.T, pool *pgxpool.Pool, companyID pgtype.UUID, o seedOpts) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	first := refNow.AddDate(0, 0, -o.daysOld)
	state := o.state
	if state == "" {
		state = "ready"
	}
	wm := o.workMode
	if wm == "" {
		wm = "remote"
	}

	// Distinct placeholders per column: reusing one for both location_country
	// (char(2)) and remote_geo_scope (text) makes Postgres unable to deduce a
	// single type for the parameter.
	var id pgtype.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO opportunity (company_id, title_raw, title_normalized, description_text,
		  role_family, seniority_ordinal, work_mode, location_country, remote_geo_scope,
		  language, apply_method, pipeline_state, first_seen_at, last_seen_at,
		  liveness_checked_at, repost_count, salary_min_minor, salary_currency, salary_period,
		  closed_at, close_reason)
		VALUES ($1,$2,$3,'A description long enough to be realistic for these tests.',
		  $4,$5,$6,$7::char(2),$8::text,'en','greenhouse',$9,$10,$10,$10,$11,$12::bigint,
		  CASE WHEN $12::bigint IS NULL THEN NULL ELSE 'USD' END,
		  CASE WHEN $12::bigint IS NULL THEN NULL ELSE 'year' END,
		  CASE WHEN $13::boolean THEN now() ELSE NULL END,
		  CASE WHEN $13::boolean THEN 'absent' ELSE NULL END)
		RETURNING id`,
		companyID, o.title, o.title, nz(o.family), o.seniority, wm, nz(o.country), nz(o.country),
		state, first, o.repostCount, o.salaryMin, o.closed).Scan(&id)
	if err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}

	if o.applyURL != "" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO opportunity_source (opportunity_id, source_id, source_job_id, apply_url)
			VALUES ($1, (SELECT id FROM source LIMIT 1), $2, $3)`,
			id, uuid.NewString(), o.applyURL); err != nil {
			t.Logf("apply url source row skipped: %v", err)
		}
	}
	if o.merged {
		// Merge into a throwaway sibling so merged_into is a valid reference.
		var other pgtype.UUID
		_ = pool.QueryRow(ctx, `
			INSERT INTO opportunity (company_id, title_raw, title_normalized, pipeline_state)
			VALUES ($1,'sibling','sibling','ready') RETURNING id`, companyID).Scan(&other)
		if _, err := pool.Exec(ctx, `UPDATE opportunity SET merged_into=$2 WHERE id=$1`, id, other); err != nil {
			t.Fatalf("mark merged: %v", err)
		}
	}
	return id
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func seedCompany(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Read Co') RETURNING id`,
		"read-"+uuid.NewString()[:8]+".example").Scan(&id); err != nil {
		t.Fatalf("company: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `UPDATE opportunity SET merged_into=NULL WHERE company_id=$1`, id)
		_, _ = pool.Exec(c, `DELETE FROM opportunity_source WHERE opportunity_id IN
		  (SELECT id FROM opportunity WHERE company_id=$1)`, id)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, id)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, id)
	})
	return id
}

// Serving must never show a closed or merged posting. A closed one is a broken
// promise; a merged one is a duplicate of its canonical row.
func TestListExcludesClosedAndMerged(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	co := seedCompany(t, pool)
	svc := NewService(pool, fixedClock{refNow})

	open := seedOpp(t, pool, co, seedOpts{title: "Open Backend Engineer", family: "backend", country: "US"})
	seedOpp(t, pool, co, seedOpts{title: "Closed Backend Engineer", family: "backend", country: "US", closed: true})
	seedOpp(t, pool, co, seedOpts{title: "Merged Backend Engineer", family: "backend", country: "US", merged: true})
	// Not through the pipeline yet: must not be served either.
	seedOpp(t, pool, co, seedOpts{title: "Unfinished Backend Engineer", family: "backend", country: "US", state: "normalized"})

	page, err := svc.List(ctx, ListFilter{RoleFamily: strp("backend"), Country: strp("US"), PageSize: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var titles []string
	for _, it := range page.Items {
		titles = append(titles, it.Title)
	}
	found := 0
	for _, it := range page.Items {
		if it.ID == open.String() {
			found++
		}
		switch it.Title {
		case "Closed Backend Engineer":
			t.Error("a closed posting was served")
		case "Merged Backend Engineer":
			t.Error("a merged posting was served")
		case "Unfinished Backend Engineer":
			t.Error("a posting that has not finished the pipeline was served")
		}
	}
	if found != 1 {
		t.Fatalf("open posting appeared %d times in %v", found, titles)
	}
}

// Undisclosed salary is its own state. Defaulting it to a placeholder is the
// invented field blueprint §3 forbids.
func TestUndisclosedSalaryIsNullNotZero(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	co := seedCompany(t, pool)
	svc := NewService(pool, fixedClock{refNow})

	seedOpp(t, pool, co, seedOpts{title: "No Salary Role", family: "backend", country: "DE"})
	amount := int64(15000000)
	seedOpp(t, pool, co, seedOpts{title: "With Salary Role", family: "backend", country: "DE", salaryMin: &amount})

	page, err := svc.List(ctx, ListFilter{Country: strp("DE"), PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	var sawNull, sawValue bool
	for _, it := range page.Items {
		switch it.Title {
		case "No Salary Role":
			if it.Salary != nil {
				t.Errorf("undisclosed salary rendered as %+v", *it.Salary)
			}
			sawNull = true
		case "With Salary Role":
			if it.Salary == nil {
				t.Error("disclosed salary was dropped")
			} else {
				if it.Salary.MinMinor != amount {
					t.Errorf("min_minor = %d, want %d", it.Salary.MinMinor, amount)
				}
				if it.Salary.Currency != "USD" || it.Salary.Period != "year" {
					t.Errorf("currency/period lost: %+v", *it.Salary)
				}
			}
			sawValue = true
		}
	}
	if !sawNull || !sawValue {
		t.Fatalf("fixtures missing (null=%v value=%v)", sawNull, sawValue)
	}
}

// Keyset pagination must not skip or duplicate rows, which is exactly what
// OFFSET does when ingestion keeps inserting underneath.
func TestKeysetPaginationCoversEveryRowExactlyOnce(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	co := seedCompany(t, pool)
	svc := NewService(pool, fixedClock{refNow})

	const total = 11
	want := map[string]bool{}
	for i := 0; i < total; i++ {
		id := seedOpp(t, pool, co, seedOpts{
			title: "Paged Engineer", family: "platform", country: "NL", daysOld: i,
		})
		want[id.String()] = true
	}

	seen := map[string]int{}
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		page, err := svc.List(ctx, ListFilter{
			RoleFamily: strp("platform"), Country: strp("NL"), PageSize: 4, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, it := range page.Items {
			if want[it.ID] {
				seen[it.ID]++
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != total {
		t.Fatalf("saw %d of %d rows across pages", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s returned %d times", id, n)
		}
	}
}

func TestMalformedCursorIsRejected(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testPool(t), fixedClock{refNow})
	for _, bad := range []string{"not-base64!!", "Zm9v", "MjAyNi0wOC0yMXxub3QtYS11dWlk"} {
		if _, err := svc.List(ctx, ListFilter{Cursor: bad}); err == nil {
			t.Errorf("cursor %q was accepted", bad)
		}
	}
}

// Ghost risk must reflect observed refreshes, and the reasons must be present so
// the user can interrogate the judgement rather than trust a bare band.
func TestGhostRiskSurfacesObservedRefreshes(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	co := seedCompany(t, pool)
	svc := NewService(pool, fixedClock{refNow})

	clean := seedOpp(t, pool, co, seedOpts{title: "Clean Role", family: "data", country: "IE", daysOld: 2})
	suspect := seedOpp(t, pool, co, seedOpts{
		title: "Perpetual Role", family: "data", country: "IE", daysOld: 200, repostCount: 5,
	})

	for _, tc := range []struct {
		id       pgtype.UUID
		wantHigh bool
	}{{clean, false}, {suspect, true}} {
		d, err := svc.Get(ctx, tc.id.String())
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		isHigh := d.Signals.GhostRisk == "high"
		if isHigh != tc.wantHigh {
			t.Errorf("%s: ghost_risk = %s (reasons %v), wantHigh=%v",
				d.Title, d.Signals.GhostRisk, d.Signals.GhostRiskReasons, tc.wantHigh)
		}
		if tc.wantHigh && len(d.Signals.GhostRiskReasons) == 0 {
			t.Error("a flagged posting must say why")
		}
	}
}

// No internal field may reach a client. The DTO is the enforcement point.
func TestResponseLeaksNoInternalFields(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	co := seedCompany(t, pool)
	svc := NewService(pool, fixedClock{refNow})
	id := seedOpp(t, pool, co, seedOpts{title: "Leak Check Engineer", family: "backend", country: "US"})

	d, err := svc.Get(ctx, id.String())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"simhash", "block_key", "content_hash", "merged_into", "pipeline_state",
		"normalization_version", "ghost_risk_score", "seniority_ordinal",
		"version", "lease_until", "next_attempt_at", "swept_at", "tenant_id",
	} {
		if contains(string(raw), forbidden) {
			t.Errorf("internal field %q leaked into the response", forbidden)
		}
	}
}

func TestGetUnknownIDIsNotFound(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testPool(t), fixedClock{refNow})
	if _, err := svc.Get(ctx, uuid.NewString()); err == nil {
		t.Fatal("unknown id returned a result")
	}
	if _, err := svc.Get(ctx, "not-a-uuid"); err == nil {
		t.Fatal("malformed id accepted")
	}
}

// Page size must be bounded: a huge page is either a mistake or enumeration.
func TestPageSizeIsClamped(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	co := seedCompany(t, pool)
	svc := NewService(pool, fixedClock{refNow})
	for i := 0; i < 3; i++ {
		seedOpp(t, pool, co, seedOpts{title: "Clamp Role", family: "qa", country: "PL", daysOld: i})
	}
	page, err := svc.List(ctx, ListFilter{RoleFamily: strp("qa"), Country: strp("PL"), PageSize: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) > MaxPageSize {
		t.Errorf("returned %d items, above the %d cap", len(page.Items), MaxPageSize)
	}
}

func strp(s string) *string { return &s }

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
