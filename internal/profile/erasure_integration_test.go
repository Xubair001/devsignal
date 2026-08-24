//go:build integration

package profile

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/auth"
	"github.com/Xubair001/devsignal/internal/dbtest"
	"github.com/Xubair001/devsignal/internal/profileindex"
	"github.com/Xubair001/devsignal/internal/store"
	"github.com/Xubair001/devsignal/pkg/blob"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.Pool(t)
}

func testService(t *testing.T, pool *pgxpool.Pool) *Service {
	t.Helper()
	ep := os.Getenv("S3_ENDPOINT")
	if ep == "" {
		t.Skip("S3_ENDPOINT not set")
	}
	b, err := blob.New(context.Background(), blob.Config{
		Endpoint: ep, Bucket: "devsignal-erasure-" + uuid.NewString()[:8],
		AccessKey: os.Getenv("S3_ACCESS_KEY"), SecretKey: os.Getenv("S3_SECRET_KEY"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	return NewService(pool, b, quiet())
}

// newUser creates a real user through the auth service, so the fixture exercises
// the same rows a live signup produces.
func newUser(t *testing.T, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	a := auth.NewService(pool, quiet(), auth.DefaultPolicy(), nil)
	email := "erasure-" + uuid.NewString()[:12] + "@example.test"
	_, ident, err := a.Register(ctx, email, "a-sufficiently-long-password", "test")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return ident.UserID, ident.TenantID
}

// The full lifecycle: a user with a profile, skills, a resume and its extracted
// text, then erased and VERIFIED empty rather than assumed empty.
func TestErasureRemovesEveryTraceAndVerifiesIt(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := testService(t, pool)
	q := store.New(pool)
	userID, tenantID := newUser(t, pool)

	// Profile.
	head := "Senior Backend Engineer"
	if _, err := svc.Save(ctx, userID, tenantID, Input{
		Headline: &head, TargetRoleFamilies: []string{"backend"},
		TargetCountries: []string{"US"},
	}); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	// A skill, so profile_skill is populated too.
	var skillID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO skill (canonical_slug, display_name, ontology_version)
		 VALUES ($1,'Go','test') ON CONFLICT (canonical_slug) DO UPDATE SET display_name='Go'
		 RETURNING id`, "go-"+uuid.NewString()[:8]).Scan(&skillID); err != nil {
		t.Fatalf("skill: %v", err)
	}
	if err := q.UpsertProfileSkill(ctx, store.UpsertProfileSkillParams{
		UserID: userID, SkillID: skillID, Origin: "manual",
	}); err != nil {
		t.Fatalf("profile skill: %v", err)
	}

	// A resume, which produces TWO objects: the file and its extracted text.
	rec, err := svc.UploadResume(ctx, userID, Upload{
		Filename: "cv.txt", ContentType: TypeText,
		Body: []byte("Jane Doe. Senior Backend Engineer. Go, PostgreSQL, AWS."),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if rec.TextObjectKey == nil {
		t.Fatal("extracted text was not stored")
	}

	prefix := UserPrefix(userID.String())
	if n, err := svc.blob.CountPrefix(ctx, prefix); err != nil || n != 2 {
		t.Fatalf("objects before erasure = %d, %v; want 2 (file + text)", n, err)
	}
	if traces, err := q.CountUserTraces(ctx, userID); err != nil || traces == 0 {
		t.Fatalf("db traces before erasure = %d, %v; want > 0", traces, err)
	}

	// Erase.
	rep, err := svc.Erase(ctx, userID)
	if err != nil {
		t.Fatalf("erase: %v", err)
	}

	if !rep.Complete {
		t.Errorf("erasure not marked complete; steps: %+v", rep.Steps)
	}
	if rep.TracesRemaining != 0 {
		t.Errorf("%d database traces survived erasure", rep.TracesRemaining)
	}
	if rep.ObjectsRemaining != 0 {
		t.Errorf("%d objects survived erasure", rep.ObjectsRemaining)
	}

	// Independent verification: do not trust the report either.
	if n, _ := svc.blob.CountPrefix(ctx, prefix); n != 0 {
		t.Errorf("independent check found %d surviving objects", n)
	}
	if traces, _ := q.CountUserTraces(ctx, userID); traces != 0 {
		t.Errorf("independent check found %d surviving rows", traces)
	}

	// Every declared location must be accounted for. A location that was silently
	// skipped is exactly the gap the inventory exists to prevent.
	seen := map[string]string{}
	for _, s := range rep.Steps {
		seen[s.Location] = s.Status
	}
	for _, loc := range AllLocations {
		status, ok := seen[loc]
		if !ok {
			t.Errorf("location %q was never attempted", loc)
			continue
		}
		if status != "done" && status != "not_applicable" {
			t.Errorf("location %q ended as %q", loc, status)
		}
	}
}

// The extracted text is the densest PII in the system. It must be gone, and it
// must never have been in Postgres to begin with.
func TestExtractedTextIsNotStoredInPostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := testService(t, pool)
	userID, _ := newUser(t, pool)

	const secret = "Jane Doe jane.doe@example.com +1-555-0100"
	rec, err := svc.UploadResume(ctx, userID, Upload{
		Filename: "cv.txt", ContentType: TypeText,
		Body: []byte(secret + " Senior Backend Engineer, Go and PostgreSQL."),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Scan every text column of the resume row for the content.
	var row string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(object_key,'') || ' ' || coalesce(text_object_key,'') || ' ' ||
		        coalesce(filename,'') || ' ' || coalesce(parse_error,'')
		   FROM resume WHERE id=$1`, rec.ID).Scan(&row); err != nil {
		t.Fatalf("read row: %v", err)
	}
	for _, leak := range []string{"Jane Doe", "jane.doe@example.com", "555-0100"} {
		if contains(row, leak) {
			t.Errorf("resume row contains %q; extracted text must live only in object storage", leak)
		}
	}

	// It IS retrievable from object storage.
	got, err := svc.blob.Get(ctx, *rec.TextObjectKey)
	if err != nil {
		t.Fatalf("reading extracted text: %v", err)
	}
	if !contains(string(got), "Jane Doe") {
		t.Error("extracted text does not contain the document content")
	}

	if _, err := svc.Erase(ctx, userID); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if _, err := svc.blob.Get(ctx, *rec.TextObjectKey); err != blob.ErrNotFound {
		t.Errorf("extracted text survived erasure: %v", err)
	}
}

// Erasure must be re-runnable: an operator retrying after a partial failure must
// not get an error.
func TestErasureIsRepeatable(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := testService(t, pool)
	userID, tenantID := newUser(t, pool)

	head := "x"
	if _, err := svc.Save(ctx, userID, tenantID, Input{Headline: &head}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadResume(ctx, userID, Upload{
		Filename: "cv.txt", ContentType: TypeText, Body: []byte("some resume text here"),
	}); err != nil {
		t.Fatal(err)
	}

	first, err := svc.Erase(ctx, userID)
	if err != nil || !first.Complete {
		t.Fatalf("first erase: complete=%v err=%v", first.Complete, err)
	}
	second, err := svc.Erase(ctx, userID)
	if err != nil {
		t.Fatalf("second erase failed: %v", err)
	}
	if !second.Complete {
		t.Error("a repeat erasure should complete cleanly with nothing to do")
	}
	if second.TracesRemaining != 0 || second.ObjectsRemaining != 0 {
		t.Errorf("repeat erasure found leftovers: traces=%d objects=%d",
			second.TracesRemaining, second.ObjectsRemaining)
	}
}

// Erasure must not reach another user's data.
func TestErasureIsScopedToOneUser(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := testService(t, pool)
	q := store.New(pool)

	victim, vTenant := newUser(t, pool)
	bystander, bTenant := newUser(t, pool)

	head := "keep"
	for _, u := range []struct {
		id, tenant pgtype.UUID
	}{{victim, vTenant}, {bystander, bTenant}} {
		if _, err := svc.Save(ctx, u.id, u.tenant, Input{Headline: &head}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.UploadResume(ctx, u.id, Upload{
			Filename: "cv.txt", ContentType: TypeText, Body: []byte("resume for one user"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := svc.Erase(ctx, victim); err != nil {
		t.Fatalf("erase: %v", err)
	}

	if traces, _ := q.CountUserTraces(ctx, bystander); traces == 0 {
		t.Fatal("erasing one user destroyed another user's data")
	}
	if n, _ := svc.blob.CountPrefix(ctx, UserPrefix(bystander.String())); n != 2 {
		t.Errorf("bystander has %d objects, want 2", n)
	}
}

// A resume the user already deleted must still have its objects removed: a
// soft-deleted row is not a deleted file.
func TestErasureRemovesObjectsForAlreadyDeletedResumes(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := testService(t, pool)
	userID, _ := newUser(t, pool)

	rec, err := svc.UploadResume(ctx, userID, Upload{
		Filename: "old.txt", ContentType: TypeText, Body: []byte("an older resume version"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteResume(ctx, userID, rec.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	// Soft delete hides the row; the objects are deliberately still there.
	if n, _ := svc.blob.CountPrefix(ctx, UserPrefix(userID.String())); n == 0 {
		t.Fatal("fixture wrong: objects already gone before erasure")
	}

	if _, err := svc.Erase(ctx, userID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.blob.CountPrefix(ctx, UserPrefix(userID.String())); n != 0 {
		t.Errorf("%d objects survived for a soft-deleted resume", n)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// The profile vector became a real store in step 14. It was previously reported
// as not_applicable, so a failure to wire the delete would look like a passing
// erasure — the exact failure mode the location inventory exists to prevent.
func TestErasureRemovesTheProfileVector(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	svc := testService(t, pool)
	userID, tenantID := newUser(t, pool)

	head := "Senior Backend Engineer, Go and PostgreSQL"
	if _, err := svc.Save(ctx, userID, tenantID, Input{
		Headline: &head, TargetRoleFamilies: []string{"backend"},
	}); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	if err := profileindex.New(pool, profileindex.Local(), quiet()).
		Refresh(ctx, userID); err != nil {
		t.Fatalf("building the vector: %v", err)
	}

	var before int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM profile_embedding WHERE user_id=$1`, userID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("no vector was stored; the test would pass for the wrong reason")
	}

	rep, err := svc.Erase(ctx, userID)
	if err != nil {
		t.Fatalf("erase: %v", err)
	}

	var after int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM profile_embedding WHERE user_id=$1`, userID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Errorf("%d profile vectors survived erasure", after)
	}

	// And the report must claim it, rather than attributing the removal to the
	// user-row cascade or still calling the store unused.
	var step *store.ListErasureStepsRow
	for i := range rep.Steps {
		if rep.Steps[i].Location == LocProfileEmbedding {
			step = &rep.Steps[i]
		}
	}
	if step == nil {
		t.Fatal("no erasure step recorded for the profile vector")
	}
	if step.Status != "done" {
		t.Errorf("profile vector step status = %q, want \"done\" now the store is in use", step.Status)
	}
	if step.Items < 1 {
		t.Errorf("profile vector step reported %d items removed, want at least 1",
			step.Items)
	}
	if !rep.Complete || rep.TracesRemaining != 0 {
		t.Errorf("erasure incomplete: complete=%v traces=%d", rep.Complete, rep.TracesRemaining)
	}
}
