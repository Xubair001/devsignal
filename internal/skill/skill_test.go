package skill

import "testing"

// TestOntologyLoadsAndIsConsistent is the guard on a hand-edited data file.
//
// Load validates rather than trusts: a duplicate slug, an alias pointing at two
// skills, an edge to a slug that does not exist, or a self-edge would all
// otherwise fail somewhere far from the cause.
func TestOntologyLoadsAndIsConsistent(t *testing.T) {
	o, err := Load()
	if err != nil {
		t.Fatalf("the committed ontology does not load: %v", err)
	}
	if len(o.Entries) < 100 {
		t.Errorf("only %d skills; the vocabulary is too small to normalize a corpus",
			len(o.Entries))
	}
	if len(o.Aliases()) < len(o.Entries)*2 {
		t.Errorf("%d aliases for %d skills; most skills have no variant spellings",
			len(o.Aliases()), len(o.Entries))
	}
}

// TestNormalizeKeepsTheCharactersThatCarryMeaning.
//
// Three characters are the whole difficulty. "C++" and "C#" are different
// languages from "C", ".NET" is not "net", and "Node.js" is "NodeJS". A
// normalizer that strips all punctuation collapses those into each other, and a
// normalizer that strips none of it misses every real variant.
func TestNormalizeKeepsTheCharactersThatCarryMeaning(t *testing.T) {
	distinct := [][2]string{
		{"C++", "C"},
		{"C#", "C"},
		{"C++", "C#"},
		{".NET", "Net"},
		{"F#", "C#"},
	}
	for _, p := range distinct {
		if Normalize(p[0]) == Normalize(p[1]) {
			t.Errorf("%q and %q normalize to the same key %q",
				p[0], p[1], Normalize(p[0]))
		}
	}

	same := [][2]string{
		{"Node.js", "NodeJS"},
		{"Node.js", "node js"},
		{"Next.js", "NextJS"},
		{"C++", "c++"},
		{"C++", "  C++  "},
		{"CI/CD", "CI CD"},
		{"REST APIs", "rest apis"},
		{"Go programming language", "Go"},
		{"React framework", "react"},
		{"Kubernetes", "kubernetes"},
	}
	for _, p := range same {
		if Normalize(p[0]) != Normalize(p[1]) {
			t.Errorf("%q -> %q but %q -> %q; these are the same skill",
				p[0], Normalize(p[0]), p[1], Normalize(p[1]))
		}
	}
}

// TestResolveTheVariantsPostingsActuallyWrite.
//
// Every left-hand string here is a spelling that appeared in, or is one letter
// away from, a real posting in the corpus. The point of the ontology is that all
// of them reach one row.
func TestResolveTheVariantsPostingsActuallyWrite(t *testing.T) {
	o, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"Go":                     "go",
		"Golang":                 "go",
		"golang":                 "go",
		"Go (Golang)":            "go",
		"Go programming":         "go",
		"PostgreSQL":             "postgresql",
		"Postgres":               "postgresql",
		"postgres":               "postgresql",
		"PG":                     "postgresql",
		"Kubernetes (K8s)":       "kubernetes",
		"K8s":                    "kubernetes",
		"Node.js":                "nodejs",
		"NodeJS":                 "nodejs",
		"node":                   "nodejs",
		"TypeScript":             "typescript",
		"TS":                     "typescript",
		"CI/CD":                  "cicd",
		"continuous integration": "cicd",
		"Continuous Delivery":    "cicd",
		"source code management": "git",
		"version control":        "git",
		"AWS":                    "aws",
		"Amazon Web Services":    "aws",
		"Google Cloud":           "gcp",
		"Ruby on Rails":          "rails",
		"RoR":                    "rails",
		"REST API":               "rest",
		"RESTful APIs":           "rest",
		"Agile Planning":         "agile",
		"Scrum":                  "agile",
		"documentation":          "technical-writing",
		"distributed systems":    "system-design",
		"DevSecOps":              "devsecops",
		"Salesforce":             "salesforce",
		"SFDC":                   "salesforce",
		"strategic partnerships": "partner-management",
		"GSA schedules":          "public-sector-sales",
		"Visa":                   "card-schemes",
		"Mastercard":             "card-schemes",
		"technical support":      "customer-support",
		"OAuth2":                 "iam",
		"SSO":                    "iam",
		"Tailwind":               "tailwindcss",
		"generative AI":          "llm",
		"LLMs":                   "llm",
	}
	for raw, want := range cases {
		got, ok := o.Resolve(raw)
		if !ok {
			t.Errorf("%q did not resolve; expected %q", raw, want)
			continue
		}
		if got != want {
			t.Errorf("%q resolved to %q, want %q", raw, got, want)
		}
	}
}

// TestUnknownSkillsDoNotResolve.
//
// Resolve returning false is not a failure — extraction keeps the unknown as its
// own skill so nothing paid for is discarded, and the unknowns stay visible for
// review. What would be a failure is resolving a phrase to something unrelated,
// which is worse than not knowing.
func TestUnknownSkillsDoNotResolve(t *testing.T) {
	o, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"hands-on labs", "partner portals", "an entirely invented technology",
		"", "   ",
	} {
		if slug, ok := o.Resolve(raw); ok {
			t.Errorf("%q resolved to %q; a wrong match is worse than no match",
				raw, slug)
		}
	}
}

// TestPrerequisiteEdgesPointFromSpecificToGeneral.
//
// The direction matters for anything that later reads the graph: knowing React
// implies knowing JavaScript, not the reverse. An inverted edge would make a
// gap analysis recommend learning React to someone who needs JavaScript.
func TestPrerequisiteEdgesPointFromSpecificToGeneral(t *testing.T) {
	o, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"react": "javascript", "nextjs": "react", "rails": "ruby",
		"django": "python", "helm": "kubernetes", "pgvector": "postgresql",
	}
	have := map[string]string{}
	for _, e := range o.Edges {
		if e.Relation == "prerequisite" {
			have[e.From] = e.To
		}
	}
	for from, to := range want {
		if have[from] != to {
			t.Errorf("prerequisite %s -> %s missing (found %q)", from, to, have[from])
		}
	}
}
