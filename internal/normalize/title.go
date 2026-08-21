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
)

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
var managementNeedles = []string{
	"manager", "director", "vp ", "vice president", "head of", "chief", "cto", "ceo", "cfo",
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
