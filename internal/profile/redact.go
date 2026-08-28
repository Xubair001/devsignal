package profile

import (
	"regexp"
	"sort"
	"strings"
)

// RedactionVersion travels with every extraction made from redacted text.
//
// Changing what leaves our boundary is a change to a promise we made to users,
// so it is versioned and recorded on the artifact rather than being an
// implementation detail nobody can date.
// Bumped when the header drop moved from line-counting to heading-detection.
// The version is on the artifact precisely so a re-extraction happens: every
// resume redacted under the old version had its name sent, and re-running is how
// the newer, narrower field set gets recorded.
const RedactionVersion = "redact-2026-08-27b"

// Redaction is what was removed, for the record.
//
// Counts, never the values. Recording "3 email addresses removed" is auditable;
// recording which ones would put the PII in the audit trail we built to prove we
// did not keep it.
//
// The counts are per-PATTERN and the patterns overlap, so they are not a
// partition of what was removed and do not sum to it. A phone number written
// "+49 30 12345678" is counted under LongDigits, because that pattern runs
// first; one inside the dropped header is counted under neither, because
// HeaderLines already accounts for the whole block. Stated rather than papered
// over: a record that implies more precision than it has is the kind of thing
// that gets quoted back during an audit.
type Redaction struct {
	Emails     int
	Phones     int
	URLs       int
	LongDigits int
	// HeaderChars is how much of the leading header block was dropped, and
	// HeaderBy says how it was located. A resume opens with a name and contact
	// block, and that block is what the pattern redactions below cannot catch —
	// a name matches nothing.
	HeaderChars int
	// HeaderBy is "heading" when a section heading was found and everything
	// before it dropped, "prefix" when none was found and a bounded leading
	// prefix was dropped instead, or "none" when the document was too short to
	// do either. Recorded because the three are different strengths of promise.
	HeaderBy string
	// InChars and OutChars bound how much was sent.
	InChars  int
	OutChars int
}

var (
	// Deliberately broad. A false positive costs a skill mention; a false
	// negative sends someone's email to a third party.
	reEmail = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	// International and domestic shapes, including spaced and dotted groups.
	rePhone = regexp.MustCompile(
		`(?:\+?\d{1,3}[\s.\-]?)?(?:\(\d{1,4}\)[\s.\-]?)?\d{2,4}(?:[\s.\-]\d{2,4}){1,4}`)
	// A profile URL carries a name in its path far more often than not.
	//
	// Two alternatives, because a resume writes them both ways. The second — a
	// dotted host with a TLD, followed by a path — is what catches
	// "linkedin.com/in/firstname-lastname", which has no scheme and no www and
	// which the first alternative missed entirely. That one only appeared to be
	// handled because the sample happened to put it in the dropped header; in the
	// body it would have leaked.
	//
	// The TLD is required to be alphabetic and at least two characters so a
	// version or a ratio ("3.5/5", "v1.2/beta") is not mistaken for a host.
	reURL = regexp.MustCompile(
		`(?i)\b(?:(?:https?://|www\.)\S+|[a-z0-9\-]+(?:\.[a-z0-9\-]+)*\.[a-z]{2,}/\S*)`)
	// Long digit runs: national IDs, account numbers, passport numbers.
	reLongDigits = regexp.MustCompile(`\b\d{7,}\b`)
	reBlankRun   = regexp.MustCompile(`\n{3,}`)
	reSpaceRun   = regexp.MustCompile(`[ \t]{2,}`)
)

// headingWords are the section headings a resume opens its content with.
//
// Locating the header SEMANTICALLY rather than by line count, because the stored
// text has no lines: profile.cleanText collapses every newline to a space before
// the text is saved, so an earlier line-counting version of this function was a
// complete no-op in production and the candidate's NAME was reaching the model.
// That is the bug this list exists to fix, and it is worth stating plainly
// because the failure was silent — the redaction record cheerfully reported
// "6 header lines dropped" for a one-line document.
var headingWords = []string{
	"summary", "professional summary", "profile", "objective", "about me",
	"experience", "work experience", "professional experience", "employment",
	"employment history", "work history", "career history",
	"skills", "technical skills", "core competencies", "education",
}

// How the header block was located, recorded on every redaction. The three are
// different strengths of promise, so they are named rather than spelled inline.
const (
	HeaderByHeading = "heading"
	HeaderByPrefix  = "prefix"
	HeaderByNone    = "none"
)

// headerPrefixChars is dropped when no heading can be found.
//
// A fallback, and a blunt one: a resume's opening 200 characters are
// overwhelmingly a name and a contact block. Losing 200 characters of skill text
// in the rare document that opens differently is a far smaller cost than sending
// someone's name to a third party, so this fails toward removing too much.
const headerPrefixChars = 200

// minAfterHeader is how much document must survive for a header drop to happen
// at all. Dropping the opening of a very short file would leave nothing to
// extract, and an empty extraction is indistinguishable from a model failure.
const minAfterHeader = 200

var reHeading = buildHeadingPattern()

func buildHeadingPattern() *regexp.Regexp {
	// Word-boundary anchored and case-insensitive. Ordered longest-first so
	// "professional experience" wins over "experience" and the drop is not cut
	// short at the adjective.
	alts := make([]string, 0, len(headingWords))
	for _, w := range headingWords {
		alts = append(alts, regexp.QuoteMeta(w))
	}
	sort.Slice(alts, func(i, j int) bool { return len(alts[i]) > len(alts[j]) })
	return regexp.MustCompile(`(?i)\b(` + strings.Join(alts, "|") + `)\b`)
}

// dropHeader removes the leading name-and-contact block.
func dropHeader(text string) (string, int, string) {
	if len(text) < minAfterHeader*2 {
		return text, 0, HeaderByNone
	}

	// Only look in the opening third: "Education" appears near the END of most
	// resumes, and matching there would delete the entire document.
	window := len(text) / 3
	if window < headerPrefixChars {
		window = headerPrefixChars
	}
	if window > len(text) {
		window = len(text)
	}

	if m := reHeading.FindStringIndex(text[:window]); m != nil {
		// Everything before the first heading is the header block.
		if len(text)-m[0] >= minAfterHeader {
			return text[m[0]:], m[0], HeaderByHeading
		}
	}

	if len(text)-headerPrefixChars >= minAfterHeader {
		return text[headerPrefixChars:], headerPrefixChars, HeaderByPrefix
	}
	return text, 0, HeaderByNone
}

// Redact removes personal detail from resume text before it leaves our boundary.
//
// Privacy rule 2: never send a whole resume to extract skills from one section.
// What is removed, precisely, so the promise is checkable rather than reassuring:
//
//   - the leading header block, located by the first section heading
//     ("Summary", "Experience", "Skills"…) and falling back to the opening 200
//     characters when no heading is found. This is what removes the NAME, which
//     no pattern below can match
//   - every email address
//   - every phone-shaped number
//   - every URL (a profile link's path is usually a name)
//   - every run of 7 or more digits: national ids, account and passport numbers
//
// What may REMAIN, stated because a vague promise is worse than a narrow one:
// employer names, job titles, dates, city names appearing in body text, and a
// name written in prose ("I led..."). Those are not reliably separable from the
// skills we are extracting, and claiming otherwise would be the invented
// guarantee this codebase exists to avoid. The mitigation is that the field set
// sent is recorded, and the provider's retention terms are a stated decision.
func Redact(text string) (string, Redaction) {
	r := Redaction{InChars: len(text)}

	// Counted against the ORIGINAL, not against the post-header text.
	//
	// The header drop removes a name and contact block wholesale, so counting
	// afterwards reported "1 email removed" for a document that had two and two
	// URLs — an audit record that understates what it removed is worse than no
	// record, because it reads as a smaller promise being kept.
	r.Emails = len(reEmail.FindAllString(text, -1))
	r.URLs = len(reURL.FindAllString(text, -1))
	r.LongDigits = len(reLongDigits.FindAllString(text, -1))

	out, dropped, by := dropHeader(text)
	r.HeaderChars, r.HeaderBy = dropped, by

	out = reEmail.ReplaceAllString(out, " ")
	out = reURL.ReplaceAllString(out, " ")
	out = reLongDigits.ReplaceAllString(out, " ")

	// Phones last, and counted here rather than up front: the pattern is loose
	// enough to match parts of a URL or a long id, so counting it before those
	// were claimed would double-count the same characters.
	r.Phones = len(rePhone.FindAllString(out, -1))
	out = rePhone.ReplaceAllString(out, " ")

	out = reSpaceRun.ReplaceAllString(out, " ")
	out = reBlankRun.ReplaceAllString(out, "\n\n")
	out = strings.TrimSpace(out)

	r.OutChars = len(out)
	return out, r
}

// FieldSet names what left our boundary, for the record.
//
// The blueprint's rule is that we define what may leave; this is that
// definition, in a form that goes into a database column rather than a document.
func (r Redaction) FieldSet() string {
	return "resume_text:redacted(" + RedactionVersion + ")"
}
