package enrich

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validJSON(t *testing.T, r Result) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func goodResult() Result {
	return Result{
		Seniority: "senior", RoleFamily: "backend", EmploymentType: "full_time",
		RemotePolicy: "remote",
		Skills: []Skill{
			{Name: "Go", Level: LevelRequired},
			{Name: "PostgreSQL", Level: LevelRequired},
			{Name: "Kubernetes", Level: LevelPreferred},
		},
		Responsibilities: []string{"own service reliability"},
		Requirements:     []string{"5+ years backend experience"},
	}
}

func TestValidateAcceptsAWellFormedResult(t *testing.T) {
	got, err := Validate(validJSON(t, goodResult()))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(got.Skills) != 3 || got.Seniority != "senior" {
		t.Errorf("round trip lost data: %+v", got)
	}
}

// The schema cannot express a contradiction between two fields, so validation
// must. An unsupported salary claim reaching a user is the error they forgive
// least.
func TestValidateRejectsSalaryContradictions(t *testing.T) {
	stated := goodResult()
	stated.SalaryStated = true // but no text
	if _, err := Validate(validJSON(t, stated)); !errors.Is(err, ErrInvalidOutput) {
		t.Errorf("salary_stated with empty text was accepted: %v", err)
	}

	texted := goodResult()
	texted.SalaryText = "$150k-$190k" // but not claimed as stated
	if _, err := Validate(validJSON(t, texted)); !errors.Is(err, ErrInvalidOutput) {
		t.Errorf("salary_text without salary_stated was accepted: %v", err)
	}

	both := goodResult()
	both.SalaryStated = true
	both.SalaryText = "$150,000 - $190,000 per year"
	if _, err := Validate(validJSON(t, both)); err != nil {
		t.Errorf("a consistent salary pair was rejected: %v", err)
	}
}

func TestValidateRejectsBadSkills(t *testing.T) {
	blank := goodResult()
	blank.Skills = []Skill{{Name: "   ", Level: LevelRequired}}
	if _, err := Validate(validJSON(t, blank)); !errors.Is(err, ErrInvalidOutput) {
		t.Error("a skill with a blank name was accepted")
	}

	badLevel := goodResult()
	badLevel.Skills = []Skill{{Name: "Go", Level: "essential"}}
	if _, err := Validate(validJSON(t, badLevel)); !errors.Is(err, ErrInvalidOutput) {
		t.Error("an invented requirement level was accepted")
	}
}

// Unknown fields mean the model answered a different question. Ignoring them is
// how junk reaches the score.
func TestValidateRejectsUnknownFields(t *testing.T) {
	raw := `{"seniority":"senior","role_family":"backend","employment_type":"full_time",
	  "remote_policy":"remote","years_experience_min":null,"skills":[],
	  "responsibilities":[],"requirements":[],"salary_stated":false,"salary_text":"",
	  "estimated_salary":"$180k"}`
	if _, err := Validate([]byte(raw)); !errors.Is(err, ErrInvalidOutput) {
		t.Error("an unexpected field was silently ignored")
	}
}

func TestValidateRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "not json", "[]", "{", `{"seniority":123}`} {
		if _, err := Validate([]byte(bad)); err == nil {
			t.Errorf("accepted garbage: %q", bad)
		}
	}
}

func TestValidateRejectsImpossibleExperience(t *testing.T) {
	r := goodResult()
	huge := 200
	r.YearsExperience = &huge
	if _, err := Validate(validJSON(t, r)); !errors.Is(err, ErrInvalidOutput) {
		t.Error("200 years of experience was accepted")
	}
}

// The schema is the API-side guard, so its shape is a contract.
func TestJSONSchemaClosesEveryObject(t *testing.T) {
	sch := JSONSchema()
	if sch["additionalProperties"] != false {
		t.Error("top-level object allows extra properties")
	}
	props, ok := sch["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	skills, ok := props["skills"].(map[string]any)
	if !ok {
		t.Fatal("schema has no skills array")
	}
	items, ok := skills["items"].(map[string]any)
	if !ok {
		t.Fatal("skills has no item schema")
	}
	if items["additionalProperties"] != false {
		t.Error("skill objects allow extra properties")
	}
	// "unknown" must be selectable on every enum field, or the model is forced to
	// guess. Driven from EnumFields so adding a field cannot bypass this check.
	for _, field := range EnumFields {
		f, ok := props[field].(map[string]any)
		if !ok {
			t.Fatalf("%s missing from schema", field)
		}
		vals, _ := f["enum"].([]string)
		var hasUnknown bool
		for _, v := range vals {
			if v == UnknownValue {
				hasUnknown = true
			}
		}
		if !hasUnknown {
			t.Errorf("%s cannot be reported as unknown; the model is forced to guess", field)
		}
	}
}

// The instruction prefix must be byte-stable or prompt caching silently stops
// working, and nothing volatile may appear in it.
func TestInstructionsAreStableAndCacheable(t *testing.T) {
	// Prompt caching needs a prefix long enough to be worth caching at all.
	if len(Instructions) < 200 {
		t.Error("prefix is too short to be worth caching")
	}
	// Nothing volatile may appear here: a timestamp or id would change the bytes
	// on every call and silently disable caching.
	for _, volatile := range []string{"2026-", "http://", "id=", "{{"} {
		if strings.Contains(Instructions, volatile) {
			t.Errorf("prefix contains something volatile (%q); it must be byte-identical "+
				"on every call", volatile)
		}
	}
	// The rules the output depends on must actually be stated. Whitespace is
	// normalized first, because the prefix is hard-wrapped for readability and a
	// rule split across lines is still stated.
	flat := strings.Join(strings.Fields(Instructions), " ")
	for _, rule := range []string{
		"unknown", "Never estimate a salary", "must be null unless",
		"Never infer, estimate, or fill gaps",
	} {
		if !strings.Contains(flat, rule) {
			t.Errorf("prefix does not state the %q rule", rule)
		}
	}
}

func TestSlugifyIsConservative(t *testing.T) {
	cases := map[string]string{
		"Go":                  "go",
		"React.js":            "react",
		"React":               "react",
		"C++":                 "cpp",
		"C#":                  "csharp",
		".NET":                "dotnet",
		"Node.js":             "node",
		"Amazon Web Services": "amazon-web-services",
		"  Kubernetes  ":      "kubernetes",
		"":                    "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	// Deliberately NOT merged: guessing synonymy is how distinct skills collapse
	// into one. Real synonyms belong in the curated alias table.
	if Slugify("Postgres") == Slugify("PostgreSQL") {
		t.Error("Postgres and PostgreSQL were merged by guesswork")
	}
	if Slugify("Java") == Slugify("JavaScript") {
		t.Error("Java and JavaScript were merged")
	}
}

func TestSlugifyIsIdempotent(t *testing.T) {
	for _, in := range []string{"React.js", "C++", "Amazon Web Services", "Go"} {
		once := Slugify(in)
		if twice := Slugify(once); twice != once {
			t.Errorf("Slugify(%q): %q -> %q", in, once, twice)
		}
	}
}
