package eval

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Xubair001/devsignal/internal/source"
	"github.com/Xubair001/devsignal/internal/source/ashby"
	"github.com/Xubair001/devsignal/internal/source/greenhouse"
	"github.com/Xubair001/devsignal/internal/source/lever"
)

// -regenerate rebuilds the frozen fixtures from recorded board payloads.
//
// Deliberately a separate flag from the parser goldens' -update, and deliberately
// manual: regenerating the label set changes what every future NDCG number means,
// so it is a reviewed act. The committed JSON is the source of truth for a run;
// this generator only exists so the labels can be audited and rebuilt.
var regenerate = flag.Bool("regenerate", false, "rebuild corpus, personas and judgements fixtures")

// rawBoards are the recorded payloads the corpus is built from. Kept out of
// testdata/ deliberately — they are large, and the parsed corpus is what eval
// actually consumes.
var rawBoards = []struct {
	family, token, path string
}{
	{"greenhouse", "gitlab", "/tmp/eval_greenhouse.json"},
	{"lever", "unlimit", "/tmp/eval_lever.json"},
	{"ashby", "linear", "/tmp/eval_ashby.json"},
}

func TestRegenerateFixtures(t *testing.T) {
	if !*regenerate {
		t.Skip("run with -regenerate to rebuild the frozen fixtures")
	}

	corpus := buildCorpus(t)
	writeFixture(t, "corpus.json", corpus)
	t.Logf("corpus: %d postings", len(corpus))

	personas := buildPersonas()
	writeFixture(t, "personas.json", personas)

	judgements := buildJudgements(personas, corpus)
	writeFixture(t, "judgements.json", judgements)
	t.Logf("judgements: %d across %d personas", len(judgements), len(personas))
}

func buildCorpus(t *testing.T) []Posting {
	t.Helper()
	var out []Posting
	for _, b := range rawBoards {
		body, err := os.ReadFile(b.path)
		if err != nil {
			t.Fatalf("reading %s: %v (fetch the board payloads first)", b.path, err)
		}
		var parsed []source.ParsedPosting
		switch b.family {
		case "greenhouse":
			parsed, err = greenhouse.New(b.token, nil).Parse(source.RawDocument{Body: body})
		case "lever":
			parsed, err = lever.New(b.token, nil).Parse(source.RawDocument{Body: body})
		case "ashby":
			parsed, err = ashby.New(b.token, nil).Parse(source.RawDocument{Body: body})
		}
		if err != nil {
			t.Fatalf("parsing %s: %v", b.family, err)
		}
		for _, p := range parsed {
			out = append(out, Posting{
				ATSType: p.ATSType, ATSJobID: p.ATSJobID, BoardToken: b.token,
				Title: p.Title, DescriptionHTML: p.DescriptionHTML,
				LocationRaw: p.LocationRaw, WorkMode: p.WorkMode,
				Language: p.Language, ApplyURL: p.ApplyURL,
				PostedAt: p.SourceReportedPostedAt,
			})
		}
	}
	// Stable order so a regeneration produces a reviewable diff rather than a
	// reshuffle.
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func writeFixture(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", name), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- personas

func s16(v int16) *int16 { return &v }

// buildPersonas returns personas chosen to match what the corpus actually holds.
//
// Counted from the recorded boards: sales/AE 63, backend 34, engineering
// management 15, support 14, platform 12, security 10, product management 10,
// design 6, fullstack 7. Frontend, QA and data/ML have one posting each, so a
// persona for them would measure the corpus rather than the scorer and is left
// out. That is a limitation of this corpus, not a judgement about those roles.
func buildPersonas() []Persona {
	remote := "remote"
	return []Persona{
		{
			ID:       "senior-backend-go",
			Headline: "Senior backend engineer working in Go and PostgreSQL on distributed systems",
			ConstructedFrom: "synthetic persona; skills and seniority chosen to match the " +
				"backend postings present in the recorded corpus",
			SeniorityOrdinal:   s16(4),
			TargetRoleFamilies: []string{"backend", "platform"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"go", "postgresql", "kubernetes", "grpc", "distributed-systems"},
		},
		{
			ID:                 "staff-platform-sre",
			Headline:           "Staff infrastructure engineer, Kubernetes and observability at scale",
			ConstructedFrom:    "synthetic persona matching the platform and SRE postings in the corpus",
			SeniorityOrdinal:   s16(5),
			TargetRoleFamilies: []string{"platform"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"kubernetes", "terraform", "prometheus", "linux", "go"},
		},
		{
			ID:                 "senior-security-engineer",
			Headline:           "Senior security engineer, application security and threat detection",
			ConstructedFrom:    "synthetic persona matching the security postings in the corpus",
			SeniorityOrdinal:   s16(4),
			TargetRoleFamilies: []string{"security"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"appsec", "threat-modelling", "python", "cryptography"},
		},
		{
			ID:                 "mid-fullstack-ts",
			Headline:           "Mid-level fullstack engineer, TypeScript and React with a Rails backend",
			ConstructedFrom:    "synthetic persona matching the fullstack postings in the corpus",
			SeniorityOrdinal:   s16(3),
			TargetRoleFamilies: []string{"fullstack", "frontend"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"typescript", "react", "ruby", "rails", "graphql"},
		},
		{
			ID:                 "senior-product-manager",
			Headline:           "Senior product manager for developer tooling and platform products",
			ConstructedFrom:    "synthetic persona matching the product management postings in the corpus",
			SeniorityOrdinal:   s16(4),
			TargetRoleFamilies: []string{"product"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"product-discovery", "roadmapping", "analytics", "sql"},
		},
		{
			ID:                 "engineering-manager",
			Headline:           "Engineering manager leading backend and platform teams",
			ConstructedFrom:    "synthetic persona matching the engineering management postings in the corpus",
			SeniorityOrdinal:   s16(5),
			IsManagement:       true,
			TargetRoleFamilies: []string{"engineering", "backend"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"people-management", "hiring", "go", "architecture"},
		},
		{
			ID:                 "support-engineer",
			Headline:           "Technical support engineer for a developer platform",
			ConstructedFrom:    "synthetic persona matching the support postings in the corpus",
			SeniorityOrdinal:   s16(3),
			TargetRoleFamilies: []string{"support"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"troubleshooting", "sql", "linux", "customer-communication"},
		},
		{
			ID:                 "senior-product-designer",
			Headline:           "Senior product designer, design systems and complex web applications",
			ConstructedFrom:    "synthetic persona matching the design postings in the corpus",
			SeniorityOrdinal:   s16(4),
			TargetRoleFamilies: []string{"design"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"design-systems", "figma", "interaction-design", "accessibility"},
		},
		{
			ID:                 "enterprise-account-executive",
			Headline:           "Enterprise account executive selling developer platforms",
			ConstructedFrom:    "synthetic persona matching the large sales cohort in the corpus",
			SeniorityOrdinal:   s16(4),
			TargetRoleFamilies: []string{"sales"},
			WorkModePreference: &remote,
			Languages:          []string{"en"},
			Skills:             []string{"enterprise-sales", "pipeline-management", "negotiation"},
		},
	}
}

// Rubric discipline vocabulary. Named because these strings appear in the pattern
// table, the adjacency map and the persona mapping, and a typo in any one of them
// would silently make a whole discipline unjudgeable.
const (
	dBackend    = "backend"
	dPlatform   = "platform"
	dSecurity   = "security"
	dFullstack  = "fullstack"
	dFrontend   = "frontend"
	dDataML     = "data-ml"
	dQA         = "qa"
	dProduct    = "product"
	dDesign     = "design"
	dSupport    = "support"
	dSales      = "sales"
	dMarketing  = "marketing"
	dEngManage  = "eng-management"
	dEngGeneric = "engineering-generic"
	dBackOffice = "finance-people-legal"
	dOther      = "other"
)

// ---------------------------------------------------------------- the rubric

// The labelling rubric, stated so a label can be argued with.
//
// Labels are assigned from the POSTING TITLE as a person would read it: what
// discipline is this role, and at what level. That is deliberately a narrower
// input than the scorer uses — the scorer also sees the description, the extracted
// skills and the embedding — but it overlaps on discipline and seniority, so these
// labels are not fully independent of the thing they measure. See the package
// comment: this is a regression harness, and the honest reading of a rising NDCG
// is "go look at which pairs moved".
//
//	3  excellent  same discipline, seniority within one rung
//	2  good       same discipline but two or more rungs away, OR a closely
//	              adjacent discipline at a comparable level
//	1  marginal   a distantly related discipline, or IC/management crossover
//	0  irrelevant a different function entirely
//
// discipline detection is keyword-based on the title. Keyword matching is crude
// and it is the right amount of crude here: a rubric complicated enough to need
// its own tests has become a second scorer, and then the eval measures agreement
// between two models rather than anything about the product.
var disciplinePatterns = []struct {
	name string
	rx   *regexp.Regexp
}{
	{dEngManage, regexp.MustCompile(`(?i)engineering manager|director,? engineering|manager,? engineering|vp,? engineering|head of engineering`)},
	{dBackend, regexp.MustCompile(`(?i)backend|back-end|gitaly|\bruby\b|\bgolang\b|\bgo\b\)`)},
	{dPlatform, regexp.MustCompile(`(?i)infrastructure|platform|\bsre\b|site reliability|devops|reliability engineer`)},
	{dSecurity, regexp.MustCompile(`(?i)security|appsec|threat|vulnerability|trust & safety`)},
	{dFullstack, regexp.MustCompile(`(?i)fullstack|full-stack|full stack`)},
	{dFrontend, regexp.MustCompile(`(?i)frontend|front-end|\breact\b`)},
	{dDataML, regexp.MustCompile(`(?i)machine learning|\bml\b|data scien|data engineer|analytics engineer`)},
	{dQA, regexp.MustCompile(`(?i)\bqa\b|quality engineer|test engineer|sdet`)},
	{dProduct, regexp.MustCompile(`(?i)product manager|product lead|group product|principal product`)},
	{dDesign, regexp.MustCompile(`(?i)designer|\bux\b|design systems`)},
	{dSupport, regexp.MustCompile(`(?i)support engineer|support specialist|technical support|customer support`)},
	{dSales, regexp.MustCompile(`(?i)account executive|\bsales\b|business development|revenue|account manager|customer success`)},
	{dMarketing, regexp.MustCompile(`(?i)marketing|brand|campaign|content strategist|communications`)},
	{dBackOffice, regexp.MustCompile(`(?i)accountant|finance|payroll|recruiter|people ops|talent|legal|counsel|auditor`)},
	// Generic engineering last: it only applies when nothing more specific did.
	{dEngGeneric, regexp.MustCompile(`(?i)engineer|developer|architect`)},
}

// adjacency records which disciplines a persona would genuinely consider, and how
// closely. Asymmetric on purpose: a backend engineer often takes a platform role,
// while a designer does not take a backend one.
var adjacency = map[string]map[string]int{
	dBackend:   {dPlatform: 2, dFullstack: 2, dEngGeneric: 2, dDataML: 1, dSecurity: 1, dEngManage: 1, dQA: 1},
	dPlatform:  {dBackend: 2, dEngGeneric: 2, dSecurity: 1, dDataML: 1, dEngManage: 1, dQA: 1},
	dSecurity:  {dPlatform: 1, dBackend: 1, dEngGeneric: 1},
	dFullstack: {dFrontend: 2, dBackend: 2, dEngGeneric: 2, dDesign: 1, dQA: 1},
	dFrontend:  {dFullstack: 2, dEngGeneric: 2, dDesign: 1},
	dProduct:   {dDesign: 1, dEngGeneric: 1, dEngManage: 1},
	dEngManage: {dBackend: 1, dPlatform: 1, dEngGeneric: 1, dProduct: 1},
	dSupport:   {dEngGeneric: 1, dBackend: 1, dQA: 1},
	dDesign:    {dProduct: 1, dFrontend: 1},
	dSales:     {dMarketing: 1},
}

// personaDiscipline maps a persona to the rubric's discipline vocabulary.
var personaDiscipline = map[string]string{
	"senior-backend-go":            dBackend,
	"staff-platform-sre":           dPlatform,
	"senior-security-engineer":     dSecurity,
	"mid-fullstack-ts":             dFullstack,
	"senior-product-manager":       dProduct,
	"engineering-manager":          dEngManage,
	"support-engineer":             dSupport,
	"senior-product-designer":      dDesign,
	"enterprise-account-executive": dSales,
}

// seniorityWords maps title vocabulary onto the 1-6 ladder. Only what titles
// actually say; a title with no level word yields 0 = unstated.
var seniorityWords = []struct {
	rx   *regexp.Regexp
	rung int16
}{
	{regexp.MustCompile(`(?i)\bintern\b|internship`), 1},
	{regexp.MustCompile(`(?i)\bjunior\b|\bassociate\b|\bgraduate\b|entry.level`), 2},
	{regexp.MustCompile(`(?i)\bintermediate\b|\bmid\b`), 3},
	{regexp.MustCompile(`(?i)\bsenior\b|\bsr\.?\b`), 4},
	{regexp.MustCompile(`(?i)\bstaff\b|\blead\b|\bmanager\b`), 5},
	{regexp.MustCompile(`(?i)\bprincipal\b|\bdistinguished\b|\bdirector\b|\bhead of\b|\bvp\b`), 6},
}

func titleDiscipline(title string) string {
	for _, d := range disciplinePatterns {
		if d.rx.MatchString(title) {
			return d.name
		}
	}
	return dOther
}

// titleSeniority returns the highest level word in the title, or 0 for unstated.
// Highest rather than first, because "Senior Staff Engineer" is staff.
func titleSeniority(title string) int16 {
	var best int16
	for _, s := range seniorityWords {
		if s.rx.MatchString(title) && s.rung > best {
			best = s.rung
		}
	}
	return best
}

// judge applies the rubric to one pair.
func judge(p Persona, post Posting) (int, string) {
	pd := personaDiscipline[p.ID]
	td := titleDiscipline(post.Title)

	if pd == td {
		ts := titleSeniority(post.Title)
		if ts == 0 || p.SeniorityOrdinal == nil {
			return 2, "same discipline; the title states no level"
		}
		gap := int(ts) - int(*p.SeniorityOrdinal)
		if gap < 0 {
			gap = -gap
		}
		if gap <= 1 {
			return 3, "same discipline, within one rung"
		}
		return 2, "same discipline, more than one rung away"
	}

	if grade, ok := adjacency[pd][td]; ok {
		if grade >= 2 {
			return 2, "closely adjacent discipline (" + td + ")"
		}
		return 1, "distantly related discipline (" + td + ")"
	}
	return 0, "different function (" + td + ")"
}

// buildJudgements labels EVERY (persona, posting) pair.
//
// Exhaustive rather than sampled, and the first version of this file got that
// wrong in a way worth recording. It sampled up to 12 grade-3 postings per
// persona, which left NDCG@10 measuring the wrong thing: the corpus holds 34
// backend postings, so the scorer's top ten for the backend persona were mostly
// postings the sample had never labelled. Unjudged counts as relevance 0, so a
// list of ten genuinely excellent matches scored 0.064 — while the security
// persona, whose nine relevant postings were nearly all labelled, scored 0.777.
// The metric was ranking label coverage, not relevance.
//
// Human labelling could not be exhaustive at this corpus size, which is why
// sampling is the norm in information retrieval. A rubric can be, at no extra
// cost, so it should be: every pair labelled means no unjudged postings, which
// removes the bias entirely rather than documenting it.
func buildJudgements(personas []Persona, corpus []Posting) []Judgement {
	out := make([]Judgement, 0, len(personas)*len(corpus))
	for _, p := range personas {
		for _, post := range corpus {
			grade, why := judge(p, post)
			out = append(out, Judgement{
				PersonaID: p.ID, PostingKey: post.Key(),
				Relevance: grade, Rationale: why,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PersonaID != out[j].PersonaID {
			return out[i].PersonaID < out[j].PersonaID
		}
		return out[i].PostingKey < out[j].PostingKey
	})
	return out
}

// ---------------------------------------------------------------- fixture guards

// The fixtures are the eval's ground truth, so their shape is worth asserting
// even though nothing computes them at run time.
func TestFixturesAreCoherent(t *testing.T) {
	corpus, err := LoadCorpus()
	if err != nil {
		t.Fatal(err)
	}
	personas, err := LoadPersonas()
	if err != nil {
		t.Fatal(err)
	}
	judgements, err := LoadJudgements()
	if err != nil {
		t.Fatal(err)
	}

	if len(corpus) < 100 {
		t.Errorf("corpus has %d postings; too small for NDCG@10 to mean much", len(corpus))
	}
	if len(personas) < 5 {
		t.Errorf("%d personas; the blueprint asks for 5-10", len(personas))
	}
	if len(judgements) < 150 {
		t.Errorf("%d judgements; the blueprint asks for 200 to start", len(judgements))
	}

	keys := map[string]bool{}
	for _, p := range corpus {
		if keys[p.Key()] {
			t.Errorf("duplicate corpus key %s", p.Key())
		}
		keys[p.Key()] = true
		if strings.TrimSpace(p.Title) == "" {
			t.Errorf("posting %s has no title", p.Key())
		}
	}

	ids := map[string]bool{}
	for _, p := range personas {
		ids[p.ID] = true
		if p.ConstructedFrom == "" {
			t.Errorf("persona %s has no provenance; a fixture without it gets mistaken for observed data", p.ID)
		}
		if _, ok := personaDiscipline[p.ID]; !ok {
			t.Errorf("persona %s has no rubric discipline, so it cannot be judged", p.ID)
		}
	}

	// Every judgement must point at something that exists, or the metric silently
	// scores against nothing.
	perPersona := map[string]int{}
	positives := map[string]int{}
	for _, j := range judgements {
		if !ids[j.PersonaID] {
			t.Errorf("judgement references unknown persona %q", j.PersonaID)
		}
		if !keys[j.PostingKey] {
			t.Errorf("judgement references unknown posting %q", j.PostingKey)
		}
		if j.Relevance < 0 || j.Relevance > 3 {
			t.Errorf("relevance %d outside 0-3", j.Relevance)
		}
		if j.Rationale == "" {
			t.Errorf("judgement %s/%s has no rationale", j.PersonaID, j.PostingKey)
		}
		perPersona[j.PersonaID]++
		if j.Relevance >= relevantThreshold {
			positives[j.PersonaID]++
		}
	}

	// A persona with no positives contributes an undefined NDCG and silently
	// shrinks the sample. Better to know.
	for id := range ids {
		if perPersona[id] == 0 {
			t.Errorf("persona %s has no judgements", id)
		}
		if positives[id] == 0 {
			t.Errorf("persona %s has no judged-relevant postings; its NDCG is undefined", id)
		}
	}
}
