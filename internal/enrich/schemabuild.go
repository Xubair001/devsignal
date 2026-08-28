package enrich

// JSON Schema keywords. Named only because they repeat across the builders
// below — the builders are what make the SCHEMAS readable, and these keep the
// builders from being a wall of string literals.
const (
	kType       = "type"
	kString     = "string"
	kFieldName  = "name"
	kFieldSkill = "skills"
	kFieldYears = "years_experience_min"
)

// Schema builders.
//
// These exist because two schemas — a posting's and a resume's — repeated the
// JSON Schema vocabulary between them: "object", "properties", "required",
// "items", "maxLength". Naming each keyword as a Go constant would satisfy a
// linter and make the schemas harder to read; a builder removes the repetition
// and states the shape instead.
//
// Every object built here is CLOSED and fully required. Both are deliberate:
// additionalProperties:false means an unexpected key is a validation failure
// rather than silently ignored data, and listing every property in `required`
// is what OpenAI's strict mode demands — nullability is expressed in the TYPE
// (`["integer","null"]`), never by omitting a field.
func object(required []string, props map[string]any) map[string]any {
	return map[string]any{
		kType:                  "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           props,
	}
}

func array(maxItems int, items map[string]any) map[string]any {
	return map[string]any{kType: "array", "maxItems": maxItems, "items": items}
}

func str(maxLen int) map[string]any {
	return map[string]any{kType: kString, "maxLength": maxLen}
}

func strWithDesc(maxLen int, desc string) map[string]any {
	return map[string]any{kType: kString, "maxLength": maxLen, "description": desc}
}

// enumOf always includes an escape hatch at the call site. "unknown" is a
// first-class answer everywhere in this system: forcing a choice is what
// produces confident nonsense.
func enumOf(vals ...string) map[string]any {
	return map[string]any{kType: kString, "enum": vals}
}

func boolean() map[string]any {
	return map[string]any{kType: "boolean"}
}

// nullableInt expresses "absent" as a null, not as a missing key.
func nullableInt(minimum, maximum int, desc string) map[string]any {
	return map[string]any{
		kType:         []string{"integer", "null"},
		"minimum":     minimum,
		"maximum":     maximum,
		"description": desc,
	}
}
