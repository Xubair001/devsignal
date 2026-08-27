package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/auth"
)

type nopLogger struct{}

func (nopLogger) Error(string, ...any) {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Info(string, ...any)  {}

// TestRequireAdminRefusesEveryNonAdminRole.
//
// The role travels to the client so the console can hide surfaces the caller
// cannot use, and it would be easy to mistake that for the boundary. It is not.
// This is the boundary, and it has to hold for a user role, an empty role, and a
// missing identity alike — an empty string is what a caller gets if a future
// change forgets to populate it, and failing OPEN there would silently expose
// the whole operations surface.
func TestRequireAdminRefusesEveryNonAdminRole(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	h := &Handler{log: nopLogger{}}
	guarded := h.RequireAdmin(next)

	cases := []struct {
		name     string
		identity *auth.Identity
		wantCode int
		wantNext bool
	}{
		{"no identity at all", nil, http.StatusNotFound, false},
		{"plain user", &auth.Identity{Role: auth.RoleUser}, http.StatusNotFound, false},
		{"empty role", &auth.Identity{Role: ""}, http.StatusNotFound, false},
		{"role that looks close", &auth.Identity{Role: "Admin"}, http.StatusNotFound, false},
		{"admin", &auth.Identity{Role: auth.RoleAdmin}, http.StatusOK, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet, "/internal/admin/sources", nil)
			if c.identity != nil {
				c.identity.UserID = pgtype.UUID{Valid: true}
				req = req.WithContext(auth.WithIdentity(req.Context(), c.identity))
			}
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, req)

			if reached != c.wantNext {
				t.Errorf("handler reached = %v, want %v", reached, c.wantNext)
			}
			if rec.Code != c.wantCode {
				t.Errorf("status %d, want %d", rec.Code, c.wantCode)
			}
		})
	}
}

// TestRefusalIs404Not403.
//
// A 403 confirms the surface exists, which tells an unauthorized caller exactly
// what to go looking for. The refusal has to be indistinguishable from a route
// that is not there.
func TestRefusalIs404Not403(t *testing.T) {
	h := &Handler{log: nopLogger{}}
	guarded := h.RequireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/internal/admin", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(),
		&auth.Identity{Role: auth.RoleUser, UserID: pgtype.UUID{Valid: true}}))
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Error("refused with 403, which confirms the surface exists")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}
