// Package skill owns the skill ontology: the canonical vocabulary, its aliases,
// the edges between skills, and the demand time-series written from it.
//
// It exists because extraction without an ontology is not normalization. The
// model names a technology the way the posting wrote it — "Go", "Golang",
// "Go (Golang)", "golang" — and if each of those becomes its own row then the
// skill factors in `internal/matching` can never match: a profile saying "Go"
// and a posting saying "Golang" look like two unrelated skills.
//
// Measured before this existed: 10 postings produced 91 distinct skills. Almost
// nothing overlapped, so 45 of the fit model's 100 points were unreachable even
// with extraction working.
//
// The seed vocabulary is ours and reviewable. Blueprint §9 bootstraps from an
// open taxonomy (Lightcast Open Skills / ESCO) and keeps the alias layer and the
// edges on top — that layer is what this package is.
package skill

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// OntologyVersion is written on every artifact that depends on this vocabulary.
//
// Hard rule 10: everything a score depends on carries its version. Changing the
// seed changes which postings match which profiles, so a rebuild has to be
// distinguishable from a coincidence.
const OntologyVersion = "seed-2026-08-27"

// nodeJS is the canonical spelling the normalizer collapses onto. Named because
// three separate rules have to agree on it.
const nodeJS = "nodejs"

//go:embed ontology.json
var ontologyFS embed.FS

// Entry is one canonical skill and the names the world writes it under.
type Entry struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	// Aliases are matched after normalization, so casing, punctuation and
	// spacing variants do not need to be listed.
	Aliases []string `json:"aliases"`
	// Family groups the vocabulary for review. Not used in scoring — a skill's
	// family is not evidence about a person.
	Family string `json:"family"`
}

// Edge is a relation between two canonical skills.
type Edge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
}

type ontologyFile struct {
	Skills []Entry `json:"skills"`
	Edges  []Edge  `json:"edges"`
}

// Ontology is the loaded vocabulary.
type Ontology struct {
	Entries []Entry
	Edges   []Edge
	// byAlias maps a NORMALIZED alias to a canonical slug. One alias may not map
	// to two skills, or normalization stops being a function — enforced at load
	// rather than trusted, because the seed is hand-edited.
	byAlias map[string]string
}

// Load reads the embedded ontology and validates it.
func Load() (*Ontology, error) {
	b, err := ontologyFS.ReadFile("ontology.json")
	if err != nil {
		return nil, fmt.Errorf("skill: reading ontology: %w", err)
	}
	var f ontologyFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("skill: parsing ontology: %w", err)
	}

	o := &Ontology{
		Entries: f.Skills, Edges: f.Edges,
		byAlias: make(map[string]string, len(f.Skills)*4),
	}
	slugs := make(map[string]bool, len(f.Skills))
	for _, e := range f.Skills {
		if e.Slug == "" || e.DisplayName == "" {
			return nil, fmt.Errorf("skill: entry with an empty slug or display name: %+v", e)
		}
		if slugs[e.Slug] {
			return nil, fmt.Errorf("skill: duplicate slug %q", e.Slug)
		}
		slugs[e.Slug] = true

		// The display name and the slug are themselves aliases. Not listing them
		// by hand is what keeps the file readable, and forgetting to list one is
		// the mistake that would make a skill unmatchable by its own name.
		for _, a := range append([]string{e.Slug, e.DisplayName}, e.Aliases...) {
			n := Normalize(a)
			if n == "" {
				continue
			}
			if other, dup := o.byAlias[n]; dup && other != e.Slug {
				return nil, fmt.Errorf(
					"skill: alias %q maps to both %q and %q; one alias must resolve to "+
						"one skill or normalization is not a function", a, other, e.Slug)
			}
			o.byAlias[n] = e.Slug
		}
	}

	for _, ed := range f.Edges {
		if !slugs[ed.From] || !slugs[ed.To] {
			return nil, fmt.Errorf("skill: edge %s->%s references an unknown slug",
				ed.From, ed.To)
		}
		if ed.From == ed.To {
			return nil, fmt.Errorf("skill: self-edge on %q", ed.From)
		}
		switch ed.Relation {
		case "prerequisite", "related", "supersedes":
		default:
			return nil, fmt.Errorf("skill: unknown relation %q", ed.Relation)
		}
	}
	return o, nil
}

// Resolve maps a raw extracted name to a canonical slug.
//
// Returns ok=false when the name is not in the vocabulary. The caller decides
// what to do with that — extraction records it as its own skill so nothing is
// lost, which keeps the unknowns visible and reviewable instead of discarding
// evidence we paid for.
func (o *Ontology) Resolve(raw string) (slug string, ok bool) {
	n := Normalize(raw)
	if n == "" {
		return "", false
	}
	if s, hit := o.byAlias[n]; hit {
		return s, true
	}
	// One retry against a de-parenthesised form. "Go (Golang)" and
	// "Kubernetes (K8s)" are how postings actually write these, and both halves
	// are usually already in the vocabulary.
	if inner := insideParens(raw); inner != "" {
		if s, hit := o.byAlias[Normalize(inner)]; hit {
			return s, true
		}
	}
	if outer := beforeParens(raw); outer != "" && outer != raw {
		if s, hit := o.byAlias[Normalize(outer)]; hit {
			return s, true
		}
	}
	return "", false
}

// Aliases returns every normalized alias, for seeding the database.
func (o *Ontology) Aliases() map[string]string { return o.byAlias }

var (
	parens   = regexp.MustCompile(`\(([^)]*)\)`)
	multiSpc = regexp.MustCompile(`\s+`)
	// " js" attached to a preceding word. Postings write "Node.js", "NodeJS" and
	// "node js" interchangeably, and the dotted rules above only catch the first
	// two. One rule covers the whole family rather than a pair per framework.
	spacedJS  = regexp.MustCompile(`(\S)\s+js\b`)
	dropChars = regexp.MustCompile(`[^a-z0-9+#. ]+`)
)

func insideParens(s string) string {
	m := parens.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func beforeParens(s string) string {
	if i := strings.IndexByte(s, '('); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return ""
}

// Normalize produces the lookup key for an alias.
//
// The hard part is that a few characters carry meaning in technology names and
// most do not. "C++" and "C#" are different languages from "C"; ".NET" is not
// "net"; "Node.js" and "NodeJS" are the same thing. So `+`, `#` and `.` survive
// the character filter and are then handled by explicit rules, while every other
// punctuation mark becomes a space.
//
// Deliberately NOT the same function as enrich.Slugify. That one produces a
// database slug for a name we are storing; this one produces a lookup key for a
// name we are trying to recognise, and it has to be far more aggressive. Merging
// them would mean either bad slugs or missed matches.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}

	// Explicit rules first, before punctuation is touched, because each of these
	// depends on a character the filter below would otherwise remove.
	for _, r := range []struct{ from, to string }{
		{"c++", "cpp"}, {"c#", "csharp"}, {"f#", "fsharp"},
		{"objective-c", "objectivec"},
		{".net", "dotnet"}, {"asp.net", "aspdotnet"},
		{"node.js", nodeJS}, {"next.js", "nextjs"}, {"nuxt.js", "nuxtjs"},
		{"vue.js", "vuejs"}, {"nest.js", "nestjs"}, {"express.js", "expressjs"},
		{"three.js", "threejs"}, {"d3.js", "d3"}, {"ember.js", "emberjs"},
		{"backbone.js", "backbonejs"}, {"knockout.js", "knockoutjs"},
	} {
		s = strings.ReplaceAll(s, r.from, r.to)
	}

	// Any surviving dot is a separator, not part of a name.
	s = strings.ReplaceAll(s, ".", " ")
	s = dropChars.ReplaceAllString(s, " ")
	s = multiSpc.ReplaceAllString(s, " ")
	s = spacedJS.ReplaceAllString(s, "${1}js")
	s = strings.TrimSpace(s)

	// Trailing noise words. Postings write "Go programming", "React framework",
	// "AWS cloud" and mean the technology.
	for _, suffix := range []string{
		" programming language", " programming", " language",
		" framework", " frameworks", " library", " libraries",
		" development", " experience", " skills", " knowledge",
	} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}
