package normalize

import "strings"

// Work modes. Empty means the text did not say, which is different from onsite.
const (
	WorkRemote = "remote"
	WorkHybrid = "hybrid"
	WorkOnsite = "onsite"
)

// Location is the derived view of a location string.
//
// Country is set only when the string names EXACTLY ONE country we recognise.
// Multi-country postings leave it nil and populate GeoScope instead: "remote,
// but US only" versus "remote, Canada or US" is the single most consequential
// distinction in remote hiring, and collapsing it to one country would be a
// confident lie.
type Location struct {
	WorkMode string
	Country  *string // ISO-3166 alpha-2
	City     *string
	GeoScope []string // every recognised country, sorted; empty when none
}

// countries maps the spellings sources actually use to ISO codes. Deliberately
// curated rather than fuzzy: an unrecognised name yields nil, and nil is a
// correct answer that normalization (or a human) can improve later.
var countries = map[string]string{
	"united states": "US", "usa": "US", "us": "US", "u.s.": "US", "u.s.a.": "US", "america": "US",
	"united kingdom": "GB", "uk": "GB", "u.k.": "GB", "great britain": "GB", "england": "GB",
	"scotland": "GB", "wales": "GB", "northern ireland": "GB",
	"canada": "CA", "mexico": "MX", "brazil": "BR", "argentina": "AR", "chile": "CL",
	"colombia": "CO", "peru": "PE", "uruguay": "UY", "costa rica": "CR",
	"germany": "DE", "france": "FR", "spain": "ES", "portugal": "PT", "italy": "IT",
	"netherlands": "NL", "the netherlands": "NL", "belgium": "BE", "luxembourg": "LU",
	"ireland": "IE", "denmark": "DK", "sweden": "SE", "norway": "NO", "finland": "FI",
	"iceland": "IS", "poland": "PL", "czechia": "CZ", "czech republic": "CZ",
	"slovakia": "SK", "hungary": "HU", "romania": "RO", "bulgaria": "BG",
	"greece": "GR", "croatia": "HR", "slovenia": "SI", "serbia": "RS",
	"austria": "AT", "switzerland": "CH", "estonia": "EE", "latvia": "LV",
	"lithuania": "LT", "ukraine": "UA", "turkey": "TR", "türkiye": "TR",
	"israel": "IL", "united arab emirates": "AE", "uae": "AE", "u.a.e.": "AE",
	"saudi arabia": "SA", "qatar": "QA", "egypt": "EG", "south africa": "ZA",
	"kenya": "KE", "nigeria": "NG", "morocco": "MA",
	"india": "IN", "pakistan": "PK", "bangladesh": "BD", "sri lanka": "LK",
	"china": "CN", "japan": "JP", "south korea": "KR", "korea": "KR",
	"singapore": "SG", "malaysia": "MY", "indonesia": "ID", "thailand": "TH",
	"vietnam": "VN", "philippines": "PH", "taiwan": "TW", "hong kong": "HK",
	"australia": "AU", "new zealand": "NZ",
}

// remoteHints and hybridHints are the only phrases we will act on.
var (
	remoteHints = []string{"remote", "work from home", "wfh", "anywhere", "distributed"}
	hybridHints = []string{"hybrid"}
)

// ParseLocation is pure and idempotent.
//
// Sources write locations as free text with any separator: "Remote, Italy",
// "Bangalore, India", "Remote, Canada; Remote, United States", or bare "Remote".
func ParseLocation(raw string) Location {
	s := normalizeSpace(strings.ToLower(raw))
	var loc Location
	if s == "" {
		return loc
	}

	switch {
	case containsAny(" "+s+" ", hybridHints):
		loc.WorkMode = WorkHybrid
	case containsAny(" "+s+" ", remoteHints):
		loc.WorkMode = WorkRemote
	}

	// Split on every separator sources use, then look each fragment up whole.
	// Substring matching against country names would make "Remote, Indiana"
	// resolve to India.
	seen := map[string]bool{}
	var order []string
	var nonCountry []string
	for _, frag := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == '/'
	}) {
		frag = stripModeWords(frag)
		if frag == "" {
			continue
		}
		if iso, ok := countries[frag]; ok {
			if !seen[iso] {
				seen[iso] = true
				order = append(order, iso)
			}
			continue
		}
		nonCountry = append(nonCountry, frag)
	}

	loc.GeoScope = sortedStrings(order)
	// Exactly one recognised country, or nothing. Two countries is a scope, not
	// a location.
	if len(loc.GeoScope) == 1 {
		c := loc.GeoScope[0]
		loc.Country = &c
	}

	// The first unrecognised fragment is most likely a city. Recorded as a city
	// but never used to infer a country — "Remote, Bangalore" tells us the city,
	// and guessing IN from it is exactly the kind of inference §3 forbids.
	if len(nonCountry) > 0 && !isNoise(nonCountry[0]) {
		city := titleish(nonCountry[0])
		loc.City = &city
	}

	// A bare "Remote" with no geography at all is still remote.
	if loc.WorkMode == "" && loc.Country == nil && loc.City == nil {
		return Location{}
	}
	if loc.WorkMode == "" && (loc.Country != nil || loc.City != nil) {
		loc.WorkMode = WorkOnsite
	}
	return loc
}

// stripModeWords removes a leading work-mode token and the separator noise
// around it, so "Hybrid - Berlin" and "Remote - Berlin" both yield "berlin".
func stripModeWords(frag string) string {
	frag = strings.TrimSpace(frag)
	for _, w := range []string{"remote", "hybrid", "onsite", "on-site", "on site", "wfh"} {
		if frag == w {
			return ""
		}
		if strings.HasPrefix(frag, w) {
			rest := strings.TrimSpace(strings.TrimPrefix(frag, w))
			rest = strings.TrimSpace(strings.Trim(rest, "-–—:()/,"))
			// Only treat it as a prefix if something separated it from the rest,
			// so "Remoteville" is not mangled into "ville".
			if rest != frag && (rest == "" || len(rest) < len(frag)-len(w)+1) {
				frag = rest
				break
			}
		}
	}
	return strings.TrimSpace(strings.Trim(frag, "-–—()"))
}

// isNoise filters fragments that are clearly not a place.
func isNoise(s string) bool {
	switch s {
	case "any", "anywhere", "global", "worldwide", "multiple locations", "various", "flexible", "n/a", "-":
		return true
	}
	return len(s) < 2
}

func titleish(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		r := []rune(p)
		if len(r) > 0 {
			parts[i] = strings.ToUpper(string(r[0])) + string(r[1:])
		}
	}
	return strings.Join(parts, " ")
}

func sortedStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := make([]string, len(in))
	copy(out, in)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
