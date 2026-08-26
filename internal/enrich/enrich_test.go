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

// TestResolvePicksProviderFromWhicheverKeyIsSet: adding one line to .env should
// be enough to turn extraction on.
func TestResolvePicksProviderFromWhicheverKeyIsSet(t *testing.T) {
	p, err := Resolve(ResolveConfig{OpenAIAPIKey: "sk-test"})
	if err != nil {
		t.Fatalf("an OpenAI key alone should resolve: %v", err)
	}
	if got := p.ModelID(); got != "openai:"+DefaultOpenAIModel {
		t.Errorf("model id %q, want the openai-prefixed default", got)
	}

	p, err = Resolve(ResolveConfig{AnthropicAPIKey: "sk-test"})
	if err != nil {
		t.Fatalf("an Anthropic key alone should resolve: %v", err)
	}
	if got := p.ModelID(); got != DefaultAnthropicModel {
		t.Errorf("model id %q, want %q", got, DefaultAnthropicModel)
	}
}

// TestTwoKeysWithoutAChoiceIsAnError.
//
// Not a precedence rule. Which vendor read a posting is part of its extraction
// cache key (hard rule 8) and part of the audit trail, so picking it by
// alphabetical accident is not a decision anyone made.
func TestTwoKeysWithoutAChoiceIsAnError(t *testing.T) {
	if _, err := Resolve(ResolveConfig{
		AnthropicAPIKey: "a", OpenAIAPIKey: "b",
	}); err == nil {
		t.Fatal("two keys and no EXTRACTION_PROVIDER resolved silently")
	}
	// An explicit choice settles it.
	p, err := Resolve(ResolveConfig{
		Provider: ProviderOpenAI, AnthropicAPIKey: "a", OpenAIAPIKey: "b",
	})
	if err != nil {
		t.Fatalf("an explicit provider should win: %v", err)
	}
	if !strings.HasPrefix(p.ModelID(), "openai:") {
		t.Errorf("explicit openai resolved to %q", p.ModelID())
	}
}

// TestModelIDIsVendorQualified guards the cache key.
//
// Two vendors can ship a model with the same name. Hard rule 8 makes the model
// id part of the determinism guarantee, so an unqualified id would let a
// provider switch silently reuse the other vendor's cached output — which is
// exactly the "fit scores flap for postings that did not change" failure the
// cache exists to prevent.
func TestModelIDIsVendorQualified(t *testing.T) {
	p, err := NewOpenAIProvider(OpenAIConfig{APIKey: "x", Model: "shared-name"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ModelID() == "shared-name" {
		t.Error("the OpenAI model id is not vendor-qualified")
	}
}

// TestNoKeyIsADistinctError: hard rule 7 needs "no model configured" to be
// separable from "the model call failed", because only one of them should
// degrade a posting rather than retry it.
func TestNoKeyIsADistinctError(t *testing.T) {
	_, err := Resolve(ResolveConfig{})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("no keys gave %v, want ErrNoProvider", err)
	}
	_, err = Resolve(ResolveConfig{Provider: ProviderNone, OpenAIAPIKey: "x"})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("provider=none gave %v, want ErrNoProvider", err)
	}
}

func TestUnknownProviderIsRejected(t *testing.T) {
	if _, err := Resolve(ResolveConfig{Provider: "gemini", OpenAIAPIKey: "x"}); err == nil {
		t.Fatal("an unknown provider was accepted; a typo must not change the model")
	}
}

// TestReasoningEffortOnlyForModelsThatTakeIt: sending the parameter to a
// non-reasoning model is a 400, and paying that per posting is not acceptable.
func TestReasoningEffortOnlyForModelsThatTakeIt(t *testing.T) {
	for _, m := range []string{"gpt-5-mini", "gpt-5", "o3", "o4-mini"} {
		if !supportsReasoningEffort(m) {
			t.Errorf("%s should accept reasoning_effort", m)
		}
	}
	for _, m := range []string{"gpt-4.1-mini", "gpt-4o", "gpt-3.5-turbo"} {
		if supportsReasoningEffort(m) {
			t.Errorf("%s does not accept reasoning_effort", m)
		}
	}
}

// TestClaudeModelNameIsRejectedByTheOpenAIProvider is a regression test for a
// real misconfiguration.
//
// EXTRACTION_MODEL is one variable shared by both providers. A .env carrying
// EXTRACTION_MODEL=claude-opus-5 from the Anthropic default, plus a newly added
// OPENAI_API_KEY, resolved to "openai:claude-opus-5" and would have 400'd on
// every posting in the corpus — silently, as a per-posting enrichment failure
// that hard rule 7 correctly degrades rather than escalates.
func TestClaudeModelNameIsRejectedByTheOpenAIProvider(t *testing.T) {
	_, err := Resolve(ResolveConfig{
		Provider: ProviderOpenAI, OpenAIAPIKey: "x", Model: "claude-opus-5",
	})
	if err == nil {
		t.Fatal("a claude model name was accepted for the openai provider")
	}
	if !strings.Contains(err.Error(), "EXTRACTION_MODEL") {
		t.Errorf("the error should name the variable to fix, got: %v", err)
	}

	// And the mirror image.
	if _, err := Resolve(ResolveConfig{
		Provider: ProviderAnthropic, AnthropicAPIKey: "x", Model: "gpt-5-mini",
	}); err == nil {
		t.Fatal("a gpt model name was accepted for the anthropic provider")
	}
}

// TestAnUnfamiliarModelNameIsAllowed: the guard rejects the other vendor's
// namespace, not everything it does not recognise. A gateway or a fine-tune can
// be called anything, and a whitelist of model names goes stale immediately.
func TestAnUnfamiliarModelNameIsAllowed(t *testing.T) {
	if _, err := Resolve(ResolveConfig{
		Provider: ProviderOpenAI, OpenAIAPIKey: "x", Model: "our-tuned-extractor-v3",
	}); err != nil {
		t.Errorf("an unfamiliar name should be allowed: %v", err)
	}
}
