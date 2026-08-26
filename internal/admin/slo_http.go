package admin

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/slo"
)

// SLOHandler serves the objective report on the operations surface.
//
// Separate from the metrics backend on purpose. Latency and availability come
// from OpenTelemetry, because a request that already returned left no row behind;
// everything here is a property of the corpus, which Postgres can answer exactly.
// The report says which is which rather than blending them into one number.
type SLOHandler struct {
	ev  *slo.Evaluator
	log Logger
}

// NewSLOHandler builds it.
func NewSLOHandler(pool *pgxpool.Pool, log Logger) *SLOHandler {
	return &SLOHandler{ev: slo.NewEvaluator(pool), log: log}
}

type sloResult struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Detail      string  `json:"detail"`
	Target      float64 `json:"target"`
	Kind        string  `json:"kind"`
	// Observed is null when there is nothing to report, which is different from
	// zero and must stay distinguishable.
	Observed *float64 `json:"observed"`
	Sample   int64    `json:"sample"`
	// BudgetRemaining and BurnRate are only meaningful for ratio objectives.
	BudgetRemaining *float64 `json:"budget_remaining"`
	BurnRate        *float64 `json:"burn_rate"`
	// AlertSeverity is what an operator should do about the burn rate now.
	AlertSeverity string `json:"alert_severity"`
}

type sloResponse struct {
	MeasuredAt time.Time   `json:"measured_at"`
	Results    []sloResult `json:"results"`
	Summary    struct {
		Breached int `json:"breached"`
		AtRisk   int `json:"at_risk"`
		// Unmeasurable is surfaced in the summary because it is the number most
		// worth reading: an all-green board with five unmeasurable objectives is
		// not the same as an all-green board.
		Unmeasurable int  `json:"unmeasurable"`
		Healthy      bool `json:"healthy"`
	} `json:"summary"`
	// LivenessVerification is reported next to the objectives but is NOT one of
	// them. "How recently we checked" and "is this role genuinely open" are
	// different claims, and only the first is something we can know.
	LivenessVerification *livenessView `json:"liveness_verification"`
	PipelineStates       []stateView   `json:"pipeline_states"`
}

type livenessView struct {
	Shown            int64   `json:"shown"`
	CheckedRecently  int64   `json:"checked_recently"`
	Fraction         float64 `json:"fraction"`
	ThresholdHours   float64 `json:"threshold_hours"`
	OldestCheckHours float64 `json:"oldest_check_hours"`
	Note             string  `json:"note"`
}

type stateView struct {
	State   string    `json:"state"`
	Records int64     `json:"records"`
	Oldest  time.Time `json:"oldest_entered"`
	// Stranded flags a non-terminal state whose oldest record is past the backlog
	// threshold. A large count that is moving is healthy; a small one that is not
	// is an incident.
	Stranded bool `json:"stranded"`
}

// Report serves GET /internal/admin/slo.
func (h *SLOHandler) Report(w http.ResponseWriter, r *http.Request) {
	rep, err := h.ev.Evaluate(r.Context())
	if err != nil {
		h.log.Error("evaluating objectives", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not evaluate objectives")
		return
	}

	out := sloResponse{MeasuredAt: rep.MeasuredAt, Results: make([]sloResult, 0, len(rep.Results))}
	for _, res := range rep.Results {
		item := sloResult{
			ID: res.Objective.ID, Description: res.Objective.Description,
			Status: string(res.Status), Detail: res.Detail,
			Target: res.Objective.Target, Kind: string(res.Objective.Kind),
			Observed: res.Observed, Sample: res.Sample,
			BudgetRemaining: res.BudgetRemaining, BurnRate: res.BurnRate,
			AlertSeverity: string(slo.SeverityNone),
		}
		if res.BurnRate != nil {
			item.AlertSeverity = string(slo.Alert(*res.BurnRate, res.Objective.Window))
		}
		out.Results = append(out.Results, item)
	}
	out.Summary.Breached = len(rep.Breached())
	out.Summary.AtRisk = len(rep.AtRisk())
	out.Summary.Unmeasurable = len(rep.Unmeasurable())
	out.Summary.Healthy = rep.Healthy()

	if lf, err := h.ev.LivenessFreshness(r.Context()); err == nil {
		out.LivenessVerification = &livenessView{
			Shown: lf.Shown, CheckedRecently: lf.CheckedRecently,
			Fraction: lf.Fraction(), ThresholdHours: lf.Threshold.Hours(),
			OldestCheckHours: lf.OldestCheck.Hours(),
			Note: "verification recency, not the liveness accuracy objective: " +
				"whether a role is genuinely open needs the employer's answer",
		}
	}

	if states, err := h.ev.PipelineStates(r.Context()); err == nil {
		for _, s := range states {
			terminal := s.State == "ready" || s.State == "failed_permanent"
			out.PipelineStates = append(out.PipelineStates, stateView{
				State: s.State, Records: s.Records, Oldest: s.Oldest,
				Stranded: !terminal && time.Since(s.Oldest) > slo.WindowHour,
			})
		}
	}

	writeJSON(w, http.StatusOK, out)
}
