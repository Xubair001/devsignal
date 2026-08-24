// Package matching is stage 0 and stage 1 of the matcher: the eligibility gate,
// the fit score, and the explanation that is the model rather than a story about
// it.
//
// Two rules shape every line here, and both come from the blueprint's audit of
// how recommenders lose trust:
//
//   - An eligibility failure is EXPLAINED, never scored down. A role requiring
//     work authorization the user does not have is not a 40% match, it is not a
//     match. Scoring it down leaves it in the feed at a lower rank, which wastes
//     the user's attention and teaches them the ranking is noise.
//   - No score shown to a user may depend on the current time. fit_score is a
//     pure function of (profile_version, opportunity_version, weights_version,
//     embedding_version). Recency and urgency live in priority_score, which
//     orders the feed and is never displayed as a match.
package matching

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Xubair001/devsignal/internal/store"
)

// Eligibility check names. These are shown to users and read by operators, so
// they are part of the contract rather than debug strings.
const (
	CheckLiveness       = "liveness"
	CheckWorkAuth       = "work_authorization"
	CheckGeography      = "geography"
	CheckTimezone       = "timezone"
	CheckEmploymentType = "employment_type"
	CheckLanguage       = "language"
	CheckSalaryFloor    = "salary_floor"
)

// Eligibility is the stage-0 verdict.
type Eligibility struct {
	Eligible bool
	// Failed lists every check that failed, not just the first. A posting
	// excluded on both geography and pay should say both — telling a user only
	// half the reason invites them to "fix" the wrong thing.
	Failed []Failure
}

// Failure is one specific reason, in terms the user can act on.
type Failure struct {
	Check string
	// Reason is written for the person excluded by it, not for a log line.
	Reason string
}

// Reasons renders the failures for display.
func (e Eligibility) Reasons() []string {
	out := make([]string, 0, len(e.Failed))
	for _, f := range e.Failed {
		out = append(out, f.Reason)
	}
	return out
}

// FailedChecks returns just the check names, for the stored audit row.
func (e Eligibility) FailedChecks() []string {
	out := make([]string, 0, len(e.Failed))
	for _, f := range e.Failed {
		out = append(out, f.Check)
	}
	return out
}

// Candidate is everything stage 0 and stage 1 need about one posting.
//
// A struct rather than the raw store row because the two stages must be pure
// functions of their inputs — that is what makes a score reproducible and a
// golden test possible. Nothing here reads a clock or a database.
type Candidate struct {
	Opportunity store.Opportunity
	// RequiredSkills and PreferredSkills are skill IDs from extraction. Empty
	// means extraction has not run or found none, which is NOT the same as a
	// posting requiring nothing — see fit.go, where an unavailable factor
	// renormalizes rather than scoring zero.
	RequiredSkills  []string
	PreferredSkills []string
	// SemanticSimilarity is cosine similarity in [-1,1] between the profile and
	// posting vectors, or nil when either vector is missing.
	SemanticSimilarity *float64
}

// Profile is the right-hand side, reduced to what matching reads.
type Profile struct {
	Profile store.Profile
	// Skills the user claims, as skill IDs.
	Skills []string
	// WorkAuthCountries are the countries the user may already work in without
	// sponsorship, from profile.work_authorization. Empty means unstated, which
	// must not exclude anything.
	WorkAuthCountries []string
	// NeedsSponsorship is true only when the user said so. Unknown is not true:
	// assuming a user needs sponsorship would silently hide most of the corpus.
	NeedsSponsorship bool
}

// CheckEligibility runs stage 0.
//
// Every check is written so that MISSING data passes. That direction is
// deliberate and it is the single most consequential decision in this file: most
// postings do not state employment type, language, salary or timezone, so
// treating silence as a mismatch would exclude most of a real corpus and present
// it to the user as "nothing matches you".
func CheckEligibility(p Profile, c Candidate) Eligibility {
	var failed []Failure
	o := c.Opportunity

	// Liveness first. It is the one check about the posting's own validity rather
	// than the pairing, and the product's central claim is that what we show is
	// open — a closed posting is not a weak match, it is not a posting.
	if o.ClosedAt.Valid {
		failed = append(failed, Failure{CheckLiveness,
			"this role has closed since it was posted"})
	}
	if o.MergedInto.Valid {
		failed = append(failed, Failure{CheckLiveness,
			"this posting was merged into another listing for the same role"})
	}

	// Work authorization. Only excludes when the employer said "no sponsorship"
	// AND the user needs it AND the user is not already authorized there.
	// visa_sponsorship is 'unknown' on almost every posting, and unknown passes.
	if o.VisaSponsorship == "no" && p.NeedsSponsorship {
		country := derefStr(o.LocationCountry)
		if country != "" && !containsFold(p.WorkAuthCountries, country) {
			failed = append(failed, Failure{CheckWorkAuth, fmt.Sprintf(
				"this employer does not sponsor visas and you have not said you can already work in %s",
				strings.ToUpper(country))})
		}
	}

	// Geography. A remote posting is exempt: its stated country is a formality,
	// and excluding it would hide exactly the roles a location-constrained user
	// most wants.
	if targets := p.Profile.TargetCountries; len(targets) > 0 && o.WorkMode != nil &&
		*o.WorkMode != "remote" {
		if country := derefStr(o.LocationCountry); country != "" &&
			!containsFold(targets, country) {
			failed = append(failed, Failure{CheckGeography, fmt.Sprintf(
				"this role is in %s, which is not one of the countries you are targeting",
				strings.ToUpper(country))})
		}
	}

	// Employment type. Unstated passes.
	if want := p.Profile.TargetEmploymentTypes; len(want) > 0 && o.EmploymentType != nil &&
		*o.EmploymentType != "" {
		if !containsFold(want, *o.EmploymentType) {
			failed = append(failed, Failure{CheckEmploymentType, fmt.Sprintf(
				"this is a %s role and you are looking for %s",
				strings.ReplaceAll(*o.EmploymentType, "_", " "),
				strings.ReplaceAll(strings.Join(want, " or "), "_", " "))})
		}
	}

	// Language. Unstated passes.
	if langs := p.Profile.Languages; len(langs) > 0 && o.Language != nil && *o.Language != "" {
		if !containsFold(langs, *o.Language) {
			failed = append(failed, Failure{CheckLanguage, fmt.Sprintf(
				"this posting is in %s, which is not a language you listed",
				strings.ToUpper(*o.Language))})
		}
	}

	// Hard salary floor, only if the user set one and the employer disclosed a
	// figure. An ESTIMATED salary must never gate: excluding a real job because
	// of a number we guessed is exactly what hard rule 3 forbids.
	if floor := p.Profile.MinSalaryMinor; floor != nil && *floor > 0 && !o.SalaryIsEstimated {
		if top := salaryCeiling(o); top != nil && comparableCurrency(p.Profile, o) && *top < *floor {
			failed = append(failed, Failure{CheckSalaryFloor,
				"the disclosed pay for this role is below the minimum you set"})
		}
	}

	// Timezone feasibility, for remote roles that state a band. Only excludes
	// when the posting states a range and the user's countries put them outside
	// it; without a stated range there is nothing to check against.
	if tzFailure(p, o) {
		failed = append(failed, Failure{CheckTimezone,
			"this remote role requires working hours in a timezone band that does not overlap yours"})
	}

	return Eligibility{Eligible: len(failed) == 0, Failed: failed}
}

// salaryCeiling is the top of the disclosed range, or the single figure when only
// one was given. nil when nothing was disclosed.
func salaryCeiling(o store.Opportunity) *int64 {
	if o.SalaryMaxMinor != nil {
		return o.SalaryMaxMinor
	}
	return o.SalaryMinMinor
}

// comparableCurrency reports whether the two figures are in the same currency and
// period. Comparing 60000 EUR/year against a 5000 USD/month floor is arithmetic
// on unlike things, and the honest answer is to not gate at all rather than to
// convert with an fx rate we did not record (hard rule 1).
func comparableCurrency(p store.Profile, o store.Opportunity) bool {
	if p.SalaryCurrency == nil || o.SalaryCurrency == nil {
		return false
	}
	if !strings.EqualFold(*p.SalaryCurrency, *o.SalaryCurrency) {
		return false
	}
	if p.SalaryPeriod == nil || o.SalaryPeriod == nil {
		return false
	}
	return strings.EqualFold(*p.SalaryPeriod, *o.SalaryPeriod)
}

// tzFailure is true only when the posting states a timezone band and none of the
// user's target countries falls inside it.
//
// Deliberately coarse: a country maps to a representative UTC offset, which is
// wrong for countries spanning several zones. Coarse in the PASSING direction —
// any overlap passes — because a false exclusion is invisible to the user and a
// false inclusion is merely a slightly worse ranking.
func tzFailure(p Profile, o store.Opportunity) bool {
	if o.RemoteTimezoneMin == nil || o.RemoteTimezoneMax == nil {
		return false
	}
	countries := p.Profile.TargetCountries
	if len(countries) == 0 {
		return false
	}
	lo, hi := int(*o.RemoteTimezoneMin), int(*o.RemoteTimezoneMax)
	for _, c := range countries {
		off, known := countryOffset(strings.ToUpper(strings.TrimSpace(c)))
		if !known {
			// An unknown country cannot be excluded on a guess.
			return false
		}
		if off >= lo && off <= hi {
			return false
		}
	}
	return true
}

// countryOffset is a representative UTC offset per country.
//
// Only the countries the corpus actually contains in volume, and unknown means
// "do not gate". A fuller table belongs in normalization with real timezone data;
// this exists so the check can be correct where it fires and silent elsewhere.
var countryOffsets = map[string]int{
	"US": -6, "CA": -5, "MX": -6, "BR": -3, "AR": -3, "CL": -4,
	"GB": 0, "IE": 0, "PT": 0, "IS": 0,
	"DE": 1, "FR": 1, "NL": 1, "BE": 1, "ES": 1, "IT": 1, "PL": 1, "SE": 1,
	"NO": 1, "DK": 1, "AT": 1, "CH": 1, "CZ": 1, "HU": 1, "RS": 1, "HR": 1,
	"FI": 2, "GR": 2, "RO": 2, "BG": 2, "UA": 2, "IL": 2, "ZA": 2, "EG": 2,
	"TR": 3, "KE": 3, "SA": 3, "AE": 4, "PK": 5, "IN": 5, "BD": 6,
	"TH": 7, "VN": 7, "ID": 7, "SG": 8, "MY": 8, "CN": 8, "PH": 8, "HK": 8,
	"JP": 9, "KR": 9, "AU": 10, "NZ": 12,
}

func countryOffset(code string) (int, bool) {
	off, ok := countryOffsets[code]
	return off, ok
}

func containsFold(haystack []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	return slices.ContainsFunc(haystack, func(h string) bool {
		return strings.EqualFold(strings.TrimSpace(h), needle)
	})
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
