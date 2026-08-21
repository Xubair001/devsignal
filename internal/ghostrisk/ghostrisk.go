// Package ghostrisk scores how likely a posting is not a real, currently-open
// opening.
//
// Independent estimates put ghost listings at roughly 20-33% of online postings.
// A product whose promise is "these are the few worth your attention" cannot
// spend a fifth of that attention on roles nobody intends to fill, so this is a
// product feature rather than data hygiene (blueprint §16).
//
// Everything here is derived from what we OBSERVED, never from what the posting
// claims about itself. The output is a band plus the reasons behind it, because
// blueprint §3 forbids rendering a bare score the user cannot interrogate.
package ghostrisk

import "time"

// Band is what the user sees. A number would imply a precision we do not have.
type Band string

const (
	BandNormal   Band = "normal"
	BandElevated Band = "elevated"
	BandHigh     Band = "high"
)

// Signals are the observable inputs. Every field is something we watched happen.
type Signals struct {
	// FirstSeenAt is OURS. Never the source's claimed posting date, which boards
	// and employers refresh precisely to defeat age signals.
	FirstSeenAt time.Time
	// LastVerifiedAt is the last successful poll that observed this posting.
	LastVerifiedAt time.Time
	// RepostCount is how many times the source moved its own posted-at forward
	// while the content stayed byte-identical.
	RepostCount int
	// HasApplyMethod is false when we could not resolve anywhere to apply.
	HasApplyMethod bool
	// CompanyMedianDaysToClose is how long this company's postings usually stay
	// open. Zero means we do not have enough history yet, in which case age
	// contributes nothing — an unknown baseline must not manufacture suspicion.
	CompanyMedianDaysToClose int
}

// Reason is a single human-readable contribution. The UI renders these, not the
// score, so the user can judge the judgement.
type Reason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type Assessment struct {
	Band    Band     `json:"band"`
	Reasons []Reason `json:"reasons"`
}

// Thresholds, stated rather than buried. These are judgement calls, not
// measurements — there is no labelled ghost-job dataset to calibrate against,
// so they are deliberately conservative: the cost of wrongly flagging a real
// job is a user skipping a good opportunity.
const (
	// A single refresh is ordinary housekeeping. Repeated refreshes of identical
	// content are the pattern that distinguishes a perpetual listing.
	repostElevated = 2
	repostHigh     = 4

	// Absolute age only matters when we have no company baseline to compare to.
	staleDays     = 60
	veryStaleDays = 120

	// Multiples of the company's own median. Relative is better than absolute:
	// a 90-day posting is unremarkable at a company that always takes 90 days.
	medianMultipleElevated = 3
	medianMultipleHigh     = 6
)

// Assess evaluates the signals at a point in time. Pure: the clock is passed in
// so the result is reproducible and testable.
func Assess(s Signals, now time.Time) Assessment {
	var reasons []Reason
	score := 0

	daysOpen := int(now.Sub(s.FirstSeenAt).Hours() / 24)
	if daysOpen < 0 {
		daysOpen = 0
	}

	switch {
	case s.RepostCount >= repostHigh:
		// Weighted highest of any single signal, and enough on its own to reach
		// "high": repeatedly moving the date forward on byte-identical content is
		// directly observed and has no innocent explanation at this frequency.
		score += 3
		reasons = append(reasons, Reason{"repeated_refresh",
			plural(s.RepostCount, "the source has refreshed this listing's date %d time%s without the content changing")})
	case s.RepostCount >= repostElevated:
		score++
		reasons = append(reasons, Reason{"refreshed",
			plural(s.RepostCount, "the source has refreshed this listing's date %d time%s without the content changing")})
	}

	// Prefer the company's own baseline; fall back to absolutes only when we
	// genuinely have no history.
	if s.CompanyMedianDaysToClose > 0 {
		switch {
		case daysOpen >= s.CompanyMedianDaysToClose*medianMultipleHigh:
			score += 2
			reasons = append(reasons, Reason{"open_far_longer_than_usual",
				itoa2("open %d days; this company usually fills a role in about %d", daysOpen, s.CompanyMedianDaysToClose)})
		case daysOpen >= s.CompanyMedianDaysToClose*medianMultipleElevated:
			score++
			reasons = append(reasons, Reason{"open_longer_than_usual",
				itoa2("open %d days; this company usually fills a role in about %d", daysOpen, s.CompanyMedianDaysToClose)})
		}
	} else {
		// No company baseline. Absolute age is the weakest signal here — a long
		// hire at a company we have no history for is genuinely ambiguous — so it
		// is capped below the level that alone reaches "high". Wrongly flagging a
		// real job costs the user an opportunity.
		switch {
		case daysOpen >= veryStaleDays:
			score += 2
			reasons = append(reasons, Reason{"open_a_long_time",
				itoa1("open %d days, and we have no hiring-pace history for this company", daysOpen)})
		case daysOpen >= staleDays:
			score++
			reasons = append(reasons, Reason{"open_a_while",
				itoa1("open %d days", daysOpen)})
		}
	}

	if !s.HasApplyMethod {
		score++
		reasons = append(reasons, Reason{"no_apply_route",
			"we could not resolve anywhere to actually apply"})
	}

	// 3 is reachable by the repost signal alone, or by two weaker signals
	// corroborating each other. No single weak signal reaches "high".
	band := BandNormal
	switch {
	case score >= 3:
		band = BandHigh
	case score >= 1:
		band = BandElevated
	}
	return Assessment{Band: band, Reasons: reasons}
}

// DaysOpen is exported because the API surfaces it directly: an observed age is
// a fact the user can check, unlike a score.
func DaysOpen(firstSeen, now time.Time) int {
	d := int(now.Sub(firstSeen).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}
