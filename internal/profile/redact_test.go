package profile

import (
	"strings"
	"testing"
)

// A resume in the conventional shape: name, contact block, then content.
const sampleResume = `Amara Okonkwo
Berlin, Germany
amara.okonkwo@example.com
+49 30 12345678
linkedin.com/in/amara-okonkwo
https://github.com/amaraok
National ID 8837219940

SUMMARY
Senior backend engineer with 9 years building payment systems.

EXPERIENCE
Staff Engineer, Unlimit (2021 - present)
  Led the migration from Ruby on Rails to Go. Owned PostgreSQL schema design
  and introduced Kubernetes for the payments tier.
  Reachable at a.okonkwo@unlimit.example for references.

Senior Engineer, GitLab (2018 - 2021)
  Built CI/CD tooling in Go. Ran incident response on call.

SKILLS
Go, PostgreSQL, Kubernetes, Terraform, Kafka, gRPC
`

// TestRedactRemovesEverythingItPromisesTo.
//
// This is the promise, so it is the test. Privacy rule 2: never send a whole
// resume to extract skills from one section. Each assertion here corresponds to
// one line of the documented list in Redact's comment — if the list and the
// behaviour drift, this fails.
func TestRedactRemovesEverythingItPromisesTo(t *testing.T) {
	out, r := Redact(sampleResume)

	mustNotContain := map[string]string{
		"amara.okonkwo@example.com":     "an email address in the contact block",
		"a.okonkwo@unlimit.example":     "an email address in the BODY, not just the header",
		"linkedin.com/in/amara-okonkwo": "a profile URL whose path is a name",
		"github.com/amaraok":            "a profile URL",
		"8837219940":                    "a national id",
		"+49 30 12345678":               "a phone number",
	}
	for needle, why := range mustNotContain {
		if strings.Contains(out, needle) {
			t.Errorf("redacted text still contains %s (%q)", why, needle)
		}
	}

	// The name appears on line 1, inside the dropped header.
	if strings.Contains(out, "Amara Okonkwo") {
		t.Error("the name survived the header drop")
	}

	if r.Emails < 2 {
		t.Errorf("counted %d emails, expected at least 2 (header and body)", r.Emails)
	}
	if r.URLs < 2 {
		t.Errorf("counted %d URLs, expected at least 2", r.URLs)
	}
	if r.HeaderChars == 0 {
		t.Error("no header block was dropped, so the name was not removed")
	}
	if r.HeaderBy != HeaderByHeading {
		t.Errorf("header located by %q, want %q for a document with a SUMMARY",
			r.HeaderBy, HeaderByHeading)
	}
	if r.OutChars >= r.InChars {
		t.Error("redaction did not shorten the document")
	}
}

// TestRedactKeepsTheSkillsTheTaskIsFor.
//
// A redactor that strips the technology names is worse than useless — it would
// silently produce an empty extraction and look like a model failure. This is
// the other half of the contract.
func TestRedactKeepsTheSkillsTheTaskIsFor(t *testing.T) {
	out, _ := Redact(sampleResume)
	for _, want := range []string{
		"Go", "PostgreSQL", "Kubernetes", "Terraform", "Kafka", "gRPC",
		"Ruby on Rails", "CI/CD", "incident response",
		"backend engineer", "payment systems",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("redaction removed %q, which is what the extraction is FOR", want)
		}
	}
}

// TestRedactDoesNotGutAShortDocument.
//
// Dropping six lines from an eight-line file would leave nothing to extract, and
// an empty extraction is indistinguishable from a model that returned nothing.
func TestRedactDoesNotGutAShortDocument(t *testing.T) {
	short := "Jane Doe\njane@example.com\n\nSKILLS\nGo, Rust, Python\n"
	out, r := Redact(short)

	if r.HeaderBy != HeaderByNone {
		t.Errorf("a short document had its header dropped by %q", r.HeaderBy)
	}
	// The precise redactions still apply.
	if strings.Contains(out, "jane@example.com") {
		t.Error("an email survived in a short document")
	}
	for _, want := range []string{"Go", "Rust", "Python"} {
		if !strings.Contains(out, want) {
			t.Errorf("a short document lost %q", want)
		}
	}
}

// TestRedactIsDeterministic. The extraction cache keys on the redacted text, so
// two runs over one resume must produce the same bytes or the cache never hits
// and the profile's skills flap between uploads.
func TestRedactIsDeterministic(t *testing.T) {
	a, ra := Redact(sampleResume)
	b, rb := Redact(sampleResume)
	if a != b {
		t.Error("redaction is not deterministic")
	}
	if ra != rb {
		t.Error("the redaction record is not deterministic")
	}
}

// TestRedactHandlesEmptyAndWhitespace: an unparseable PDF yields an empty text
// layer, and that must not panic on the way to a clean "nothing to extract".
func TestRedactHandlesEmptyAndWhitespace(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n\n", "\t"} {
		out, r := Redact(in)
		if strings.TrimSpace(out) != "" {
			t.Errorf("Redact(%q) produced %q", in, out)
		}
		if r.OutChars != len(out) {
			t.Error("the record disagrees with the output length")
		}
	}
}

// TestFieldSetNamesWhatLeft. The blueprint's rule is that we DEFINE what may
// leave our boundary; this is that definition in a form that goes in a column.
func TestFieldSetNamesWhatLeft(t *testing.T) {
	_, r := Redact(sampleResume)
	fs := r.FieldSet()
	if !strings.Contains(fs, RedactionVersion) {
		t.Errorf("the field set %q does not carry the redaction version", fs)
	}
	if !strings.Contains(fs, "redacted") {
		t.Errorf("the field set %q does not say the text was redacted", fs)
	}
}

// TestBareDomainProfileURLsInTheBodyAreRemoved.
//
// A regression test for a real gap. The first URL pattern required a scheme or a
// www, so "linkedin.com/in/firstname-lastname" was not matched — it only appeared
// to be handled because the sample put it in the dropped header. In the body it
// would have left our boundary with a name in it.
func TestBareDomainProfileURLsInTheBodyAreRemoved(t *testing.T) {
	body := `SUMMARY
Senior engineer.

EXPERIENCE
Worked on Go services. Portfolio at linkedin.com/in/amara-okonkwo and
github.com/amaraok/side-project. Also see mysite.co.uk/about.

SKILLS
Go, Kubernetes
`
	out, r := Redact(body)
	for _, leak := range []string{
		"linkedin.com/in/amara-okonkwo",
		"github.com/amaraok",
		"mysite.co.uk/about",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("a bare-domain URL leaked: %q", leak)
		}
	}
	if r.URLs < 3 {
		t.Errorf("counted %d URLs, expected 3", r.URLs)
	}
	// And the skills are intact.
	for _, keep := range []string{"Go", "Kubernetes", "Senior engineer"} {
		if !strings.Contains(out, keep) {
			t.Errorf("the broader URL pattern ate %q", keep)
		}
	}
}

// TestVersionsAndRatiosAreNotMistakenForHosts. The TLD requirement in the URL
// pattern exists for this: "3.5/5" is a rating, not a profile link.
func TestVersionsAndRatiosAreNotMistakenForHosts(t *testing.T) {
	in := `SUMMARY
Rated 4.5/5 on delivery. Shipped v1.2/beta and node.js services.

EXPERIENCE
Go and PostgreSQL.

SKILLS
Go
`
	out, _ := Redact(in)
	for _, keep := range []string{"4.5/5", "v1.2/beta", "node.js", "PostgreSQL"} {
		if !strings.Contains(out, keep) {
			t.Errorf("the URL pattern removed %q, which is not a URL", keep)
		}
	}
}

// TestRedactWorksOnTheONELINE_FormActuallyStored.
//
// This is a regression test for a silent privacy defect. profile.cleanText
// collapses every newline to a space before a resume's text is saved, so the
// stored document has NO LINES — and an earlier line-counting version of Redact
// was a complete no-op in production while cheerfully recording "6 header lines
// dropped". The candidate's name was reaching the model.
//
// The fixture here is deliberately the flattened form, not the pretty one.
func TestRedactWorksOnTheONELINE_FormActuallyStored(t *testing.T) {
	flat := flatten(sampleResume)
	if strings.Contains(flat, "\n") {
		t.Fatal("the fixture is not flattened; this test would not exercise the bug")
	}

	out, r := Redact(flat)

	if strings.Contains(out, "Amara Okonkwo") {
		t.Error("the NAME survived redaction of the one-line stored form")
	}
	if r.HeaderChars == 0 {
		t.Error("no header block was dropped from the one-line form")
	}
	for _, leak := range []string{
		"amara.okonkwo@example.com", "linkedin.com/in/amara-okonkwo", "8837219940",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("%q survived", leak)
		}
	}
	// And the skills still survive.
	for _, keep := range []string{"Go", "PostgreSQL", "Kubernetes", "Kafka"} {
		if !strings.Contains(out, keep) {
			t.Errorf("the one-line path removed %q", keep)
		}
	}
}

// TestEducationLateInTheDocumentDoesNotDeleteEverything.
//
// "Education" is a heading word and it appears near the END of most resumes.
// Matching it there would drop the entire document, so the search is confined to
// the opening third — this asserts that guard.
func TestEducationLateInTheDocumentDoesNotDeleteEverything(t *testing.T) {
	out, r := Redact(flatten(sampleResume))
	if r.HeaderChars > len(sampleResume)/2 {
		t.Errorf("dropped %d chars of a %d-char document; a late heading matched",
			r.HeaderChars, len(sampleResume))
	}
	if !strings.Contains(out, "SKILLS") && !strings.Contains(out, "Go") {
		t.Error("the document was gutted")
	}
}

// TestNoHeadingFallsBackToAPrefixDrop.
//
// A resume that opens straight into prose has no heading to find. Dropping a
// bounded prefix is blunt and deliberately fails toward removing too much: 200
// characters of skill text is a far smaller cost than a name reaching a third
// party.
func TestNoHeadingFallsBackToAPrefixDrop(t *testing.T) {
	in := "Amara Okonkwo, Berlin. " +
		strings.Repeat("Nine years of backend work in Go and PostgreSQL. ", 20)
	out, r := Redact(in)

	if r.HeaderBy != HeaderByPrefix {
		t.Errorf("header located by %q, want %q", r.HeaderBy, HeaderByPrefix)
	}
	if strings.Contains(out, "Amara Okonkwo") {
		t.Error("the name survived the prefix fallback")
	}
	if !strings.Contains(out, "PostgreSQL") {
		t.Error("the prefix fallback removed the whole document")
	}
}

// flatten reproduces what profile.cleanText does on the way into storage.
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
