package engagement

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/auth"
	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/opportunity"
)

// The feed's response types are the enforcement point for blueprint §3, the same
// way internal/opportunity's DTOs are for the corpus. Two absences are deliberate
// and load-bearing:
//
//   - There is no percentage field, and no field a client could reasonably render
//     as one. The band and the per-factor points are what may be shown. A number
//     out of 100 reads as a probability, and we have measured no calibration that
//     would justify it (blueprint §20).
//   - There is no priority field. Priority orders the list and is volatile by
//     design; exposing it invites a client to display it, and then a match score
//     changes overnight because a posting got older.
//
// Both are asserted by tests, because a DTO is only an enforcement point while
// someone is checking.

// FactorView is one term of the fit sum, as arithmetic the user can check.
type FactorView struct {
	Factor string `json:"factor"`
	// Points earned and the most this factor could have earned. "+29 of 35".
	Points    float64 `json:"points"`
	MaxPoints float64 `json:"max_points"`
	// Scored is false when there was no observable data. The client must show
	// these differently from a zero: "we could not read the required skills" and
	// "you match none of them" mean opposite things.
	Scored bool   `json:"scored"`
	Reason string `json:"reason,omitempty"`
}

// FitView is the match, expressed the only way it is allowed to be shown.
type FitView struct {
	// Band is the headline: "Strong fit", "Worth a look", "Stretch", or
	// "Not enough information".
	Band string `json:"band"`
	// Points earned out of points achievable. Below 100 whenever a factor had no
	// data, which is most postings today.
	Points    int `json:"points"`
	MaxPoints int `json:"max_points"`
	// Summary is the one-line human form of the pair above.
	Summary string       `json:"summary"`
	Factors []FactorView `json:"factors"`
	// Versions let a client tell a stale cached explanation from a current one.
	Versions VersionView `json:"versions"`
}

// VersionView is what produced the score.
type VersionView struct {
	Weights   string `json:"weights"`
	Embedding string `json:"embedding"`
	Profile   int32  `json:"profile"`
}

// StateView is what the user has already done with this posting.
type StateView struct {
	Saved   bool `json:"saved"`
	Applied bool `json:"applied"`
	// AppliedAt is when the user TOLD US they applied. We cannot see the
	// employer's side, and the field name says so.
	AppliedAt *time.Time `json:"applied_at"`
	Dismissed bool       `json:"dismissed"`
}

// FeedItem is one row of the feed.
type FeedItem struct {
	OpportunityID string    `json:"opportunity_id"`
	Title         string    `json:"title"`
	Fit           FitView   `json:"fit"`
	State         StateView `json:"state"`
	// Channels records how retrieval found it. Useful for support and for the
	// admin surface; harmless to expose and it makes "why is this here" answerable.
	Channels []string `json:"channels"`
	// Posting is the read-side summary: company, location, salary, apply URL and
	// liveness. Shared with the browse list rather than duplicated, so the two
	// surfaces cannot drift.
	//
	// Not a pointer and not omitempty: an item without it must never reach the
	// client, because the display rules forbid showing a posting in the daily
	// feed whose open state is unknown. The handler drops such an item instead.
	Posting opportunity.Summary `json:"posting"`
}

// FeedResponse is today's feed.
type FeedResponse struct {
	Items []FeedItem `json:"items"`
	// Diagnostics are honest about what the feed is, especially when it is thin.
	// A short feed and a broken pipeline look identical without them.
	Diagnostics FeedDiagnostics `json:"diagnostics"`
}

// FeedDiagnostics explains the shape of the result rather than only its contents.
type FeedDiagnostics struct {
	EligibleAfterPredicates int `json:"eligible_after_predicates"`
	Retrieved               int `json:"retrieved"`
	PassedEligibilityGate   int `json:"passed_eligibility_gate"`
	ExcludedByGate          int `json:"excluded_by_gate"`
	// Truncated says the candidate set hit its cap, so the feed is not an
	// exhaustive view of what matched.
	Truncated bool `json:"retrieval_truncated"`
	// ClosedSinceScoring counts postings that ranked but closed or were merged
	// away before the response was written. Reported rather than hidden: it is
	// the difference between a quiet market and a stale score.
	ClosedSinceScoring int `json:"closed_since_scoring"`
}

// ExcludedResponse answers "why am I not seeing X".
type ExcludedResponse struct {
	Items []ExcludedItem `json:"items"`
}

// ExcludedItem is a posting the gate removed, with the specific reason.
type ExcludedItem struct {
	OpportunityID string `json:"opportunity_id"`
	Title         string `json:"title"`
	// FailedChecks names the gates; Reasons is the same information written for
	// the person it excluded.
	FailedChecks []string `json:"failed_checks"`
	Reasons      []string `json:"reasons"`
}

// embeddingVersion is surfaced in explanations so a client can tell a stale
// cached breakdown from a current one. Read from the embed package rather than
// restated, because two copies of a version string is how they drift.
var embeddingVersion = embed.LocalVersion

// Handler serves the feed and records engagement.
type Handler struct {
	matcher *matching.Service
	svc     *Service
	// opps supplies the posting itself. The matcher returns a ranking; it does
	// not return what a card has to show.
	opps *opportunity.Service
	log  Logger
}

// Logger is the subset of slog the handler needs.
type Logger interface {
	Error(msg string, args ...any)
	Warn(msg string, args ...any)
}

// NewHandler builds the handler.
func NewHandler(
	matcher *matching.Service, svc *Service, opps *opportunity.Service, log Logger,
) *Handler {
	return &Handler{matcher: matcher, svc: svc, opps: opps, log: log}
}

// defaultFeedSize is what the product promises daily, which is also why
// Precision@7 is measured at 7.
const defaultFeedSize = 7

// maxFeedSize bounds what a client can ask for. Not a performance guard so much
// as a product one: a feed of 500 is a search result, and the two are different
// surfaces with different honesty obligations.
const maxFeedSize = 50

// Routes mounts the feed and engagement endpoints.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.feed)
	r.Get("/excluded", h.excluded)
	r.Get("/{id}/explanation", h.explanation)
	return r
}

// EngagementRoutes mounts the write surface, separate because the paths differ.
func (h *Handler) EngagementRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/saved/{id}", h.act(EventSaved))
	r.Delete("/saved/{id}", h.act(EventUnsaved))
	r.Post("/applied/{id}", h.act(EventApplied))
	r.Post("/opened/{id}", h.act(EventOpened))
	r.Post("/dismissed/{id}", h.act(EventDismissed))
	r.Get("/saved", h.listSaved)
	r.Get("/dismiss-reasons", h.dismissReasons)
	return r
}

func (h *Handler) feed(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(w, r)
	if !ok {
		return
	}
	size := clampSize(r.URL.Query().Get("limit"))

	// Unlimited from the matcher, then filtered, then cut to size. Applying the
	// limit first would let dismissed postings consume slots and return a feed of
	// three when seven were asked for.
	res, err := h.matcher.MatchForUser(r.Context(), id.UserID, 0)
	if err != nil {
		if errors.Is(err, matching.ErrNoProfile) {
			writeErr(w, http.StatusConflict, "create a profile before requesting a feed")
			return
		}
		h.log.Error("building feed", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not build the feed")
		return
	}

	state, err := h.svc.StateFor(r.Context(), id.UserID)
	if err != nil {
		// Degrade rather than fail: a feed without save badges is still a feed.
		h.log.Warn("loading engagement state", "user_id", id.UserID.String(), "err", err)
		state = map[string]State{}
	}

	// Rank order, minus what the user already dismissed, capped at what could
	// possibly be shown.
	//
	// A dismissal is the user telling us to stop showing it. Honouring that is
	// not optional: a feed that keeps returning something someone rejected
	// teaches them their feedback does nothing.
	//
	// The cap is what keeps the posting lookup proportional to the page rather
	// than to the candidate set — loading 188 rows to render 7 is the same waste
	// the batched write fixed on the way in. The slack above `size` covers
	// postings that closed between scoring and now.
	ranked := make([]matching.Match, 0, size*2)
	for _, m := range res.Matches {
		if state[m.Opportunity.ID.String()].Dismissed {
			continue
		}
		if len(ranked) >= size*2 {
			break
		}
		ranked = append(ranked, m)
	}

	ids := make([]pgtype.UUID, 0, len(ranked))
	for _, m := range ranked {
		ids = append(ids, m.Opportunity.ID)
	}
	// The posting itself. This one does NOT degrade: an empty feed is a product
	// statement — "nothing met your bar today" — and returning it because a
	// query failed would be a manufactured signal. A 500 is honest; a quiet
	// market that never happened is not.
	postings, err := h.opps.SummariesByID(r.Context(), ids)
	if err != nil {
		h.log.Error("loading feed postings", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not build the feed")
		return
	}

	out := FeedResponse{
		Items: make([]FeedItem, 0, len(res.Matches)),
		Diagnostics: FeedDiagnostics{
			EligibleAfterPredicates: res.Retrieval.Eligible,
			Retrieved:               len(res.Retrieval.Candidates),
			PassedEligibilityGate:   res.Passed,
			ExcludedByGate:          len(res.Excluded),
			Truncated:               res.Retrieval.Truncated,
		},
	}
	shown := make([]matching.Match, 0, size)
	for _, m := range ranked {
		if len(shown) >= size {
			break
		}
		// Closed or merged away between scoring and now. Dropping it is hard rule
		// 9's mirror image: we never invent a closure, and we never serve one we
		// already observed.
		posting, ok := postings[m.Opportunity.ID.String()]
		if !ok {
			out.Diagnostics.ClosedSinceScoring++
			continue
		}
		shown = append(shown, m)
		out.Items = append(out.Items, toFeedItem(m, posting, state, res.ProfileVersion))
	}

	// Impressions are recorded AFTER the response is assembled and only for what
	// is actually returned, so the saturation penalty reflects what a user saw
	// rather than what the matcher considered.
	h.svc.RecordShown(r.Context(), id.UserID, shown, res.ProfileVersion)

	writeJSON(w, http.StatusOK, out)
}

// excluded answers "why am I not seeing X" directly.
//
// A separate endpoint rather than a field on the feed: it is a diagnostic a user
// asks for deliberately, and mixing it into the feed would put roles they cannot
// apply to in front of them, which is what the gate exists to prevent.
func (h *Handler) excluded(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(w, r)
	if !ok {
		return
	}
	res, err := h.matcher.MatchForUser(r.Context(), id.UserID, 0)
	if err != nil {
		if errors.Is(err, matching.ErrNoProfile) {
			writeErr(w, http.StatusConflict, "create a profile first")
			return
		}
		h.log.Error("listing exclusions", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not list exclusions")
		return
	}

	out := ExcludedResponse{Items: make([]ExcludedItem, 0, len(res.Excluded))}
	for _, e := range res.Excluded {
		out.Items = append(out.Items, ExcludedItem{
			OpportunityID: e.Opportunity.ID.String(),
			Title:         e.Opportunity.TitleRaw,
			FailedChecks:  e.Eligibility.FailedChecks(),
			Reasons:       e.Eligibility.Reasons(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// explanation returns the breakdown for one posting.
func (h *Handler) explanation(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(w, r)
	if !ok {
		return
	}
	target, ok := parseID(w, r)
	if !ok {
		return
	}

	res, err := h.matcher.MatchForUser(r.Context(), id.UserID, 0)
	if err != nil {
		h.log.Error("building explanation", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not build the explanation")
		return
	}
	for _, m := range res.Matches {
		if m.Opportunity.ID == target {
			writeJSON(w, http.StatusOK, toFitView(m.Fit, res.ProfileVersion))
			return
		}
	}
	// Not in the ranked set. It may have been excluded, which is a different
	// answer and has its own endpoint, so say which.
	for _, e := range res.Excluded {
		if e.Opportunity.ID == target {
			writeJSON(w, http.StatusOK, ExcludedItem{
				OpportunityID: e.Opportunity.ID.String(),
				Title:         e.Opportunity.TitleRaw,
				FailedChecks:  e.Eligibility.FailedChecks(),
				Reasons:       e.Eligibility.Reasons(),
			})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "this posting is not in your candidate set")
}

// actRequest is the body for a dismissal.
type actRequest struct {
	Reason string `json:"reason"`
}

// act builds a handler for one engagement verb.
func (h *Handler) act(event string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := identity(w, r)
		if !ok {
			return
		}
		target, ok := parseID(w, r)
		if !ok {
			return
		}

		var reason string
		if event == EventDismissed {
			var req actRequest
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				writeErr(w, http.StatusBadRequest, "a dismissal needs a reason")
				return
			}
			reason = req.Reason
		}

		// The decision context is looked up rather than trusted from the client:
		// a client-supplied score would let the decision record say whatever the
		// client wanted, which defeats the purpose of keeping one.
		decision := h.decisionFor(r, id.UserID, target)

		var err error
		switch event {
		case EventSaved:
			err = h.svc.Save(r.Context(), id.UserID, target, decision)
		case EventUnsaved:
			err = h.svc.Unsave(r.Context(), id.UserID, target)
		case EventApplied:
			err = h.svc.Apply(r.Context(), id.UserID, target, decision)
		case EventOpened:
			err = h.svc.Open(r.Context(), id.UserID, target, decision)
		case EventDismissed:
			err = h.svc.Dismiss(r.Context(), id.UserID, target, reason, decision)
		}

		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, ErrReasonRequired), errors.Is(err, ErrUnknownReason):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrNotFound):
			writeErr(w, http.StatusNotFound, "no such posting")
		default:
			h.log.Error("recording engagement",
				"user_id", id.UserID.String(), "event", event, "err", err)
			writeErr(w, http.StatusInternalServerError, "could not record that")
		}
	}
}

// decisionFor reads the ranking context for one posting from the score cache.
//
// From the cache, not by re-running the matcher: the record must say what was
// SHOWN, and recomputing would both risk a different answer and turn a single
// insert into a full retrieval and scoring pass on a request the user is waiting
// on.
//
// Best-effort on purpose. Nil means the action did not come from a ranked surface
// — a direct link, or a posting whose cached score has since been invalidated by
// a version change. Recording zeros instead would fabricate a ranking that never
// happened.
func (h *Handler) decisionFor(r *http.Request, userID, oppID pgtype.UUID) *Decision {
	d, err := h.svc.CachedDecision(r.Context(), userID, oppID)
	if err != nil {
		h.log.Warn("no cached decision for this action; recording the event without one",
			"user_id", userID.String(), "opportunity_id", oppID.String())
		return nil
	}
	return d
}

func (h *Handler) listSaved(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(w, r)
	if !ok {
		return
	}
	var before *time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "before must be an RFC3339 timestamp")
			return
		}
		before = &t
	}
	rows, err := h.svc.ListSaved(r.Context(), id.UserID, before, int32(clampSize(r.URL.Query().Get("limit"))))
	if err != nil {
		h.log.Error("listing saved", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not list saved postings")
		return
	}
	// The posting, for the same reason the feed needs it (hard rule 27): a list of
	// ids is not a list a person can read. This was ids and timestamps only, so a
	// saved-items screen had no title, no company, no apply link and no way to
	// say whether the role is still open — which is the one thing a saved list is
	// for, because a save is a decision revisited days later.
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.OpportunityID)
	}
	postings, perr := h.opps.SummariesByID(r.Context(), ids)
	if perr != nil {
		h.log.Error("loading saved postings", "user_id", id.UserID.String(), "err", perr)
		writeErr(w, http.StatusInternalServerError, "could not list saved postings")
		return
	}

	type savedItem struct {
		OpportunityID string    `json:"opportunity_id"`
		SavedAt       time.Time `json:"saved_at"`
		// Posting is a value, not a pointer: an entry we cannot describe is
		// dropped rather than sent half-populated.
		Posting opportunity.Summary `json:"posting"`
	}
	out := struct {
		Items []savedItem `json:"items"`
		// NextBefore is the keyset cursor. Offset pagination drifts when rows are
		// inserted between pages, which for a save list means silently skipping
		// something the user saved.
		NextBefore *time.Time `json:"next_before"`
		// Closed counts saves whose posting is gone — closed, merged away, or
		// purged with its source. Reported rather than hidden: a shrinking saved
		// list with no explanation looks like data loss.
		Closed int `json:"closed_since_saved"`
	}{Items: make([]savedItem, 0, len(rows))}

	for _, row := range rows {
		posting, ok := postings[row.OpportunityID.String()]
		if !ok {
			out.Closed++
			continue
		}
		out.Items = append(out.Items, savedItem{
			OpportunityID: row.OpportunityID.String(),
			SavedAt:       row.SavedAt.Time,
			Posting:       posting,
		})
	}
	// The cursor comes from the last row READ, not the last row shown: paging on
	// a filtered timestamp would re-fetch the dropped entries forever.
	if len(rows) > 0 {
		last := rows[len(rows)-1].SavedAt.Time
		out.NextBefore = &last
	}
	writeJSON(w, http.StatusOK, out)
}

// dismissReasons exposes the closed set so a client never invents one.
func (h *Handler) dismissReasons(w http.ResponseWriter, _ *http.Request) {
	type reason struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	labels := map[string]string{
		ReasonWrongStack:     "Wrong technology stack",
		ReasonWrongLevel:     "Wrong seniority level",
		ReasonWrongLocation:  "Wrong location or work mode",
		ReasonCompTooLow:     "Compensation too low",
		ReasonAlreadyApplied: "I already applied",
		ReasonNotInterested:  "Not interested",
	}
	out := struct {
		Reasons []reason `json:"reasons"`
	}{}
	for _, v := range DismissReasons {
		out.Reasons = append(out.Reasons, reason{v, labels[v]})
	}
	writeJSON(w, http.StatusOK, out)
}

// ------------------------------------------------------------------ mapping

func toFeedItem(
	m matching.Match, posting opportunity.Summary,
	state map[string]State, profileVersion int32,
) FeedItem {
	st := state[m.Opportunity.ID.String()]
	return FeedItem{
		OpportunityID: m.Opportunity.ID.String(),
		Title:         m.Opportunity.TitleRaw,
		Posting:       posting,
		Fit:           toFitView(m.Fit, profileVersion),
		State: StateView{
			Saved: st.Saved, Applied: st.Applied,
			AppliedAt: st.AppliedAt, Dismissed: st.Dismissed,
		},
		Channels: m.Channels,
	}
}

func toFitView(f matching.Fit, profileVersion int32) FitView {
	out := FitView{
		Band:      string(f.Band()),
		Points:    f.Score,
		MaxPoints: f.MaxPossible,
		Summary:   f.Summary(),
		Factors:   make([]FactorView, 0, len(f.Factors)),
		Versions: VersionView{
			Weights: f.WeightsVersion, Embedding: embeddingVersion, Profile: profileVersion,
		},
	}
	for _, fs := range f.Factors {
		out.Factors = append(out.Factors, FactorView{
			Factor: fs.Factor, Points: round1(fs.Contribution),
			MaxPoints: round1(fs.MaxContribution), Scored: fs.Available, Reason: fs.Reason,
		})
	}
	return out
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// ------------------------------------------------------------------ plumbing

func identity(w http.ResponseWriter, r *http.Request) (*auth.Identity, bool) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return id, true
}

func parseID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed opportunity id")
		return id, false
	}
	return id, true
}

func clampSize(raw string) int {
	if raw == "" {
		return defaultFeedSize
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultFeedSize
	}
	if n > maxFeedSize {
		return maxFeedSize
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
