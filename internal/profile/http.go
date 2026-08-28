package profile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/auth"
	"github.com/Xubair001/devsignal/internal/normalize"
	"github.com/Xubair001/devsignal/internal/store"
)

type Handler struct {
	svc *Service
	// vectors keeps the profile's embedding in step with its content. Nil is
	// allowed so tests and tools can construct a handler without an embedder,
	// but a nil refresher in production means vectors never update, so the
	// constructor logs it rather than letting it pass silently.
	vectors VectorRefresher
	log     *slog.Logger
}

// VectorRefresher recomputes a user's profile vector. Satisfied by
// profileindex.Service; an interface here so the profile package does not depend
// on an embedder.
type VectorRefresher interface {
	Refresh(ctx context.Context, userID pgtype.UUID) error
}

func NewHandler(svc *Service, vectors VectorRefresher, log *slog.Logger) *Handler {
	if vectors == nil {
		log.Warn("profile handler built with no vector refresher; " +
			"profile edits will not update matching")
	}
	return &Handler{svc: svc, vectors: vectors, log: log}
}

// Routes must be mounted BEHIND authentication: every handler here reads the
// identity from the context and never from the request body.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.get)
	r.Put("/", h.put)
	r.Post("/resume", h.uploadResume)
	r.Get("/resumes", h.listResumes)
	r.Delete("/resumes/{id}", h.deleteResume)
	// Erasure lives here rather than under an admin route: it is the user's own
	// right, exercised by them.
	r.Delete("/", h.erase)
	return r
}

// ---------------------------------------------------------------- DTOs

type profileRequest struct {
	Headline           *string  `json:"headline"`
	YearsExperience    *int16   `json:"years_experience"`
	Seniority          *string  `json:"seniority"`
	IsManagement       bool     `json:"is_management"`
	TargetRoleFamilies []string `json:"target_role_families"`
	TargetCountries    []string `json:"target_countries"`
	WorkModePreference *string  `json:"work_mode_preference"`
	// Empty or absent means no constraint. Retrieval reads it the same way.
	TargetEmploymentTypes []string          `json:"target_employment_types"`
	Languages             []string          `json:"languages"`
	MinSalaryMinor        *int64            `json:"min_salary_minor"`
	SalaryCurrency        *string           `json:"salary_currency"`
	SalaryPeriod          *string           `json:"salary_period"`
	WorkAuthorization     map[string]string `json:"work_authorization"`
	// Skills is absent when the caller is not editing skills, and [] when they
	// are clearing them. A profile form PUT without this field must not wipe the
	// user's skills, so nil and empty cannot be conflated.
	Skills []skillInput `json:"skills"`
}

type skillInput struct {
	Name        string `json:"name"`
	Proficiency *int16 `json:"proficiency"`
	Years       *int16 `json:"years"`
}

type skillResponse struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Origin      string `json:"origin"`
	Proficiency *int16 `json:"proficiency"`
	Years       *int16 `json:"years"`
}

type profileResponse struct {
	Headline              *string           `json:"headline"`
	YearsExperience       *int16            `json:"years_experience"`
	Seniority             *string           `json:"seniority"`
	IsManagement          bool              `json:"is_management"`
	TargetRoleFamilies    []string          `json:"target_role_families"`
	TargetCountries       []string          `json:"target_countries"`
	WorkModePreference    *string           `json:"work_mode_preference"`
	TargetEmploymentTypes []string          `json:"target_employment_types"`
	Languages             []string          `json:"languages"`
	MinSalary             *money            `json:"min_salary"`
	WorkAuthorization     map[string]string `json:"work_authorization"`
	Skills                []skillResponse   `json:"skills"`
	// ProfileVersion is surfaced so a client can tell whether a cached fit score
	// it holds is still current.
	ProfileVersion int32 `json:"profile_version"`
	// UnresolvedSkills names the skills we could not place in the ontology.
	//
	// Returned rather than dropped: the profile deliberately cannot mint new
	// skills, so a name we do not recognise counts toward nothing. Telling the
	// user is the difference between a typo they can fix and a skill that
	// silently never matches anything.
	UnresolvedSkills []string `json:"unresolved_skills,omitempty"`
}

type money struct {
	MinMinor int64  `json:"min_minor"`
	Currency string `json:"currency"`
	Period   string `json:"period"`
}

type resumeResponse struct {
	ID         string  `json:"id"`
	Filename   *string `json:"filename"`
	SizeBytes  int64   `json:"size_bytes"`
	ParseState string  `json:"parse_state"`
	ParseError *string `json:"parse_error"`
	TextChars  *int32  `json:"text_chars"`
	UploadedAt string  `json:"uploaded_at"`
	// Deliberately absent: object keys and any extracted content. A client has no
	// use for storage paths, and the text itself must not travel.
}

// ---------------------------------------------------------------- handlers

// identity resolves the caller, writing the 401 itself so each handler is one
// line rather than the same six-line block repeated. Every handler here operates
// on the caller's own data, so this is the only source of the user id — never the
// request body.
func (h *Handler) identity(w http.ResponseWriter, r *http.Request) (*auth.Identity, bool) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "unauthenticated"})
		return nil, false
	}
	return id, true
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identity(w, r)
	if !ok {
		return
	}
	p, skills, err := h.svc.Get(r.Context(), id.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toProfileResponse(p, skills))
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identity(w, r)
	if !ok {
		return
	}
	var req profileRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.failMsg(w, r, http.StatusBadRequest, "malformed request body")
		return
	}

	in := Input{
		Headline: req.Headline, YearsExperience: req.YearsExperience,
		SeniorityOrdinal: normalize.SeniorityOrdinal(req.Seniority), IsManagement: req.IsManagement,
		TargetRoleFamilies: req.TargetRoleFamilies, TargetCountries: req.TargetCountries,
		WorkModePreference: req.WorkModePreference, Languages: req.Languages,
		TargetEmploymentTypes: req.TargetEmploymentTypes,
		MinSalaryMinor:        req.MinSalaryMinor, SalaryCurrency: req.SalaryCurrency,
		SalaryPeriod: req.SalaryPeriod,
	}
	if req.WorkAuthorization != nil {
		raw, err := json.Marshal(req.WorkAuthorization)
		if err != nil {
			h.failMsg(w, r, http.StatusBadRequest, "invalid work_authorization")
			return
		}
		in.WorkAuthorization = raw
	}

	if req.Skills != nil {
		in.Skills = make([]SkillInput, 0, len(req.Skills))
		for _, sk := range req.Skills {
			in.Skills = append(in.Skills, SkillInput(sk))
		}
	}

	p, err := h.svc.Save(r.Context(), id.UserID, id.TenantID, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Skills after the profile row exists, and only when the caller sent the
	// field: a form PUT that omits skills is editing preferences, not clearing
	// someone's skill list.
	var unresolved []string
	if req.Skills != nil {
		res, serr := h.svc.SaveSkills(r.Context(), id.UserID, in.Skills)
		if serr != nil {
			h.fail(w, r, serr)
			return
		}
		unresolved = res.Unresolved
	}
	// The vector is derived data. A failure here degrades match quality until the
	// next edit or backfill; it is not a reason to tell the user their own
	// preferences failed to save. The stored profile_version makes the staleness
	// detectable either way.
	if h.vectors != nil {
		if verr := h.vectors.Refresh(r.Context(), id.UserID); verr != nil {
			h.log.Error("refreshing profile vector",
				"user_id", id.UserID.String(), "err", verr)
		}
	}
	_, skills, _ := h.svc.Get(r.Context(), id.UserID)
	resp := toProfileResponse(p, skills)
	resp.UnresolvedSkills = unresolved
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) uploadResume(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identity(w, r)
	if !ok {
		return
	}

	// Capped before reading: an unbounded read of an upload is how a service OOMs.
	r.Body = http.MaxBytesReader(w, r.Body, MaxResumeBytes+1024)
	if err := r.ParseMultipartForm(MaxResumeBytes); err != nil {
		h.failMsg(w, r, http.StatusBadRequest, "expected a multipart form with a 'file' field")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		h.failMsg(w, r, http.StatusBadRequest, "missing 'file' field")
		return
	}
	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(io.LimitReader(file, MaxResumeBytes+1))
	if err != nil {
		h.failMsg(w, r, http.StatusBadRequest, "could not read the upload")
		return
	}

	ct := hdr.Header.Get("Content-Type")
	if ct == "" {
		ct = TypeText
	}
	rec, err := h.svc.UploadResume(r.Context(), id.UserID, Upload{
		Filename: hdr.Filename, ContentType: ct, Body: body,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toResumeResponse(rec))
}

func (h *Handler) listResumes(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identity(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ListResumes(r.Context(), id.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]resumeResponse, 0, len(rows))
	for _, rec := range rows {
		out = append(out, toResumeResponse(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) deleteResume(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identity(w, r)
	if !ok {
		return
	}
	var rid pgUUID
	if err := rid.Scan(chi.URLParam(r, "id")); err != nil {
		h.failMsg(w, r, http.StatusBadRequest, "not a uuid")
		return
	}
	// Scoped to the caller in the query itself, not by checking ownership first:
	// a scope clause cannot be forgotten the way a check can.
	if err := h.svc.DeleteResume(r.Context(), id.UserID, rid.UUID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) erase(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identity(w, r)
	if !ok {
		return
	}
	rep, err := h.svc.Erase(r.Context(), id.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The response states what was done per location. An erasure the user cannot
	// inspect is a promise rather than a guarantee.
	steps := make([]map[string]any, 0, len(rep.Steps))
	for _, s := range rep.Steps {
		steps = append(steps, map[string]any{
			"location": s.Location, "status": s.Status, "items": s.Items,
		})
	}
	status := http.StatusOK
	if !rep.Complete || rep.TracesRemaining != 0 || rep.ObjectsRemaining != 0 {
		// Partial erasure is not a success. Saying so is the honest answer.
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{
		"complete":          rep.Complete,
		"traces_remaining":  rep.TracesRemaining,
		"objects_remaining": rep.ObjectsRemaining,
		"steps":             steps,
	})
}

// ---------------------------------------------------------------- mapping

func toProfileResponse(p store.Profile, skills []store.ListProfileSkillsRow) profileResponse {
	out := profileResponse{
		Headline: p.Headline, YearsExperience: p.YearsExperience,
		Seniority: normalize.SeniorityLabel(p.SeniorityOrdinal), IsManagement: p.IsManagement,
		TargetRoleFamilies:    nonNil(p.TargetRoleFamilies),
		TargetEmploymentTypes: nonNil(p.TargetEmploymentTypes),
		TargetCountries:       nonNil(p.TargetCountries),
		WorkModePreference:    p.WorkModePreference,
		Languages:             nonNil(p.Languages),
		WorkAuthorization:     decodeAuth(p.WorkAuthorization),
		ProfileVersion:        p.ProfileVersion,
		Skills:                make([]skillResponse, 0, len(skills)),
	}
	if p.MinSalaryMinor != nil {
		m := &money{MinMinor: *p.MinSalaryMinor}
		if p.SalaryCurrency != nil {
			m.Currency = *p.SalaryCurrency
		}
		if p.SalaryPeriod != nil {
			m.Period = *p.SalaryPeriod
		}
		out.MinSalary = m
	}
	for _, s := range skills {
		out.Skills = append(out.Skills, skillResponse{
			Slug: s.CanonicalSlug, Name: s.DisplayName, Origin: s.Origin,
			Proficiency: s.Proficiency, Years: s.Years,
		})
	}
	return out
}

func toResumeResponse(r store.Resume) resumeResponse {
	return resumeResponse{
		ID: r.ID.String(), Filename: r.Filename, SizeBytes: r.SizeBytes,
		ParseState: r.ParseState, ParseError: r.ParseError, TextChars: r.TextChars,
		UploadedAt: r.UploadedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// nonNil keeps an empty list serializing as [] rather than null: a client
// calling .length on null breaks.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func decodeAuth(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// ---------------------------------------------------------------- plumbing

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		h.failMsg(w, r, http.StatusNotFound, "no profile yet")
	case errors.Is(err, ErrTooLarge):
		h.failMsg(w, r, http.StatusRequestEntityTooLarge, "resume exceeds the size limit")
	case errors.Is(err, ErrUnsupportedType):
		h.failMsg(w, r, http.StatusUnsupportedMediaType, "unsupported resume format")
	case errors.Is(err, ErrInvalidInput):
		h.failMsg(w, r, http.StatusBadRequest, err.Error())
	default:
		// Never the error string: it can carry query structure, and here it could
		// carry document content.
		h.log.Error("unhandled profile error", "err", err,
			"request_id", middleware.GetReqID(r.Context()), "path", r.URL.Path)
		h.failMsg(w, r, http.StatusInternalServerError, "internal error")
	}
}

func (h *Handler) failMsg(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg, RequestID: middleware.GetReqID(r.Context())})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
