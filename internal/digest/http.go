package digest

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/auth"
	"github.com/Xubair001/devsignal/internal/store"
)

// Handler serves the user's own notification settings.
//
// Consent is a separate call from the rest of the settings, deliberately.
// Folding it into a settings save would mean an unrelated preference change
// looks like a consent event in the record — and "consent you cannot evidence is
// consent you do not have" cuts both ways: a consent you cannot distinguish from
// a timezone edit is not evidence either.
type Handler struct {
	q   *store.Queries
	log Logger
}

// NewHandler builds one.
func NewHandler(pool *pgxpool.Pool, log Logger) *Handler {
	return &Handler{q: store.New(pool), log: log}
}

// Routes mounts the settings surface.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.get)
	r.Put("/", h.put)
	r.Post("/consent", h.consent)
	r.Delete("/consent", h.withdraw)
	r.Get("/history", h.history)
	return r
}

type settingsResponse struct {
	Timezone      string `json:"timezone"`
	QuietStart    int16  `json:"quiet_start"`
	QuietEnd      int16  `json:"quiet_end"`
	DigestEnabled bool   `json:"digest_enabled"`
	MaxPerWeek    int16  `json:"max_per_week"`
	MinBand       string `json:"min_band"`
	SendWhenEmpty bool   `json:"send_when_empty"`
	// Consent state, reported as three distinct facts rather than one boolean:
	// never given, given, and withdrawn are different situations and only the
	// middle one may be mailed.
	ConsentAt      *time.Time `json:"consent_at"`
	ConsentWording *string    `json:"consent_wording_version"`
	WithdrawnAt    *time.Time `json:"consent_withdrawn_at"`
	// Configured is false when no settings row exists at all. Distinct from a row
	// with digest_enabled=false: one is "never asked", the other is "said no".
	Configured bool `json:"configured"`
}

type settingsRequest struct {
	Timezone      string `json:"timezone"`
	QuietStart    int16  `json:"quiet_start"`
	QuietEnd      int16  `json:"quiet_end"`
	DigestEnabled bool   `json:"digest_enabled"`
	MaxPerWeek    int16  `json:"max_per_week"`
	MinBand       string `json:"min_band"`
	SendWhenEmpty bool   `json:"send_when_empty"`
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	row, err := h.q.GetNotificationSetting(r.Context(), id.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Defaults, marked as not configured. Returning 404 would make the client
		// invent its own defaults, and two sets of defaults is one too many.
		writeJSON(w, http.StatusOK, settingsResponse{
			Timezone: "UTC", QuietStart: 21, QuietEnd: 8,
			MaxPerWeek: 5, MinBand: BarStrong, Configured: false,
		})
		return
	}
	if err != nil {
		h.log.Error("loading notification settings", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not load settings")
		return
	}
	writeJSON(w, http.StatusOK, toSettings(row))
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req settingsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body")
		return
	}

	// Validated here rather than trusted to the CHECK constraints, so the user
	// gets a sentence instead of a 500 with a constraint name in it.
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeErr(w, http.StatusBadRequest, "timezone must be an IANA zone name, for example Europe/London")
		return
	}
	if req.QuietStart < 0 || req.QuietStart > 23 || req.QuietEnd < 0 || req.QuietEnd > 23 {
		writeErr(w, http.StatusBadRequest, "quiet hours must be local hours between 0 and 23")
		return
	}
	if req.MaxPerWeek < 0 || req.MaxPerWeek > 7 {
		writeErr(w, http.StatusBadRequest, "max_per_week must be between 0 and 7")
		return
	}
	if req.MinBand != BarStrong && req.MinBand != BarWorthALook {
		writeErr(w, http.StatusBadRequest, `min_band must be "strong" or "worth_a_look"`)
		return
	}

	row, err := h.q.UpsertNotificationSetting(r.Context(),
		store.UpsertNotificationSettingParams{
			UserID: id.UserID, TenantID: id.TenantID, Timezone: req.Timezone,
			QuietStart: req.QuietStart, QuietEnd: req.QuietEnd,
			DigestEnabled: req.DigestEnabled, MaxPerWeek: req.MaxPerWeek,
			MinBand: req.MinBand, SendWhenEmpty: req.SendWhenEmpty,
		})
	if err != nil {
		h.log.Error("saving notification settings", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	writeJSON(w, http.StatusOK, toSettings(row))
}

type consentRequest struct {
	WordingVersion string `json:"wording_version"`
}

func (h *Handler) consent(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req consentRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.WordingVersion == "" {
		// The wording version is required, not defaulted. Recording that someone
		// consented without recording WHAT they agreed to is not evidence of
		// consent, and a default here would manufacture exactly that.
		writeErr(w, http.StatusBadRequest, "wording_version is required: consent must record what was agreed to")
		return
	}

	// A settings row must exist before consent can attach to it.
	if _, err := h.q.GetNotificationSetting(r.Context(), id.UserID); errors.Is(err, pgx.ErrNoRows) {
		if _, uerr := h.q.UpsertNotificationSetting(r.Context(),
			store.UpsertNotificationSettingParams{
				UserID: id.UserID, TenantID: id.TenantID, Timezone: "UTC",
				QuietStart: 21, QuietEnd: 8, DigestEnabled: true,
				MaxPerWeek: 5, MinBand: BarStrong,
			}); uerr != nil {
			h.log.Error("creating settings for consent", "err", uerr)
			writeErr(w, http.StatusInternalServerError, "could not record consent")
			return
		}
	}

	if _, err := h.q.RecordDigestConsent(r.Context(), store.RecordDigestConsentParams{
		UserID:      id.UserID,
		ConsentedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		// The IP is deliberately NOT taken from a header here. X-Forwarded-For is
		// spoofable and the router does not run RealIP for exactly that reason; an
		// evidenced IP has to come from a trusted-proxy config, and recording a
		// forgeable one as evidence is worse than recording none.
		WordingVersion: &req.WordingVersion,
	}); err != nil {
		h.log.Error("recording consent", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not record consent")
		return
	}
	h.get(w, r)
}

func (h *Handler) withdraw(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	// The consent record is KEPT. Proving we had consent, and that it was
	// withdrawn on a date, is the point of recording it — erasing it on
	// withdrawal would destroy the evidence that the withdrawal was honoured.
	if _, err := h.q.WithdrawDigestConsent(r.Context(), store.WithdrawDigestConsentParams{
		UserID:      id.UserID,
		WithdrawnAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		h.log.Error("withdrawing consent", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not withdraw consent")
		return
	}
	h.get(w, r)
}

type historyRow struct {
	LocalDate string     `json:"local_date"`
	Outcome   string     `json:"outcome"`
	Reason    *string    `json:"reason"`
	ItemCount int32      `json:"item_count"`
	SentAt    *time.Time `json:"sent_at"`
	Attempts  int32      `json:"attempts"`
}

// history answers "why did I not get a digest", which is the question the
// outcome column exists for.
func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	rows, err := h.q.ListDigestSends(r.Context(), store.ListDigestSendsParams{
		UserID: id.UserID, MaxRows: 30,
	})
	if err != nil {
		h.log.Error("loading digest history", "user_id", id.UserID.String(), "err", err)
		writeErr(w, http.StatusInternalServerError, "could not load history")
		return
	}
	out := make([]historyRow, 0, len(rows))
	for _, r := range rows {
		hr := historyRow{
			LocalDate: r.LocalDate.Time.Format("2006-01-02"),
			Outcome:   r.Outcome, Reason: r.Reason,
			ItemCount: r.ItemCount, Attempts: r.Attempts,
		}
		if r.SentAt.Valid {
			t := r.SentAt.Time
			hr.SentAt = &t
		}
		out = append(out, hr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sends": out})
}

func toSettings(row store.NotificationSetting) settingsResponse {
	s := settingsResponse{
		Timezone: row.Timezone, QuietStart: row.QuietStart, QuietEnd: row.QuietEnd,
		DigestEnabled: row.DigestEnabled, MaxPerWeek: row.MaxPerWeek,
		MinBand: row.MinBand, SendWhenEmpty: row.SendWhenEmpty,
		ConsentWording: row.DigestConsentWordingVersion, Configured: true,
	}
	if row.DigestConsentAt.Valid {
		t := row.DigestConsentAt.Time
		s.ConsentAt = &t
	}
	if row.DigestConsentWithdrawnAt.Valid {
		t := row.DigestConsentWithdrawnAt.Time
		s.WithdrawnAt = &t
	}
	return s
}

// writeErr keeps the error envelope in one place. Seventeen inline
// map[string]string literals is seventeen chances to name the key differently.
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
