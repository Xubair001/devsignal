//go:build integration

package retrieve

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/profileindex"
	"github.com/Xubair001/devsignal/internal/store"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

const (
	backendText = `Senior Backend Engineer. Go, PostgreSQL, distributed systems, gRPC.
You will design and operate high-throughput services, own schema migrations and
work on query performance, caching and observability across the platform.`

	frontendText = `Senior Frontend Engineer. React, TypeScript, CSS, accessibility.
You will build component libraries, own the design system and improve rendering
performance and bundle size across our web application.`

	marketingText = `Field Marketing Manager. Campaign planning, brand partnerships,
event logistics and budget ownership for regional trade shows and conferences.`
)

// posting describes one fixture, including the columns the hard predicates read.
type posting struct {
	title      string
	text       string
	country    string
	workMode   string
	employment string
	language   string
}

// seed inserts a company plus postings, embeds them, and marks them ready — the
// state retrieval requires. Cleanup removes everything it created.
func seed(t *testing.T, pool *pgxpool.Pool, ps map[string]posting) map[string]pgtype.UUID {
	t.Helper()
	ctx := context.Background()

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (canonical_domain, display_name) VALUES ($1,'Retrieve Co') RETURNING id`,
		"retr-"+uuid.NewString()[:8]+".example").Scan(&companyID); err != nil {
		t.Fatalf("company: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM opportunity_embedding WHERE opportunity_id IN
			(SELECT id FROM opportunity WHERE company_id=$1)`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM opportunity WHERE company_id=$1`, companyID)
		_, _ = pool.Exec(c, `DELETE FROM company WHERE id=$1`, companyID)
	})

	q := store.New(pool)
	e := embed.NewLocal()
	ids := map[string]pgtype.UUID{}

	for name, p := range ps {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO opportunity (
			  company_id, title_raw, title_normalized, description_text,
			  location_country, work_mode, employment_type, language,
			  pipeline_state, last_seen_at)
			VALUES ($1,$2,$2,$3,$4,$5,$6,$7,'ready',now()) RETURNING id`,
			companyID, p.title, p.text,
			nullable(p.country), nullable(p.workMode),
			nullable(p.employment), nullable(p.language),
		).Scan(&id); err != nil {
			t.Fatalf("opportunity %s: %v", name, err)
		}
		vec, err := e.Embed(ctx, p.text)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.PutOpportunityEmbedding(ctx, store.PutOpportunityEmbeddingParams{
			OpportunityID: id, EmbeddingModel: e.ModelID(), EmbeddingVersion: e.Version(),
			EmbeddingDim: int32(len(vec)), Embedding: pgvector.NewVector(vec),
		}); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	return ids
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// queryVector embeds arbitrary text as a stand-in for a profile vector.
func queryVector(t *testing.T, text string) pgvector.Vector {
	t.Helper()
	v, err := embed.NewLocal().Embed(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	return pgvector.NewVector(v)
}

func found(res *Result, id pgtype.UUID) bool {
	for _, c := range res.Candidates {
		if c.ID == id {
			return true
		}
	}
	return false
}

// The core promise: a filter nobody set must not exclude anything. This is the
// nil-versus-empty array hazard reaching the database rather than a unit test.
func TestUnconstrainedCriteriaReturnEveryEligiblePosting(t *testing.T) {
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"backend":  {title: "Senior Backend Engineer", text: backendText, country: "DE", workMode: "remote", employment: "full_time", language: "en"},
		"frontend": {title: "Senior Frontend Engineer", text: frontendText, country: "NL", workMode: "onsite", employment: "full_time", language: "en"},
	})

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion, Criteria{})
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range ids {
		if !found(res, id) {
			t.Errorf("%s was excluded by a filter nobody set", name)
		}
	}
}

func TestCountryFilterExcludesOtherCountries(t *testing.T) {
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		// Onsite, so the remote exemption cannot be what admits it.
		"german": {title: "Backend Engineer Berlin", text: backendText, country: "DE", workMode: "onsite"},
		"dutch":  {title: "Backend Engineer Amsterdam", text: backendText, country: "NL", workMode: "onsite"},
	})

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{Countries: []string{"DE"}})
	if err != nil {
		t.Fatal(err)
	}
	if !found(res, ids["german"]) {
		t.Error("a posting in the requested country was excluded")
	}
	if found(res, ids["dutch"]) {
		t.Error("a posting outside the requested country was returned")
	}
}

// A remote posting's country is a formality. Excluding it from a country-filtered
// search would hide exactly the roles a remote-seeking user wants most.
func TestRemotePostingsSurviveACountryFilter(t *testing.T) {
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"remote_elsewhere": {title: "Remote Backend Engineer", text: backendText, country: "US", workMode: "remote"},
		"onsite_elsewhere": {title: "Onsite Backend Engineer", text: backendText, country: "US", workMode: "onsite"},
	})

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{Countries: []string{"DE"}})
	if err != nil {
		t.Fatal(err)
	}
	if !found(res, ids["remote_elsewhere"]) {
		t.Error("a remote posting was excluded by a country filter")
	}
	if found(res, ids["onsite_elsewhere"]) {
		t.Error("an onsite posting in the wrong country was returned")
	}
}

// Asking for remote must not surface onsite work. The reverse direction — hybrid
// accepting remote — is deliberate and covered below.
func TestWorkModeFilterIsRespected(t *testing.T) {
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"remote": {title: "Remote Backend Engineer", text: backendText, workMode: "remote"},
		"onsite": {title: "Onsite Backend Engineer", text: backendText, workMode: "onsite"},
		"hybrid": {title: "Hybrid Backend Engineer", text: backendText, workMode: "hybrid"},
	})
	remote := "remote"

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{WorkMode: &remote})
	if err != nil {
		t.Fatal(err)
	}
	if !found(res, ids["remote"]) {
		t.Error("a remote posting was excluded from a remote search")
	}
	if found(res, ids["onsite"]) {
		t.Error("an onsite posting was returned for a remote search")
	}
	if found(res, ids["hybrid"]) {
		t.Error("asking for remote returned hybrid; only the reverse is intended")
	}
}

func TestHybridAcceptsRemote(t *testing.T) {
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"remote": {title: "Remote Backend Engineer", text: backendText, workMode: "remote"},
		"onsite": {title: "Onsite Backend Engineer", text: backendText, workMode: "onsite"},
	})
	hybrid := "hybrid"

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{WorkMode: &hybrid})
	if err != nil {
		t.Fatal(err)
	}
	if !found(res, ids["remote"]) {
		t.Error("hybrid must accept remote: it is a superset of remote days")
	}
	if found(res, ids["onsite"]) {
		t.Error("hybrid must not accept onsite")
	}
}

// A posting that never stated its employment type or language must not be
// dropped by a filter on that field. Most boards omit both, so treating unknown
// as a mismatch would discard most of the corpus.
func TestUnknownFieldsAreNotTreatedAsMismatches(t *testing.T) {
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"unstated":  {title: "Backend Engineer", text: backendText},
		"contract":  {title: "Contract Backend Engineer", text: backendText, employment: "contract"},
		"full_time": {title: "Full Time Backend Engineer", text: backendText, employment: "full_time"},
	})

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{EmploymentTypes: []string{"full_time"}, Languages: []string{"en"}})
	if err != nil {
		t.Fatal(err)
	}
	if !found(res, ids["unstated"]) {
		t.Error("a posting with no stated employment type was dropped by an employment filter")
	}
	if !found(res, ids["full_time"]) {
		t.Error("a matching posting was excluded")
	}
	if found(res, ids["contract"]) {
		t.Error("a posting that stated a different employment type was returned")
	}
}

// Closed, merged and not-yet-ready postings must never reach the scorer.
func TestIneligiblePostingsNeverRetrieved(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"open":     {title: "Open Backend Engineer", text: backendText},
		"closed":   {title: "Closed Backend Engineer", text: backendText},
		"draft":    {title: "Draft Backend Engineer", text: backendText},
		"merged":   {title: "Merged Backend Engineer", text: backendText},
		"stale":    {title: "Stale Backend Engineer", text: backendText},
		"survivor": {title: "Survivor Backend Engineer", text: backendText},
	})
	mustExec(t, pool, `UPDATE opportunity SET closed_at=now(), close_reason='absent' WHERE id=$1`, ids["closed"])
	mustExec(t, pool, `UPDATE opportunity SET pipeline_state='enriched' WHERE id=$1`, ids["draft"])
	mustExec(t, pool, `UPDATE opportunity SET merged_into=$2 WHERE id=$1`, ids["merged"], ids["survivor"])
	mustExec(t, pool, `UPDATE opportunity SET last_seen_at=now() - interval '90 days' WHERE id=$1`, ids["stale"])

	res, err := New(pool).Retrieve(ctx, queryVector(t, backendText), embed.LocalVersion, Criteria{})
	if err != nil {
		t.Fatal(err)
	}
	if !found(res, ids["open"]) {
		t.Fatal("the eligible posting was not retrieved; the test proves nothing")
	}
	for _, name := range []string{"closed", "draft", "merged", "stale"} {
		if found(res, ids[name]) {
			t.Errorf("%s posting reached retrieval", name)
		}
	}
}

// The keyword channel exists because it fails differently from the vector one.
// This is the case that motivates it: an exact role-term match the lexical
// embedder ranks poorly.
func TestKeywordChannelFindsWhatItsTermsName(t *testing.T) {
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"marketing": {title: "Field Marketing Manager", text: marketingText},
		"backend":   {title: "Senior Backend Engineer", text: backendText},
	})

	// Query vector is backend-flavoured, but the terms ask for marketing.
	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{Terms: "marketing"})
	if err != nil {
		t.Fatal(err)
	}
	if !found(res, ids["marketing"]) {
		t.Error("the keyword channel did not surface a posting naming its term")
	}

	var marketing *Candidate
	for i := range res.Candidates {
		if res.Candidates[i].ID == ids["marketing"] {
			marketing = &res.Candidates[i]
		}
	}
	if marketing == nil {
		t.Fatal("candidate missing")
	}
	if !marketing.FoundBy(ChannelKeyword) {
		t.Errorf("provenance wrong: channels = %v", marketing.Channels)
	}
}

// Empty terms must skip the channel rather than run a query matching nothing and
// reporting a zero return that looks like a fault.
func TestEmptyTermsSkipTheKeywordChannel(t *testing.T) {
	pool := dbtest.Pool(t)
	seed(t, pool, map[string]posting{"backend": {title: "Backend", text: backendText}})

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion, Criteria{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Coverage {
		if c.Channel == ChannelKeyword {
			t.Error("the keyword channel ran with no terms")
		}
	}
}

// Coverage has to be reported per channel, because silent under-return is this
// stage's characteristic failure and it is invisible without a denominator.
func TestCoverageIsReportedPerChannel(t *testing.T) {
	pool := dbtest.Pool(t)
	seed(t, pool, map[string]posting{
		"backend":  {title: "Senior Backend Engineer", text: backendText},
		"frontend": {title: "Senior Frontend Engineer", text: frontendText},
	})

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{Terms: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range res.Coverage {
		seen[c.Channel] = true
		if c.Requested <= 0 {
			t.Errorf("channel %s reported no request size", c.Channel)
		}
	}
	if !seen[ChannelVector] || !seen[ChannelKeyword] {
		t.Errorf("both channels must report coverage, saw %v", seen)
	}
	if res.Eligible < 2 {
		t.Errorf("eligible count = %d, want at least the 2 seeded postings", res.Eligible)
	}
}

// The cap is the whole point of the stage: it is what bounds the work downstream.
func TestTheCapIsEnforcedAndReported(t *testing.T) {
	pool := dbtest.Pool(t)
	ps := map[string]posting{}
	for i := range 12 {
		ps[string(rune('a'+i))] = posting{
			title: "Backend Engineer " + string(rune('a'+i)), text: backendText,
		}
	}
	seed(t, pool, ps)

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), embed.LocalVersion,
		Criteria{MaxCandidates: 5, Terms: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 5 {
		t.Errorf("got %d candidates, want exactly the cap of 5", len(res.Candidates))
	}
	if !res.Truncated {
		t.Error("a truncated set must say so; downstream cannot treat it as exhaustive")
	}
}

// Retrieval must not read vectors from a different embedding version: distances
// across models are meaningless, so a version mismatch has to return nothing
// rather than nonsense.
func TestRetrievalIsScopedToOneEmbeddingVersion(t *testing.T) {
	pool := dbtest.Pool(t)
	seed(t, pool, map[string]posting{"backend": {title: "Backend Engineer", text: backendText}})

	res, err := New(pool).Retrieve(context.Background(),
		queryVector(t, backendText), "some-other-version", Criteria{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Candidates {
		if c.FoundBy(ChannelVector) {
			t.Error("the vector channel returned a candidate from a different embedding version")
		}
	}
}

// End to end from a stored profile: the path the feed will actually call.
func TestRetrieveForProfileUsesTheStoredVectorAndPreferences(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	ids := seed(t, pool, map[string]posting{
		"backend":  {title: "Senior Backend Engineer", text: backendText, country: "DE", workMode: "remote"},
		"frontend": {title: "Senior Frontend Engineer", text: frontendText, country: "DE", workMode: "remote"},
		"elsewhere": {title: "Onsite Backend Engineer US", text: backendText,
			country: "US", workMode: "onsite"},
	})
	userID := seedProfileWithVector(t, pool)

	res, version, err := New(pool).RetrieveForProfile(ctx, userID, embed.LocalVersion, 50)
	if err != nil {
		t.Fatal(err)
	}
	if version < 1 {
		t.Errorf("profile version = %d; callers need it to know which revision produced the set", version)
	}
	if !found(res, ids["backend"]) {
		t.Error("the profile's own target role was not retrieved")
	}
	if found(res, ids["elsewhere"]) {
		t.Error("an onsite posting outside the profile's target countries was returned")
	}
	// Retrieval is recall, not ranking: the frontend posting is eligible and
	// should be present for the scorer to rank down.
	if !found(res, ids["frontend"]) {
		t.Error("retrieval dropped an eligible posting; that is the scorer's job, not this stage's")
	}
}

// A profile with no vector must fail loudly rather than quietly degrading to
// keyword-only recall without telling the caller.
func TestMissingProfileVectorIsReportedNotHidden(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	userID := seedProfileWithVector(t, pool)
	mustExec(t, pool, `DELETE FROM profile_embedding WHERE user_id=$1`, userID)

	_, _, err := New(pool).RetrieveForProfile(ctx, userID, embed.LocalVersion, 50)
	if err == nil {
		t.Fatal("retrieval with no profile vector returned no error")
	}
	if !errors.Is(err, ErrNoVector) {
		t.Errorf("err = %v, want ErrNoVector so the caller can choose how to degrade", err)
	}
}

// A profile edit bumps profile_version; the stored vector records the version it
// was built from, which is what makes staleness detectable rather than silent.
func TestStaleProfileVectorIsDetectable(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	userID := seedProfileWithVector(t, pool)

	before, err := store.New(pool).GetProfileEmbedding(ctx, store.GetProfileEmbeddingParams{
		UserID: userID, EmbeddingVersion: embed.LocalVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.EmbeddedProfileVersion != before.CurrentProfileVersion {
		t.Fatalf("fresh vector already stale: %d vs %d",
			before.EmbeddedProfileVersion, before.CurrentProfileVersion)
	}

	mustExec(t, pool, `UPDATE profile SET headline='changed' WHERE user_id=$1`, userID)

	after, err := store.New(pool).GetProfileEmbedding(ctx, store.GetProfileEmbeddingParams{
		UserID: userID, EmbeddingVersion: embed.LocalVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.EmbeddedProfileVersion >= after.CurrentProfileVersion {
		t.Errorf("a profile edit did not make the vector detectably stale: %d vs %d",
			after.EmbeddedProfileVersion, after.CurrentProfileVersion)
	}

	// Refreshing brings them back in step.
	if err := profileindex.New(pool, profileindex.Local(), quietLog()).Refresh(ctx, userID); err != nil {
		t.Fatal(err)
	}
	fixed, err := store.New(pool).GetProfileEmbedding(ctx, store.GetProfileEmbeddingParams{
		UserID: userID, EmbeddingVersion: embed.LocalVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixed.EmbeddedProfileVersion != fixed.CurrentProfileVersion {
		t.Errorf("refresh left the vector stale: %d vs %d",
			fixed.EmbeddedProfileVersion, fixed.CurrentProfileVersion)
	}
}

// An empty profile must not get a zero vector: a zero vector is equidistant from
// everything, which would return the whole corpus dressed up as a match.
func TestEmptyProfileGetsNoVector(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	userID, tenantID := seedUser(t, pool)
	mustExec(t, pool,
		`INSERT INTO profile (user_id, tenant_id) VALUES ($1,$2)`, userID, tenantID)

	err := profileindex.New(pool, profileindex.Local(), quietLog()).Refresh(ctx, userID)
	if !errors.Is(err, profileindex.ErrEmptyProfile) {
		t.Errorf("err = %v, want ErrEmptyProfile rather than a stored zero vector", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM profile_embedding WHERE user_id=$1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d vectors stored for an empty profile, want 0", n)
	}
}

// ------------------------------------------------------------------ helpers

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func seedUser(t *testing.T, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	var tenantID, userID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (display_name) VALUES ('Retrieve Tenant') RETURNING id`,
	).Scan(&tenantID); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO app_user (tenant_id, email, password_hash)
		 VALUES ($1,$2,'x') RETURNING id`,
		tenantID, "retr-"+uuid.NewString()[:8]+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM app_user WHERE id=$1`, userID)
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id=$1`, tenantID)
	})
	return userID, tenantID
}

func seedProfileWithVector(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	userID, tenantID := seedUser(t, pool)

	mustExec(t, pool, `
		INSERT INTO profile (user_id, tenant_id, headline, seniority_ordinal,
		  target_role_families, target_countries, work_mode_preference, languages,
		  target_employment_types)
		VALUES ($1,$2,'Senior backend engineer, Go and PostgreSQL',3,
		  ARRAY['backend'], ARRAY['DE']::char(2)[], 'remote',
		  ARRAY['en']::char(2)[], ARRAY['full_time'])`, userID, tenantID)

	if err := profileindex.New(pool, profileindex.Local(), quietLog()).Refresh(ctx, userID); err != nil {
		t.Fatalf("refresh profile vector: %v", err)
	}
	return userID
}
