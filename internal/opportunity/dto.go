// Package opportunity serves the read side of the corpus.
//
// The response types here are the enforcement point for blueprint §3: nothing
// renders that cannot be derived from something we observed. Internal fields —
// simhash, block_key, content_hash, every version column, ghost_risk_score —
// are deliberately absent, and an explicit DTO is what makes that a decision
// rather than an accident.
package opportunity

import (
	"time"

	"github.com/Xubair001/devsignal/internal/ghostrisk"
)

// Money is serialized as parts, never a formatted string and never a float.
// Formatting depends on the viewer's locale, which the API does not know.
type Money struct {
	MinMinor    int64  `json:"min_minor"`
	MaxMinor    *int64 `json:"max_minor"`
	Currency    string `json:"currency"`
	Period      string `json:"period"`
	IsEstimated bool   `json:"is_estimated"`
}

type Company struct {
	Name string `json:"name"`
	// DomainConfirmed distinguishes a real registrable domain from the synthetic
	// identity derived from an ATS board token. Surfaced because it changes how
	// much company-level information can be trusted.
	DomainConfirmed bool `json:"domain_confirmed"`
}

type Location struct {
	Country  *string  `json:"country"`
	City     *string  `json:"city"`
	WorkMode *string  `json:"work_mode"`
	GeoScope []string `json:"geo_scope"`
}

type Role struct {
	Family *string `json:"family"`
	// Seniority is a label, not the internal ordinal. The ordinal is an
	// implementation detail of scoring.
	Seniority    *string `json:"seniority"`
	IsManagement bool    `json:"is_management"`
}

// Liveness is the product's core trust claim, so it is a first-class object
// rather than a timestamp the client has to interpret.
type Liveness struct {
	VerifiedOpen bool       `json:"verified_open"`
	CheckedAt    *time.Time `json:"checked_at"`
	// FirstSeenAt is OURS and is the only trustworthy age signal.
	FirstSeenAt time.Time `json:"first_seen_at"`
	DaysOpen    int       `json:"days_open"`
	// SourceClaimsPostedAt is THEIR claim. Returned for transparency, clearly
	// named so it is never mistaken for our observation, and never scored.
	SourceClaimsPostedAt *time.Time `json:"source_claims_posted_at"`
}

// Signals are observable facts only. There is deliberately no competitiveness
// estimate: we have no applicant counts, and one invented field discredits the
// honest ones next to it.
type Signals struct {
	GhostRisk        ghostrisk.Band     `json:"ghost_risk"`
	GhostRiskReasons []ghostrisk.Reason `json:"ghost_risk_reasons"`
	TimesRefreshed   int                `json:"times_refreshed"`
	SourcesSeenOn    int                `json:"sources_seen_on"`
	ApplyMethod      *string            `json:"apply_method"`
}

type Summary struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Company  Company  `json:"company"`
	Role     Role     `json:"role"`
	Location Location `json:"location"`
	// Salary is null when undisclosed. That is a real state, not a missing value
	// to default away: inventing a range is the error users forgive least.
	Salary          *Money   `json:"salary"`
	VisaSponsorship string   `json:"visa_sponsorship"`
	Language        *string  `json:"language"`
	ApplyURL        *string  `json:"apply_url"`
	Liveness        Liveness `json:"liveness"`
	Signals         Signals  `json:"signals"`
}

type Detail struct {
	Summary
	DescriptionHTML *string `json:"description_html"`
	// OpenSimilarRolesAtCompany is an observable competition proxy, not a
	// competitiveness score.
	OpenSimilarRolesAtCompany int `json:"open_similar_roles_at_company"`
}

type Page struct {
	Items []Summary `json:"items"`
	// NextCursor is opaque and empty at the end. Clients must not construct one.
	NextCursor string `json:"next_cursor,omitempty"`
}

// seniorityLabels maps the internal ordinal to what a user reads.
var seniorityLabels = map[int16]string{
	1: "intern", 2: "junior", 3: "mid", 4: "senior", 5: "staff", 6: "principal",
}

func seniorityLabel(ord *int16) *string {
	if ord == nil {
		return nil
	}
	if s, ok := seniorityLabels[*ord]; ok {
		return &s
	}
	return nil
}
