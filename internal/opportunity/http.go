package opportunity

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	return r
}

// knownParams is the allowlist. Unknown query parameters are rejected rather
// than ignored: silently dropping ?remote=true when the parameter is actually
// work_mode is a bug users report as "the filter doesn't work".
var knownParams = map[string]bool{
	"role_family": true, "work_mode": true, "country": true,
	"page_size": true, "cursor": true,
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if !knownParams[k] {
			h.fail(w, r, http.StatusBadRequest, "unknown query parameter: "+k)
			return
		}
	}

	f := ListFilter{
		RoleFamily: optParam(q.Get("role_family")),
		WorkMode:   optParam(q.Get("work_mode")),
		Country:    optParam(q.Get("country")),
		Cursor:     q.Get("cursor"),
	}
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			h.fail(w, r, http.StatusBadRequest, "page_size must be a positive integer")
			return
		}
		f.PageSize = n
	}

	page, err := h.svc.List(r.Context(), f)
	if err != nil {
		h.failErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.failErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func optParam(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// failErr maps domain errors to status codes. This is the only place a status is
// chosen, and a 5xx body never carries the error string — it can contain query
// structure or another tenant's values.
func (h *Handler) failErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		h.fail(w, r, http.StatusNotFound, "not found")
	case errors.Is(err, ErrInvalidInput):
		h.fail(w, r, http.StatusBadRequest, err.Error())
	default:
		h.log.Error("unhandled error", "err", err,
			"request_id", middleware.GetReqID(r.Context()), "path", r.URL.Path)
		h.fail(w, r, http.StatusInternalServerError, "internal error")
	}
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg, RequestID: middleware.GetReqID(r.Context())})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
