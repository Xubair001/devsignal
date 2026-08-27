package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type ctxKey int

const identityKey ctxKey = iota

// One message for every token failure. Expired, revoked, unknown and REPLAYED
// all look identical to a caller — distinguishing them is free reconnaissance.
const msgTokenInvalid = "invalid or expired token"

// FromContext returns the authenticated identity. Handlers must use this rather
// than trusting anything in the request body.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey).(*Identity)
	return id, ok
}

// WithIdentity attaches an identity to a context.
//
// Exported for tests only: production code gets here through Authenticator,
// which is the single place a token becomes an identity. A handler that builds
// its own identity would be trusting its own input.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	r.Post("/refresh", h.refresh)
	r.Post("/logout", h.logout)
	return r
}

// ---------------------------------------------------------------- DTOs
// Explicit request and response types. A store struct must never be returned
// directly: it leaks columns into the API and exposes internal fields.

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type tokensResponse struct {
	SessionToken string `json:"session_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"` // RFC 3339 UTC
}

func newTokensResponse(t *Tokens) tokensResponse {
	return tokensResponse{
		SessionToken: t.SessionToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    t.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ---------------------------------------------------------------- handlers

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	req, err := decode[credentialsRequest](r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	tok, _, err := h.svc.Register(r.Context(), req.Email, req.Password, r.UserAgent())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newTokensResponse(tok))
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	req, err := decode[credentialsRequest](r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	tok, _, err := h.svc.Login(r.Context(), req.Email, req.Password, r.UserAgent())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newTokensResponse(tok))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	req, err := decode[refreshRequest](r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	tok, _, err := h.svc.Refresh(r.Context(), req.RefreshToken, r.UserAgent())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newTokensResponse(tok))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), bearer(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- middleware

// Authenticator rejects unauthenticated requests and puts the identity in the
// context. Scoping every query to that identity is enforced here plus in the
// query, never by remembering it per handler.
func (h *Handler) Authenticator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: "missing bearer token"})
			return
		}
		ident, err := h.svc.Authenticate(r.Context(), tok)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: msgTokenInvalid})
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, ident)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// ---------------------------------------------------------------- plumbing

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// decode rejects unknown fields rather than ignoring them: silently dropping a
// misspelled field is a bug users report as "it didn't save".
func decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, errBadRequest{err}
	}
	return v, nil
}

type errBadRequest struct{ err error }

func (e errBadRequest) Error() string { return e.err.Error() }

// fail is the ONLY place a status code is chosen. A 5xx body never carries the
// error string — it can contain query structure or another tenant's values.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	reqID := middleware.GetReqID(r.Context())

	var status int
	var msg string
	switch {
	case errors.As(err, &errBadRequest{}):
		status, msg = http.StatusBadRequest, "malformed request body"
	case errors.Is(err, ErrWeakPassword):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrEmailTaken):
		status, msg = http.StatusConflict, "email already registered"
	case errors.Is(err, ErrInvalidCredentials):
		status, msg = http.StatusUnauthorized, "invalid credentials"
	case errors.Is(err, ErrAccountLocked):
		status, msg = http.StatusTooManyRequests, "account temporarily locked"
	case errors.Is(err, ErrSuspended):
		status, msg = http.StatusForbidden, "account not active"
	case errors.Is(err, ErrTokenReplayed):
		// Deliberately indistinguishable from an ordinary invalid token.
		status, msg = http.StatusUnauthorized, msgTokenInvalid
	case errors.Is(err, ErrTokenInvalid):
		status, msg = http.StatusUnauthorized, msgTokenInvalid
	default:
		status, msg = http.StatusInternalServerError, "internal error"
		h.log.Error("unhandled error", "err", err, "request_id", reqID, "path", r.URL.Path)
	}
	writeJSON(w, status, errorBody{Error: msg, RequestID: reqID})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
