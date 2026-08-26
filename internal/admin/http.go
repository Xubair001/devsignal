package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/auth"
)

// Handler serves the operations surface.
//
// Mounted under /internal/admin, behind the same session mechanism as everything
// else plus one authorization middleware. One middleware rather than a check per
// handler: a per-handler check is how one handler ends up without it, and the one
// that ends up without it is always the destructive one.
type Handler struct {
	svc *Service
	log Logger
}

// JSON keys used in more than one response, named so a typo cannot make two
// endpoints disagree about the same field.
const (
	keyStatus      = "status"
	keyTargetState = "target_state"
)

// Logger is the subset of slog this package needs.
type Logger interface {
	Error(msg string, args ...any)
	Warn(msg string, args ...any)
}

// NewHandler builds the handler.
func NewHandler(svc *Service, log Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RequireAdmin is the authorization middleware.
//
// Answers 404 rather than 403 for a non-admin. A 403 confirms the surface exists
// and that the caller found a real route, which is free reconnaissance; 404 says
// nothing. Admins who lose their role see the same thing, which is the correct
// trade for an internal surface.
func (h *Handler) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.FromContext(r.Context())
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := h.svc.Authorize(r.Context(), id.UserID); err != nil {
			if !errors.Is(err, ErrForbidden) {
				h.log.Error("authorizing admin", "err", err)
			}
			// Logged at warn so an attempt to reach the surface is visible without
			// being an error. The audit log records successful actions; this records
			// refused ones.
			h.log.Warn("admin access refused", "user_id", id.UserID.String())
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Routes mounts the admin surface. Wrap with RequireAdmin at the mount point.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/sources", h.listSources)
	r.Get("/sources/{id}/health", h.sourceHealth)
	r.Post("/sources/{id}/status", h.setSourceStatus)
	r.Post("/sources/{id}/requeue", h.requeueSource)
	r.Get("/sources/{id}/purge-plan", h.purgePlan)
	r.Post("/sources/{id}/purge", h.purgeSource)

	r.Get("/opportunities/{id}/sources", h.provenance)
	r.Post("/opportunities/{id}/unmerge", h.unmerge)
	r.Post("/opportunities/{id}/requeue", h.requeueOpportunity)

	r.Get("/merge-candidates", h.listMergeCandidates)
	r.Post("/merge-candidates/{id}/resolve", h.resolveMergeCandidate)

	r.Get("/flags", h.listFlags)
	r.Post("/flags/{id}/resolve", h.resolveFlag)

	return r
}

// ---------------------------------------------------------------- sources

func (h *Handler) listSources(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.ListSources(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	type item struct {
		ID                 string     `json:"id"`
		Name               string     `json:"name"`
		Tier               string     `json:"tier"`
		Type               string     `json:"type"`
		Status             string     `json:"status"`
		LastSuccessAt      *time.Time `json:"last_success_at"`
		LastFailureAt      *time.Time `json:"last_failure_at"`
		ItemsDiscovered    int64      `json:"items_discovered"`
		ItemsProcessed     int64      `json:"items_processed"`
		PostingsAttributed int64      `json:"postings_attributed"`
		// The legal review trail. Surfaced because a source with no recorded
		// review is a compliance problem, and it should be visible on the same
		// screen as its health rather than in a document nobody opens.
		LegalBasis      string     `json:"legal_basis"`
		RobotsCheckedAt *time.Time `json:"robots_checked_at"`
		TermsReviewedAt *time.Time `json:"terms_reviewed_at"`
		ReviewedBy      *string    `json:"reviewed_by"`
		ETagSupported   bool       `json:"etag_supported"`
	}
	out := struct {
		Sources []item `json:"sources"`
	}{Sources: make([]item, 0, len(rows))}
	for _, s := range rows {
		out.Sources = append(out.Sources, item{
			ID: s.ID.String(), Name: s.Name, Tier: s.Tier, Type: s.Type, Status: s.Status,
			LastSuccessAt: tp(s.LastSuccessAt), LastFailureAt: tp(s.LastFailureAt),
			ItemsDiscovered: s.ItemsDiscovered, ItemsProcessed: s.ItemsProcessed,
			PostingsAttributed: s.PostingsAttributed,
			LegalBasis:         s.LegalBasis,
			RobotsCheckedAt:    tp(s.RobotsCheckedAt), TermsReviewedAt: tp(s.TermsReviewedAt),
			ReviewedBy: s.ReviewedBy, ETagSupported: s.EtagSupported,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) sourceHealth(w http.ResponseWriter, r *http.Request) {
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	days := int32(intParam(r, "days", 30))
	rows, err := h.svc.SourceHealth(r.Context(), id, days)
	if err != nil {
		h.fail(w, err)
		return
	}
	type day struct {
		Day            string `json:"day"`
		Polls          int32  `json:"polls"`
		PollFailures   int32  `json:"poll_failures"`
		NotModified    int32  `json:"not_modified"`
		PostingsSeen   int32  `json:"postings_seen"`
		PostingsUsable int32  `json:"postings_usable"`
		// Field fill rates: the columns that reveal parser rot. A source whose
		// with_company drops to zero is broken even while its fetches succeed.
		WithCompany  int32 `json:"with_company"`
		WithLocation int32 `json:"with_location"`
		WithApplyURL int32 `json:"with_apply_url"`
		WithLanguage int32 `json:"with_language"`
		WithSalary   int32 `json:"with_salary"`
	}
	out := struct {
		Days []day `json:"days"`
	}{Days: make([]day, 0, len(rows))}
	for _, r := range rows {
		out.Days = append(out.Days, day{
			Day: r.Day.Time.Format(time.DateOnly), Polls: r.Polls,
			PollFailures: r.PollFailures, NotModified: r.NotModified,
			PostingsSeen: r.PostingsSeen, PostingsUsable: r.PostingsUsable,
			WithCompany: r.WithCompany, WithLocation: r.WithLocation,
			WithApplyURL: r.WithApplyUrl, WithLanguage: r.WithLanguage,
			WithSalary: r.WithSalary,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type statusRequest struct {
	Status string `json:"status"`
	// Note is stored in the audit entry. Not required, but an unexplained
	// quarantine is the kind of thing that confuses whoever is on call next.
	Note string `json:"note"`
}

func (h *Handler) setSourceStatus(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req statusRequest
	if !decode(w, r, &req) {
		return
	}
	row, err := h.svc.SetSourceStatus(r.Context(), actor, id, req.Status, req.Note)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id": row.ID.String(), "name": row.Name, keyStatus: row.Status,
	})
}

type requeueRequest struct {
	// TargetState is validated against the pipeline state machine, not accepted as
	// a string: an unknown value would strand every posting in a state no worker
	// claims.
	TargetState string `json:"target_state"`
	Note        string `json:"note"`
}

func (h *Handler) requeueSource(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req requeueRequest
	if !decode(w, r, &req) {
		return
	}
	n, err := h.svc.RequeueSource(r.Context(), actor, id, req.TargetState, req.Note)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requeued": n, keyTargetState: req.TargetState,
	})
}

func (h *Handler) requeueOpportunity(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req requeueRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.svc.RequeueOpportunity(r.Context(), actor, id, req.TargetState, req.Note); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- purge

func (h *Handler) purgePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	plan, err := h.svc.PlanSourcePurge(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source_id":           plan.SourceID.String(),
		"total_attributed":    plan.Total,
		"merged":              plan.Merged,
		"also_seen_elsewhere": plan.AlsoSeenElsewhere,
		// The number the caller must echo back to actually run the purge.
		"will_be_deleted": plan.WillBeDeleted,
	})
}

type purgeRequest struct {
	// ConfirmDeleteCount must equal what the plan reported. A destructive
	// operation that runs on a number the operator has not seen is how the wrong
	// source gets emptied.
	ConfirmDeleteCount int64 `json:"confirm_delete_count"`
	// DryRun performs the count and writes the audit entry without deleting, so
	// the drill blueprint §30 asks for is a real rehearsal.
	DryRun bool   `json:"dry_run"`
	Note   string `json:"note"`
}

func (h *Handler) purgeSource(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req purgeRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := h.svc.PurgeSource(r.Context(), actor, id,
		req.ConfirmDeleteCount, req.DryRun, req.Note)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dry_run":               res.DryRun,
		"source_rows_deleted":   res.SourceRowsDeleted,
		"opportunities_deleted": res.OpportunitiesDeleted,
		"merge_records_deleted": res.MergeRecordsDeleted,
	})
}

// ---------------------------------------------------------------- provenance

func (h *Handler) provenance(w http.ResponseWriter, r *http.Request) {
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	p, err := h.svc.Provenance(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	type srcRow struct {
		ID              string     `json:"id"`
		SourceName      string     `json:"source_name"`
		ATSType         *string    `json:"ats_type"`
		ATSJobID        *string    `json:"ats_job_id"`
		ApplyURL        *string    `json:"apply_url"`
		MergeReason     *string    `json:"merge_reason"`
		MergeConfidence *float32   `json:"merge_confidence"`
		MergedBy        *string    `json:"merged_by"`
		FirstSeenAt     *time.Time `json:"first_seen_at"`
		LastSeenAt      *time.Time `json:"last_seen_at"`
	}
	type mergedRow struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		SourceRows int64  `json:"source_rows"`
	}
	out := struct {
		Sources []srcRow `json:"sources"`
		// MergedIn are the postings folded into this one. Each is an un-merge
		// candidate, which is why the count of source rows is included.
		MergedIn []mergedRow `json:"merged_in"`
	}{
		Sources:  make([]srcRow, 0, len(p.Sources)),
		MergedIn: make([]mergedRow, 0, len(p.MergedInto)),
	}
	for _, s := range p.Sources {
		out.Sources = append(out.Sources, srcRow{
			ID: s.ID.String(), SourceName: s.SourceName,
			ATSType: s.AtsType, ATSJobID: s.AtsJobID, ApplyURL: s.ApplyUrl,
			MergeReason: s.MergeReason, MergeConfidence: s.MergeConfidence,
			MergedBy:    s.MergedBy,
			FirstSeenAt: tp(s.FirstSeenAt), LastSeenAt: tp(s.LastSeenAt),
		})
	}
	for _, m := range p.MergedInto {
		out.MergedIn = append(out.MergedIn, mergedRow{
			ID: m.ID.String(), Title: m.TitleRaw, SourceRows: m.SourceRows,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type noteRequest struct {
	Note string `json:"note"`
}

func (h *Handler) unmerge(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req noteRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.svc.Unmerge(r.Context(), actor, id, req.Note); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listMergeCandidates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.ListMergeCandidates(r.Context(), int32(intParam(r, "limit", 50)))
	if err != nil {
		h.fail(w, err)
		return
	}
	type item struct {
		ID              string    `json:"id"`
		LeftID          string    `json:"left_opportunity_id"`
		RightID         string    `json:"right_opportunity_id"`
		LeftTitle       string    `json:"left_title"`
		RightTitle      string    `json:"right_title"`
		Reason          string    `json:"reason"`
		Confidence      float32   `json:"confidence"`
		WithheldBecause string    `json:"withheld_because"`
		CreatedAt       time.Time `json:"created_at"`
	}
	out := struct {
		Candidates []item `json:"candidates"`
	}{Candidates: make([]item, 0, len(rows))}
	for _, c := range rows {
		out.Candidates = append(out.Candidates, item{
			ID: c.ID.String(), LeftID: c.LeftOpportunityID.String(),
			RightID:   c.RightOpportunityID.String(),
			LeftTitle: c.LeftTitle, RightTitle: c.RightTitle,
			Reason: c.Reason, Confidence: c.Confidence,
			WithheldBecause: c.WithheldBecause, CreatedAt: c.CreatedAt.Time,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type resolveRequest struct {
	Resolution string `json:"resolution"`
	Note       string `json:"note"`
}

func (h *Handler) resolveMergeCandidate(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req resolveRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.svc.ResolveMergeCandidate(r.Context(), actor, id, req.Resolution, req.Note); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- flags

func (h *Handler) listFlags(w http.ResponseWriter, r *http.Request) {
	var status *string
	if v := r.URL.Query().Get("status"); v != "" {
		status = &v
	}
	rows, err := h.svc.ListFlags(r.Context(), status, int32(intParam(r, "limit", 50)))
	if err != nil {
		h.fail(w, err)
		return
	}
	type item struct {
		ID            string    `json:"id"`
		OpportunityID string    `json:"opportunity_id"`
		Title         string    `json:"title"`
		CompanyName   *string   `json:"company_name"`
		Reason        string    `json:"reason"`
		Detail        *string   `json:"detail"`
		Status        string    `json:"status"`
		CreatedAt     time.Time `json:"created_at"`
		// FlagsOnPosting surfaces pile-ups: three reports on one listing is a
		// different signal from three reports across three listings.
		FlagsOnPosting int64 `json:"flags_on_posting"`
		PostingClosed  bool  `json:"posting_closed"`
	}
	out := struct {
		Flags []item `json:"flags"`
	}{Flags: make([]item, 0, len(rows))}
	for _, f := range rows {
		out.Flags = append(out.Flags, item{
			ID: f.ID.String(), OpportunityID: f.OpportunityID.String(),
			Title: f.TitleRaw, CompanyName: f.CompanyName,
			Reason: f.Reason, Detail: f.Detail, Status: f.Status,
			CreatedAt: f.CreatedAt.Time, FlagsOnPosting: f.FlagsOnPosting,
			PostingClosed: f.ClosedAt.Valid,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type resolveFlagRequest struct {
	Status string  `json:"status"`
	Note   *string `json:"note"`
}

func (h *Handler) resolveFlag(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req resolveFlagRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.svc.ResolveFlag(r.Context(), actor, id, req.Status, req.Note); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------- the user-facing flag route

// FlagRoutes is the one route in this package that is NOT admin-only: users
// report listings. Mounted under the authenticated user API, not under
// /internal/admin.
func (h *Handler) FlagRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/{id}/flag", h.raiseFlag)
	r.Get("/flag-reasons", h.flagReasons)
	return r
}

type raiseFlagRequest struct {
	Reason string  `json:"reason"`
	Detail *string `json:"detail"`
}

func (h *Handler) raiseFlag(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, ok := h.id(w, r)
	if !ok {
		return
	}
	var req raiseFlagRequest
	if !decode(w, r, &req) {
		return
	}
	flagID, err := h.svc.RaiseFlag(r.Context(), actor, id, req.Reason, req.Detail)
	if err != nil {
		if errors.Is(err, ErrAlreadyResolved) {
			// Already reported by this user. Their point is in the queue; saying so
			// is friendlier than an error and truer than a fresh success.
			writeJSON(w, http.StatusOK, map[string]string{
				keyStatus: "already_reported",
			})
			return
		}
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": flagID.String()})
}

func (h *Handler) flagReasons(w http.ResponseWriter, _ *http.Request) {
	labels := map[string]string{
		FlagScamOrFraud:    "Scam or fraud",
		FlagNotARealJob:    "Not a real job",
		FlagMisleadingPay:  "Misleading pay information",
		FlagDiscriminatory: "Discriminatory content",
		FlagExpired:        "This role is no longer open",
		FlagDuplicate:      "Duplicate of another listing",
		FlagOther:          "Something else",
	}
	type reason struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	out := struct {
		Reasons []reason `json:"reasons"`
	}{}
	for _, v := range FlagReasons {
		out.Reasons = append(out.Reasons, reason{v, labels[v]})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------- plumbing

func (h *Handler) actor(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return pgtype.UUID{}, false
	}
	return id.UserID, true
}

func (h *Handler) id(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed id")
		return id, false
	}
	return id, true
}

// fail maps service errors onto status codes in one place, so a new handler
// cannot invent its own mapping.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrInvalidStatus), errors.Is(err, ErrInvalidReason):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrAlreadyResolved):
		writeErr(w, http.StatusConflict, "already resolved")
	case errors.Is(err, ErrNotReversible):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrConfirmationMismatch):
		// 409 rather than 400: the request was well formed, the world moved.
		writeErr(w, http.StatusConflict, err.Error())
	default:
		h.log.Error("admin request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body")
		return false
	}
	return true
}

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func tp(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
