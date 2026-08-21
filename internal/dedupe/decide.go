package dedupe

import "strings"

// MaxHamming is the SimHash threshold, calibrated against measurement rather
// than intuition (see TestSimHashCalibration).
//
// The blueprint suggested 3. That is empirically wrong for 64-bit SimHash over
// job-description-length text: a single word change already moves the signature
// 4-6 bits, so a threshold of 3 would only ever match byte-identical text —
// which content_hash catches for free, making the whole SimHash stage dead code.
//
// Measured (TestSimHashCalibration), using the negative case that actually
// occurs — two postings from ONE company sharing a long boilerplate intro:
//
//	same posting, lightly edited ......  4-6 bits
//	different role, shared boilerplate . 13 bits
//	different role, no shared intro .... 38 bits
//
// 9 is the midpoint of that 6..13 gap. The gap is narrow, and that narrowness is
// itself the finding: text similarity is a weak signal for job postings because
// boilerplate dominates. It is therefore never used alone — titlesAgree and
// MinTextForFuzzy both gate it independently.
const MaxHamming = 9

// Reason records WHY two postings were judged the same, so a merge can be
// reviewed and reversed. Ordered by strength.
type Reason string

const (
	ReasonExactATS    Reason = "exact_ats"    // same ATS and job id: conclusive
	ReasonContentHash Reason = "content_hash" // byte-identical meaningful content
	ReasonApplyURL    Reason = "apply_url"    // same application endpoint
	ReasonSimHash     Reason = "simhash"      // near-identical text; probabilistic
	ReasonNone        Reason = ""
)

// MinTextForFuzzy is the shortest hashed text we will trust for APPROXIMATE
// SimHash matching.
//
// Below this, a signature is not discriminating: a handful of shingles means one
// appended sentence can look like the same document, and two genuinely different
// short descriptions can land within any usable threshold. Real job descriptions
// run to thousands of characters, so this only excludes stubs.
//
// An EXACT signature match (distance 0) is exempt: identical text with agreeing
// titles is evidence regardless of length.
const MinTextForFuzzy = 400

// MinTitleTokensForFuzzy is how specific a title must be before text similarity
// is allowed to merge on it.
//
// A title of just "Engineer" carries no discriminating information: at one
// company it could be any number of genuinely different jobs. The description
// would have to carry all the signal, and it cannot — boilerplate can be 90% of
// a posting, so two unrelated roles land within any usable Hamming threshold.
// Requiring three distinctive tokens ("senior backend engineer") means the title
// contributes real evidence before the weakest signal is trusted.
const MinTitleTokensForFuzzy = 3

// MinAutoMergeConfidence is the floor for applying a merge without a human.
//
// Below it the pair is queued for review instead. A SimHash verdict at the far
// edge of MaxHamming scores 0.60, and observed on real data that band produced a
// pair where one side had no parsed geography at all — plausible, but not
// evidence worth acting on unattended.
const MinAutoMergeConfidence = 0.75

// Candidate is the minimum needed to judge a pair.
type Candidate struct {
	CompanyID   string
	ATSType     string
	ATSJobID    string
	ApplyURL    string
	ContentHash []byte
	SimHash     uint64
	// GeoScope is the sorted set of countries the posting is open to. Two
	// postings with identical text but different scope are DIFFERENT
	// opportunities — see geoConflict.
	GeoScope string
	// TextLen is the length of the text that produced SimHash. Short text makes
	// the signature untrustworthy for anything but an exact match.
	TextLen int
	Title   string
	Country string
}

type Verdict struct {
	Same       bool
	Reason     Reason
	Confidence float32
}

// AutoApply reports whether this verdict is strong enough to merge unattended.
// A verdict that is Same but not AutoApply belongs in the review queue.
func (v Verdict) AutoApply() bool {
	return v.Same && v.Confidence >= MinAutoMergeConfidence
}

// Decide runs the cascade in cost order, cheapest and strongest first.
//
// Every branch requires the same company. Two different employers advertising
// identical text are two real opportunities, and merging them across companies
// would hide one of them — the exact failure this package exists to avoid.
func Decide(a, b Candidate) Verdict {
	if a.CompanyID == "" || a.CompanyID != b.CompanyID {
		return Verdict{Reason: ReasonNone}
	}

	// 1. ATS identity. For Tier-A sources this pair is globally unique, so it is
	//    conclusive rather than probabilistic.
	if a.ATSType != "" && a.ATSType == b.ATSType && a.ATSJobID != "" && a.ATSJobID == b.ATSJobID {
		return Verdict{Same: true, Reason: ReasonExactATS, Confidence: 1.0}
	}

	// Geographic scope is part of identity for everything except an exact ATS
	// match. Observed on real data: one company posted "Forward Deployed
	// Engineer - EMEA" twice with byte-identical descriptions and identical
	// titles, but one requisition covered Italy and Sweden while the other
	// covered Austria and Switzerland. They are two real openings, and merging
	// them hides one from every candidate in the countries it dropped.
	if geoConflict(a, b) {
		return Verdict{Reason: ReasonNone}
	}

	// 2. Identical meaningful content.
	if len(a.ContentHash) > 0 && string(a.ContentHash) == string(b.ContentHash) {
		return Verdict{Same: true, Reason: ReasonContentHash, Confidence: 0.99}
	}

	// 3. Same application endpoint. Compared on host+path only: query strings
	//    routinely carry per-source tracking parameters.
	if ua, ok := normalizeURL(a.ApplyURL); ok {
		if ub, ok2 := normalizeURL(b.ApplyURL); ok2 && ua == ub {
			return Verdict{Same: true, Reason: ReasonApplyURL, Confidence: 0.97}
		}
	}

	// 4. Near-identical text. Two independent guards, because text similarity is
	//    the weakest signal here and the only probabilistic one:
	//      - titles must agree: a company's postings share most of their
	//        boilerplate, so description similarity alone is not identity
	//      - the text must be long enough to be distinctive, unless the
	//        signatures match exactly
	if a.SimHash != 0 && b.SimHash != 0 && titlesAgree(a.Title, b.Title) &&
		titleIsSpecific(a.Title) && titleIsSpecific(b.Title) {
		d := Hamming(a.SimHash, b.SimHash)
		longEnough := a.TextLen >= MinTextForFuzzy && b.TextLen >= MinTextForFuzzy
		if d == 0 || (d <= MaxHamming && longEnough) {
			// Confidence decays with distance so a reviewer can triage the weakest
			// merges first.
			return Verdict{Same: true, Reason: ReasonSimHash, Confidence: 0.90 - float32(d)*0.05}
		}
	}

	return Verdict{Reason: ReasonNone}
}

// geoConflict reports whether two postings are open to demonstrably different
// country sets. An empty scope on either side is unknown, not a conflict: we do
// not manufacture a difference we cannot observe.
func geoConflict(a, b Candidate) bool {
	if a.GeoScope == "" || b.GeoScope == "" {
		return false
	}
	return a.GeoScope != b.GeoScope
}

// titlesAgree requires the shorter title's tokens to be a SUBSET of the longer's.
//
// This is the real discriminator, and it has to be strict. A proportional
// threshold does not work: "Senior Backend Engineer" and "Senior Frontend
// Engineer" share two of three tokens, so any two-thirds rule accepts them —
// while ignoring the single token that distinguishes the two roles. Combined
// with boilerplate-dominated descriptions (where the shared intro can be 90% of
// the text), that produced false merges in the eval set.
//
// Subset containment still accepts the case that matters: a suffixed or
// team-scoped variant of the same role, e.g. "Senior Backend Engineer" against
// "Senior Backend Engineer - Monitoring".
func titlesAgree(a, b string) bool {
	ta, tb := dropStopWords(tokenize(a)), dropStopWords(tokenize(b))
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	shorter, longer := ta, tb
	if len(tb) < len(ta) {
		shorter, longer = tb, ta
	}
	set := make(map[string]bool, len(longer))
	for _, t := range longer {
		set[t] = true
	}
	for _, t := range shorter {
		if !set[t] {
			return false
		}
	}
	return true
}

// titleIsSpecific reports whether a title carries enough distinctive tokens for
// text similarity to be trustworthy alongside it.
func titleIsSpecific(t string) bool {
	return len(dropStopWords(tokenize(t))) >= MinTitleTokensForFuzzy
}

// normalizeURL reduces to scheme-less host+path, lowercased, without a trailing
// slash. Returns false when there is nothing usable to compare.
func normalizeURL(raw string) (string, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return "", false
	}
	for _, p := range []string{"https://", "http://"} {
		raw = strings.TrimPrefix(raw, p)
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" || !strings.Contains(raw, "/") {
		return "", false
	}
	return raw, true
}
