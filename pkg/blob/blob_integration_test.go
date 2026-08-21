//go:build integration

package blob

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	ep := os.Getenv("S3_ENDPOINT")
	if ep == "" {
		t.Skip("S3_ENDPOINT not set")
	}
	s, err := New(context.Background(), Config{
		Endpoint:  ep,
		Bucket:    "devsignal-test-" + uuid.NewString()[:8],
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return s
}

func TestPutGetDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	key := "users/" + uuid.NewString() + "/resume.pdf"
	body := []byte("%PDF-1.4 pretend resume bytes")

	if err := s.Put(ctx, key, body, "application/pdf"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("round trip corrupted the object")
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, key); err != ErrNotFound {
		t.Fatalf("after delete, get returned %v, want ErrNotFound", err)
	}
}

// Erasure must be re-runnable, so a second delete of an absent key is success.
func TestDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	key := "users/" + uuid.NewString() + "/gone.txt"
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("deleting an absent key failed: %v", err)
	}
}

// The erasure primitive: one call clears every object under a user prefix, and
// verification is that the count returns to zero.
func TestDeletePrefixClearsEverythingForAUser(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	user := uuid.NewString()
	prefix := "users/" + user + "/"

	for _, k := range []string{"resume.pdf", "resume.txt", "nested/export.json"} {
		if err := s.Put(ctx, prefix+k, []byte("data"), "application/octet-stream"); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	// Another user's object must survive.
	other := "users/" + uuid.NewString() + "/resume.pdf"
	if err := s.Put(ctx, other, []byte("keep me"), "application/pdf"); err != nil {
		t.Fatal(err)
	}

	if n, err := s.CountPrefix(ctx, prefix); err != nil || n != 3 {
		t.Fatalf("CountPrefix = %d, %v; want 3", n, err)
	}

	removed, err := s.DeletePrefix(ctx, prefix)
	if err != nil {
		t.Fatalf("delete prefix: %v", err)
	}
	if removed != 3 {
		t.Errorf("removed %d objects, want 3", removed)
	}
	if n, _ := s.CountPrefix(ctx, prefix); n != 0 {
		t.Errorf("%d objects survived erasure", n)
	}
	if _, err := s.Get(ctx, other); err != nil {
		t.Errorf("another user's object was deleted: %v", err)
	}
}

// An empty prefix would wipe the bucket. A blank user id must not be able to do
// that by accident.
func TestDeletePrefixRefusesEmptyPrefix(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if err := s.Put(ctx, "users/keep/a.txt", []byte("x"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   "} {
		if _, err := s.DeletePrefix(ctx, bad); err == nil {
			t.Errorf("DeletePrefix(%q) was allowed; it would empty the bucket", bad)
		}
	}
	if n, _ := s.CountPrefix(ctx, "users/keep/"); n != 1 {
		t.Error("object was deleted by a refused call")
	}
}

func TestGetMissingKeyIsDistinguishable(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	_, err := s.Get(ctx, "users/nobody/"+uuid.NewString())
	if err != ErrNotFound {
		t.Fatalf("got %v, want ErrNotFound: erasure verification depends on telling "+
			"'gone' from 'broken'", err)
	}
	if !strings.Contains(ErrNotFound.Error(), "not found") {
		t.Error("ErrNotFound message is unhelpful")
	}
}
