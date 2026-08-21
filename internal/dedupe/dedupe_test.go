package dedupe

import (
	"fmt"
	"strings"
	"testing"
)

const (
	coA = "company-a"
	coB = "company-b"
	gh  = "greenhouse"
)

func TestSimHashIsStableAndSensitive(t *testing.T) {
	text := "We are looking for a senior backend engineer with Go and PostgreSQL experience."
	a, b := SimHash(text), SimHash(text)
	if a != b {
		t.Fatal("SimHash is not deterministic")
	}
	if a == 0 {
		t.Fatal("SimHash returned zero for real text")
	}
	// Near-identical text stays close.
	near := SimHash(text + " Kubernetes is a plus.")
	if d := Hamming(a, near); d > 12 {
		t.Errorf("small edit moved the signature %d bits; expected it to stay near", d)
	}
	// Genuinely different text moves far.
	far := SimHash("Our marketing team seeks a content strategist for demand generation campaigns.")
	if d := Hamming(a, far); d < 12 {
		t.Errorf("unrelated text is only %d bits away; the signature is not discriminating", d)
	}
}

// Two boards rendering the same job with different markup must agree.
func TestSimHashIgnoresMarkup(t *testing.T) {
	plain := "Senior Backend Engineer. You will build Go services against PostgreSQL and AWS."
	marked := "<div class=\"x\"><p>Senior Backend Engineer.</p><ul><li>You will build Go services " +
		"against PostgreSQL and AWS.</li></ul></div>"
	if d := Hamming(SimHash(plain), SimHash(marked)); d > MaxHamming {
		t.Errorf("markup changed the signature by %d bits (max %d)", d, MaxHamming)
	}
}

func TestBlockKeyGroupsAndSeparates(t *testing.T) {
	// Same role, different word order and filler: same block.
	a := BlockKey(coA, "Senior Backend Engineer", "US")
	b := BlockKey(coA, "Backend Engineer, Senior", "US")
	if a != b {
		t.Errorf("same role landed in different blocks:\n  %s\n  %s", a, b)
	}
	// Different company: never comparable.
	if BlockKey(coB, "Senior Backend Engineer", "US") == a {
		t.Error("different companies share a block")
	}
	// Different country: different block.
	if BlockKey(coA, "Senior Backend Engineer", "DE") == a {
		t.Error("different countries share a block")
	}
	// Different role at the same company: different block.
	if BlockKey(coA, "Senior Frontend Designer", "US") == a {
		t.Error("unrelated roles share a block")
	}
}

// ---------------------------------------------------------------- eval set
//
// Dedup cannot be unit tested meaningfully: it is a statistical decision with
// asymmetric costs. This is the labelled set, and the metric weights false
// merges heavier because a false merge hides a real job permanently.

type pair struct {
	name string
	a, b Candidate
	same bool // ground truth
}

// desc builds a description at realistic length. Real postings run from ~700 to
// ~10,000 characters (a live GitLab board averaged around 9,700), and
// MinTextForFuzzy deliberately refuses to fuzzy-match anything shorter — so an
// eval set built from short stubs would silently exercise the wrong path.
func desc(role, extra string) string {
	return "About the company. We are a global technology organisation with teams " +
		"distributed across more than sixty countries. We believe in transparency, " +
		"iteration and measurable results, and we operate as a remote-first company " +
		"with asynchronous communication as the default. Benefits include private " +
		"health cover, generous parental leave, a home office budget, an annual " +
		"learning stipend and equity for every employee. We are an equal opportunity " +
		"employer and we welcome applicants from every background. " +
		"Role: " + role + ". " + extra +
		" How we hire: an initial screen, a technical conversation, a practical " +
		"exercise reviewed with the team, and a final conversation with the hiring " +
		"manager. We aim to complete the process within three weeks and we give " +
		"feedback at every stage."
}

func evalSet() []pair {
	backend := desc("Senior Backend Engineer building Go services on PostgreSQL",
		"You will own service reliability and mentor engineers.")
	backendEdited := desc("Senior Backend Engineer building Go services on PostgreSQL",
		"You will own service reliability and mentor other engineers.")
	frontend := desc("Senior Frontend Engineer building React interfaces",
		"You will own the design system and mentor engineers.")

	mk := func(co, ats, id, url, role string, hash []byte) Candidate {
		return Candidate{
			CompanyID: co, ATSType: ats, ATSJobID: id, ApplyURL: url,
			ContentHash: hash, SimHash: SimHash(desc(role, "")),
			Title: role, Country: "US",
		}
	}
	_ = mk

	cand := func(co, ats, id, url, title, body string, hash []byte) Candidate {
		return Candidate{
			CompanyID: co, ATSType: ats, ATSJobID: id, ApplyURL: url,
			ContentHash: hash, SimHash: SimHash(body), TextLen: len(body),
			Title: title, Country: "US",
		}
	}

	return []pair{
		{
			name: "same ATS id is conclusive",
			a:    cand(coA, gh, "111", "https://boards.gh.io/a/jobs/111", "Senior Backend Engineer", backend, []byte("h1")),
			b:    cand(coA, gh, "111", "https://boards.gh.io/a/jobs/111?src=x", "Senior Backend Engineer", backend, []byte("h2")),
			same: true,
		},
		{
			name: "identical content hash",
			a:    cand(coA, gh, "201", "https://boards.gh.io/a/jobs/201", "Senior Backend Engineer", backend, []byte("same")),
			b:    cand(coA, "lever", "999", "https://jobs.lever.co/a/999", "Senior Backend Engineer", backend, []byte("same")),
			same: true,
		},
		{
			name: "same apply url, tracking params differ",
			a:    cand(coA, gh, "301", "https://careers.a.com/jobs/301", "Senior Backend Engineer", backend, []byte("x")),
			b:    cand(coA, "", "", "https://careers.a.com/jobs/301?utm_source=board", "Senior Backend Engineer", backend, []byte("y")),
			same: true,
		},
		{
			name: "lightly edited repost of the same role",
			a:    cand(coA, gh, "401", "https://boards.gh.io/a/jobs/401", "Senior Backend Engineer", backend, []byte("p")),
			b:    cand(coA, "", "", "https://jobs.other.com/a/xyz", "Senior Backend Engineer", backendEdited, []byte("q")),
			same: true,
		},
		{
			name: "different roles sharing boilerplate must NOT merge",
			a:    cand(coA, gh, "501", "https://boards.gh.io/a/jobs/501", "Senior Backend Engineer", backend, []byte("r")),
			b:    cand(coA, gh, "502", "https://boards.gh.io/a/jobs/502", "Senior Frontend Engineer", frontend, []byte("s")),
			same: false,
		},
		{
			name: "identical text at DIFFERENT companies must NOT merge",
			a:    cand(coA, gh, "601", "https://boards.gh.io/a/jobs/601", "Senior Backend Engineer", backend, []byte("t")),
			b:    cand(coB, gh, "701", "https://boards.gh.io/b/jobs/701", "Senior Backend Engineer", backend, []byte("t")),
			same: false,
		},
		{
			name: "same title, genuinely different role text",
			a:    cand(coA, gh, "801", "https://boards.gh.io/a/jobs/801", "Engineer", backend, []byte("u")),
			b:    cand(coA, gh, "802", "https://boards.gh.io/a/jobs/802", "Engineer", frontend, []byte("v")),
			same: false,
		},
		{
			name: "no signals at all must NOT merge",
			a:    Candidate{CompanyID: coA, Title: "Engineer"},
			b:    Candidate{CompanyID: coA, Title: "Engineer"},
			same: false,
		},
	}
}

func TestDedupeEvalSet(t *testing.T) {
	set := evalSet()

	var tp, fp, fn, tn int
	for _, p := range set {
		v := Decide(p.a, p.b)
		switch {
		case v.Same && p.same:
			tp++
		case v.Same && !p.same:
			fp++
			// A false merge hides a real job from the user permanently, and
			// nothing surfaces the mistake. This is the failure that matters.
			t.Errorf("FALSE MERGE (%s): reason=%s confidence=%.2f", p.name, v.Reason, v.Confidence)
		case !v.Same && p.same:
			fn++
			t.Logf("missed merge (%s) — shows a duplicate, self-evident to the user", p.name)
		default:
			tn++
		}
		// Symmetry: the verdict must not depend on argument order.
		if rev := Decide(p.b, p.a); rev.Same != v.Same {
			t.Errorf("%s: asymmetric verdict (%v vs %v)", p.name, v.Same, rev.Same)
		}
	}

	precision, recall := 1.0, 1.0
	if tp+fp > 0 {
		precision = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		recall = float64(tp) / float64(tp+fn)
	}
	t.Logf("precision=%.3f recall=%.3f  (tp=%d fp=%d fn=%d tn=%d)", precision, recall, tp, fp, fn, tn)

	// Precision is the hard gate: false merges are the costly error.
	if precision < 1.0 {
		t.Errorf("precision %.3f < 1.000: a false merge is not an acceptable trade", precision)
	}
	if recall < 0.75 {
		t.Errorf("recall %.3f below floor: too many duplicates reaching users", recall)
	}
}

func TestDecideRequiresSameCompany(t *testing.T) {
	c := Candidate{CompanyID: coA, ATSType: gh, ATSJobID: "1"}
	d := Candidate{CompanyID: coB, ATSType: gh, ATSJobID: "1"}
	if Decide(c, d).Same {
		t.Fatal("merged across companies on a matching ATS id")
	}
	// Empty company must never merge either: unknown identity is not shared identity.
	if Decide(Candidate{ATSType: gh, ATSJobID: "1"}, Candidate{ATSType: gh, ATSJobID: "1"}).Same {
		t.Fatal("merged two candidates with no company")
	}
}

func TestNormalizeURL(t *testing.T) {
	const want = "careers.a.com/jobs/1"
	cases := map[string]string{
		"https://careers.a.com/jobs/1?utm=x": want,
		"http://careers.a.com/jobs/1/":       want,
		"HTTPS://Careers.A.com/Jobs/1#apply": want,
	}
	for in, want := range cases {
		got, ok := normalizeURL(in)
		if !ok || got != want {
			t.Errorf("normalizeURL(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "   ", "https://careers.a.com"} {
		if _, ok := normalizeURL(bad); ok {
			t.Errorf("normalizeURL(%q) should be unusable", bad)
		}
	}
}

func TestShinglesHandleShortText(t *testing.T) {
	if got := Shingles(""); got != nil {
		t.Errorf("empty text produced %v", got)
	}
	if got := Shingles("two words"); len(got) != 1 {
		t.Errorf("short text produced %d shingles, want 1", len(got))
	}
	if got := Shingles(strings.Repeat("word ", 10)); len(got) != 8 {
		t.Errorf("10 words produced %d shingles, want 8", len(got))
	}
}

func TestTokenizeKeepsSkillTokens(t *testing.T) {
	got := fmt.Sprint(tokenize("Experience with C++ and C# required"))
	for _, want := range []string{"c++", "c#"} {
		if !strings.Contains(got, want) {
			t.Errorf("tokenize dropped %q: %s", want, got)
		}
	}
}

// TestSimHashCalibration is the evidence behind MaxHamming. If these distances
// drift — because the shingle size, tokenizer or hash changes — the threshold
// must be re-derived rather than left to coincidence.
func TestSimHashCalibration(t *testing.T) {
	const intro0 = "About the company. We are a global technology organisation with teams " +
		"distributed across sixty countries. Benefits include health cover, parental " +
		"leave, a home office budget and a learning stipend. We are an equal " +
		"opportunity employer and welcome applicants from every background. "
	base := intro0 + "What you will do: design, build and operate backend services in Go " +
		"backed by PostgreSQL. You will own reliability end to end, including on-call, " +
		"capacity planning and incident review."
	h0 := SimHash(base)

	sameJob := []struct {
		name string
		text string
	}{
		{"one word added", base + " You will mentor other engineers too."},
		{"paragraph appended", base + " Our stack includes Kubernetes, Kafka and gRPC."},
		{"salary line appended", base + " Salary range: $150,000 - $190,000."},
	}
	for _, c := range sameJob {
		d := Hamming(h0, SimHash(c.text))
		t.Logf("same posting, %-22s hamming=%d", c.name, d)
		if d > MaxHamming {
			t.Errorf("%s: distance %d exceeds MaxHamming %d — real reposts will be missed",
				c.name, d, MaxHamming)
		}
	}

	// The negative case that matters is NOT unrelated text — it is two postings
	// from the SAME company that share a long boilerplate intro and differ only
	// in the role section. Boilerplate compresses the distance (34 bits without a
	// shared intro, 21 with one), so calibrating against unrelated text would
	// leave the threshold far too loose for real data.
	const intro = "About the company. We are a global technology organisation with teams " +
		"distributed across sixty countries. Benefits include health cover, parental " +
		"leave, a home office budget and a learning stipend. We are an equal " +
		"opportunity employer and welcome applicants from every background. "

	different := []struct {
		name string
		text string
	}{
		{"shared boilerplate, marketing role", intro +
			"What you will do: own demand generation across paid search, paid social and " +
			"lifecycle email. You will build the campaign calendar, manage agency " +
			"relationships and the media budget, and report pipeline contribution."},
		{"no shared boilerplate, finance role", "We are hiring a financial analyst to own " +
			"budgeting, forecasting and month-end close for a global engineering organisation."},
	}
	for _, c := range different {
		d := Hamming(h0, SimHash(c.text))
		t.Logf("different posting, %-19s hamming=%d", c.name, d)
		if d <= MaxHamming {
			t.Errorf("%s: distance %d is within MaxHamming %d — unrelated text would merge",
				c.name, d, MaxHamming)
		}
	}
}

// Regression from real data: one company posted "Forward Deployed Engineer -
// EMEA" twice with byte-identical descriptions and identical titles, but the two
// requisitions covered different countries. An earlier version merged them,
// which hid one opening from every candidate in the countries it dropped.
func TestDifferentGeoScopeIsNotTheSameJob(t *testing.T) {
	body := desc("Forward Deployed Engineer - EMEA",
		"You will work with strategic customers across the region to deploy the platform.")
	base := Candidate{
		CompanyID: coA, Title: "Forward Deployed Engineer - EMEA",
		ContentHash: []byte("identical"), SimHash: SimHash(body), TextLen: len(body),
	}

	a, b := base, base
	a.ATSJobID, b.ATSJobID = "8522265002", "8522408002"
	a.GeoScope = "DE,DK,FR,GB,IE,IT,NL,SE"
	b.GeoScope = "AT,CH,DE,DK,FR,GB,IE,NL"

	if v := Decide(a, b); v.Same {
		t.Errorf("merged two requisitions with different country scope (reason=%s)", v.Reason)
	}

	// Same scope, same everything else: this pair IS one job.
	b.GeoScope = a.GeoScope
	if v := Decide(a, b); !v.Same {
		t.Errorf("identical postings with identical scope were not merged (reason=%s)", v.Reason)
	}

	// An exact ATS id match stays conclusive: it is literally the same posting
	// record, so a scope difference means the scope was edited, not that there
	// are two openings.
	c, d := a, b
	c.ATSType, d.ATSType = gh, gh
	c.ATSJobID, d.ATSJobID = "999", "999"
	c.GeoScope, d.GeoScope = "US", "CA,US"
	if v := Decide(c, d); !v.Same || v.Reason != ReasonExactATS {
		t.Errorf("exact ATS match should override a scope change, got same=%v reason=%s", v.Same, v.Reason)
	}
}

// Unknown scope must not be treated as a difference: we never manufacture a
// distinction we cannot observe.
func TestUnknownGeoScopeIsNotAConflict(t *testing.T) {
	body := desc("Senior Backend Engineer", "You will own service reliability end to end.")
	base := Candidate{
		CompanyID: coA, Title: "Senior Backend Engineer",
		ContentHash: []byte("same"), SimHash: SimHash(body), TextLen: len(body),
	}
	a, b := base, base
	a.GeoScope = "US"
	b.GeoScope = "" // source did not say
	if v := Decide(a, b); !v.Same {
		t.Errorf("an unknown scope blocked a valid merge (reason=%s)", v.Reason)
	}
}
