package normalize

import (
	"strings"
)

// Seniority is the individual-contributor ladder. Management is tracked
// separately (see Title.IsManagement) because a Senior Engineer and an
// Engineering Manager are different tracks, not different rungs — collapsing
// them would make the seniority term in the fit score compare unlike things.
const (
	SeniorityIntern    int16 = 1
	SeniorityJunior    int16 = 2
	SeniorityMid       int16 = 3
	SenioritySenior    int16 = 4
	SeniorityStaff     int16 = 5
	SeniorityPrincipal int16 = 6

	// SeniorityMin and SeniorityMax bound the ladder. The fit score's seniority
	// term divides by this span, and anything outside it is treated as unknown
	// rather than clamped: the opportunity column permits 0-9 for historical
	// reasons, and a value of 9 compared against a 1-6 profile is not a distance,
	// it is a category error.
	SeniorityMin int16 = SeniorityIntern
	SeniorityMax int16 = SeniorityPrincipal
)

// seniorityLadder is the single definition of the rungs.
//
// It was previously written out in three places — title parsing, the profile API,
// and the profile embedder — and the third copy was off by one, so a "mid" profile
// was embedded with the word "senior". One definition, because a ladder that
// disagrees with itself silently shifts every seniority comparison by a rung.
// Rung labels, named so the embedding vocabulary below is built FROM the label
// rather than repeating it. That makes "the terms contain the label" structural
// instead of a coincidence a later edit could break.
const (
	labelIntern    = "intern"
	labelJunior    = "junior"
	labelMid       = "mid"
	labelSenior    = "senior"
	labelStaff     = "staff"
	labelPrincipal = "principal"
)

var seniorityLadder = map[int16]struct {
	Label string
	// Terms is the vocabulary postings actually use, for embedding. The label
	// alone is too sparse: "mid" rarely appears in a job description, while
	// "mid level engineer" does.
	Terms string
}{
	SeniorityIntern:    {labelIntern, labelIntern + " internship entry level"},
	SeniorityJunior:    {labelJunior, labelJunior + " engineer early career"},
	SeniorityMid:       {labelMid, labelMid + " level engineer intermediate"},
	SenioritySenior:    {labelSenior, labelSenior + " engineer"},
	SeniorityStaff:     {labelStaff, labelStaff + " engineer technical lead"},
	SeniorityPrincipal: {labelPrincipal, labelPrincipal + " engineer distinguished architect"},
}

// SeniorityLabel is the short name for a rung, or nil when the ordinal is not on
// the ladder.
func SeniorityLabel(ordinal *int16) *string {
	if ordinal == nil {
		return nil
	}
	if r, ok := seniorityLadder[*ordinal]; ok {
		l := r.Label
		return &l
	}
	return nil
}

// SeniorityOrdinal maps a label back to its rung.
func SeniorityOrdinal(label *string) *int16 {
	if label == nil {
		return nil
	}
	for ord, r := range seniorityLadder {
		if r.Label == *label {
			o := ord
			return &o
		}
	}
	return nil
}

// SeniorityTerms is the embedding vocabulary for a rung, empty when unknown.
func SeniorityTerms(ordinal *int16) string {
	if ordinal == nil {
		return ""
	}
	if r, ok := seniorityLadder[*ordinal]; ok {
		return r.Terms
	}
	return ""
}

// Role families. A closed enum: retrieval filters and the fit score's domain
// term both key on these, so the set must be shared rather than restated.
const (
	FamilyMl          = "ml"
	FamilyData        = "data"
	FamilySecurity    = "security"
	FamilyPlatform    = "platform"
	FamilyMobile      = "mobile"
	FamilyFrontend    = "frontend"
	FamilyBackend     = "backend"
	FamilyFullstack   = "fullstack"
	FamilyQa          = "qa"
	FamilyDesign      = "design"
	FamilyProduct     = "product"
	FamilySales       = "sales"
	FamilySupport     = "support"
	FamilyMarketing   = "marketing"
	FamilyPeople      = "people"
	FamilyFinance     = "finance"
	FamilyEngineering = "engineering"
)

// Title is the derived view of a job title. Pointer fields are nil when the
// title did not say.
type Title struct {
	Normalized   string
	RoleFamily   *string
	Seniority    *int16
	IsManagement bool
}

// seniorityRules are checked in order, most specific first, so "senior staff"
// resolves to staff rather than senior.
var seniorityRules = []struct {
	needles []string
	level   int16
}{
	{[]string{"distinguished", "fellow"}, SeniorityPrincipal},
	{[]string{"principal"}, SeniorityPrincipal},
	{[]string{"staff"}, SeniorityStaff},
	{[]string{"senior", "sr.", "sr ", "lead "}, SenioritySenior},
	{[]string{"intermediate", "mid-level", "mid level"}, SeniorityMid},
	{[]string{"junior", "jr.", "jr ", "entry level", "entry-level", "graduate", "associate"}, SeniorityJunior},
	{[]string{"intern", "internship", "apprentice", "trainee"}, SeniorityIntern},
}

// managementNeedles indicate people leadership. "lead" is deliberately absent:
// "Tech Lead" is usually an IC role, and guessing wrong here mis-sorts a whole
// career track.
//
// Bare "manager" was also here, and was worse than "lead" for exactly the reason
// that comment gives. Measured on 286 real postings from three boards, it flagged
// 99 of them — 35% — as people leadership, including "Business Development
// Manager", "Customer Success Manager", "Account Manager" and "Product Manager".
// In commercial titles "X Manager" usually means an individual owning a book of
// business or a product, not a person managing people.
//
// It stayed invisible until the fit score began reading is_management, at which
// point the eval harness showed the product-manager persona's NDCG@10 collapse
// from 0.699 to 0.026: every Product Manager posting had become a cross-track
// mismatch. "Manager" now only counts where the title names a management context.
// Needles are matched as substrings against a space-padded title, so any needle
// short enough to appear inside another word MUST carry its own spaces. "cto"
// without them matched "Public Se-cto-r", which classified every public-sector
// sales title as people leadership — a bug that predated the tightening below and
// was found by the same eval run.
var managementNeedles = []string{
	"director", "vice president", "head of", "chief",
	" vp ", " cto ", " ceo ", " cfo ", " cpo ", " cio ",
	// "Manager" only in contexts that are unambiguously about leading people or an
	// engineering organisation. Anything not listed here stays IC, which is the
	// safe direction: a misfiled IC role ranks slightly wrong, while a misfiled
	// management role hides an entire track from the person looking for it.
	//
	// "development manager" is deliberately absent: it matches "Business
	// Development Manager", which is an individual sales role.
	"engineering manager", "manager, engineering", "manager of engineering",
	"delivery manager", "people manager", "team manager", "technical manager",
}

// roleFamilyRules map keywords to a family. Order matters: the more specific
// family wins, so "machine learning engineer" is ml, not backend.
//
// Some needles happen to spell the same word as their family constant. They are
// still different things — one is a search term matched against a title, the
// other is the identifier we store — so substituting the constant would couple
// two unrelated meanings for the sake of a linter.
//
//nolint:goconst // search needles are data, not identifiers
var roleFamilyRules = []struct {
	family  string
	needles []string
}{
	{FamilyMl, []string{"machine learning", "ml engineer", "deep learning", "nlp", "computer vision", "ai engineer", "research scientist"}},
	{FamilyData, []string{"data engineer", "data scientist", "analytics engineer", "data analyst", "data platform", "bi ", "business intelligence"}},
	{FamilySecurity, []string{"security", "appsec", "infosec", "threat", "vulnerability", "penetration"}},
	{FamilyPlatform, []string{"site reliability", " sre", "sre ", "devops", "platform engineer", "infrastructure engineer", "cloud engineer"}},
	{FamilyMobile, []string{"ios", "android", "mobile engineer", "react native", "flutter"}},
	{FamilyFrontend, []string{"frontend", "front-end", "front end", "ui engineer", "web engineer"}},
	{FamilyBackend, []string{"backend", "back-end", "back end", "server-side"}},
	{FamilyFullstack, []string{"fullstack", "full-stack", "full stack"}},
	{FamilyQa, []string{"qa ", "quality engineer", "test engineer", "sdet", "automation engineer"}},
	{FamilyDesign, []string{"designer", "ux ", "ui/ux", "product design"}},
	{FamilyProduct, []string{"product manager", "product owner", "technical program manager", "tpm "}},
	{FamilySales, []string{"account executive", "sales", "business development", "solutions engineer", "pre-sales"}},
	{FamilySupport, []string{"support engineer", "customer success", "solutions architect", "technical account"}},
	{FamilyMarketing, []string{"marketing", "content strategist", "demand generation"}},
	{FamilyPeople, []string{"recruiter", "talent acquisition", "people partner", "human resources", "employee relations"}},
	{FamilyFinance, []string{"accountant", "financial analyst", "controller", "payroll"}},
	// Generic last: only reached when nothing more specific matched.
	{FamilyEngineering, []string{"engineer", "developer", "programmer", "architect"}},
}

// ParseTitle derives structure from a raw title. It is pure and idempotent:
// ParseTitle(t).Normalized fed back in yields the same result.
func ParseTitle(raw string) Title {
	norm := normalizeSpace(strings.ToLower(raw))
	t := Title{Normalized: norm}
	if norm == "" {
		return t
	}
	// Pad so word-boundary needles like "sr " and " sre" match at the edges.
	padded := " " + norm + " "

	for _, rule := range seniorityRules {
		if containsAny(padded, rule.needles) {
			lvl := rule.level
			t.Seniority = &lvl
			break
		}
	}
	t.IsManagement = containsAny(padded, managementNeedles)

	for _, rule := range roleFamilyRules {
		if containsAny(padded, rule.needles) {
			fam := rule.family
			t.RoleFamily = &fam
			break
		}
	}
	return t
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// normalizeSpace collapses whitespace and strips the punctuation that varies
// between sources without changing meaning. Kept minimal on purpose: aggressive
// stripping makes distinct titles collide, which then makes dedup merge them.
func normalizeSpace(s string) string {
	s = strings.NewReplacer(
		" ", " ", "\t", " ", "\n", " ", "\r", " ",
		"—", "-", "–", "-",
	).Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
