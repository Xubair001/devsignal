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
	// Unauthenticated: a verification link is followed in whatever browser opened
	// the email, and the single-use token is the authorization.
	r.Post("/verify", h.verify)
	return r
}

// AccountRoutes are the authenticated account actions.
//
// Separate from Routes() because these need a session and those must not. A
// resend that accepted an email address without one would be an enumeration
// oracle and a way to have us mail strangers on request.
func (h *Handler) AccountRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/resend-verification", h.resendVerification)
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
	tok, ident, err := h.svc.Register(r.Context(), req.Email, req.Password, r.UserAgent())
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Issue the verification link, but never fail the registration on it. The
	// account exists and the address is unverified either way; a resend is one
	// click, and refusing the signup because mail is down would lose the account.
	// The plaintext token is DISCARDED here — it only ever leaves through the
	// transport, never through this response.
	if _, verr := h.svc.IssueEmailVerification(r.Context(), ident.UserID, req.Email); verr != nil {
		h.log.Error("issuing email verification",
			"user_id", ident.UserID.String(), "err", verr)
	}
	writeJSON(w, http.StatusCreated, newTokensResponse(tok))
}

type verifyRequest struct {
	Token string `json:"token"`
}

// verify consumes a verification link.
//
// Unauthenticated on purpose: the link arrives by email and is followed in
// whatever browser opened it, which may hold no session. The token IS the
// authorization, and it is single-use.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	req, err := decode[verifyRequest](r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "token is required"})
		return
	}
	if _, verr := h.svc.VerifyEmail(r.Context(), req.Token); verr != nil {
		if errors.Is(verr, ErrVerificationInvalid) {
			// One message for expired, used and unknown alike. Distinguishing them
			// tells a caller whether an address is registered.
			writeJSON(w, http.StatusBadRequest, errorBody{
				Error: "this link is invalid or has expired. Request a new one."})
			return
		}
		h.log.Error("verifying email", "err", verr)
		writeJSON(w, http.StatusInternalServerError, errorBody{
			Error: "could not verify the address"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resendVerification issues a fresh link for the CALLER's own address.
//
// Authenticated, and takes no email parameter. An unauthenticated resend that
// accepts an address is an email-enumeration oracle and a way to have us mail
// strangers on request.
func (h *Handler) resendVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "unauthenticated"})
		return
	}
	user, err := h.svc.q.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		h.log.Error("loading user for resend", "user_id", id.UserID.String(), "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{
			Error: "could not send the email"})
		return
	}
	if _, verr := h.svc.IssueEmailVerification(r.Context(), id.UserID, string(user.Email)); verr != nil {
		if errors.Is(verr, ErrAlreadyVerified) {
			// Not an error. Idempotent from the caller's point of view.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.log.Error("resending verification", "user_id", id.UserID.String(), "err", verr)
		writeJSON(w, http.StatusInternalServerError, errorBody{
			Error: "could not send the email"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
