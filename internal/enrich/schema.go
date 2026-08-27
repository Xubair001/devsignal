// Package enrich turns a job description into structured facts.
//
// This is the only component billed per token, and the only one whose output is
// not deterministic. Both facts shape the design: every result is cached on a
// composite key, and the cache is what makes fit scores stable rather than
// merely what makes the bill smaller (blueprint M-05, P-01).
package enrich

// Versions. Bumping any of these invalidates the cache deliberately; nothing
// else may.
const (
	// PromptVersion changes when the instruction text changes.
	PromptVersion = "p-2026-08-27"
	// SchemaVersion changes when the output shape changes.
	SchemaVersion = "s-2026-08-24"
)

// Field names, defined once and used by both the schema and EnumFields. A field
// named in two places drifts in one of them.
const (
	FieldSeniority      = "seniority"
	FieldRoleFamily     = "role_family"
	FieldEmploymentType = "employment_type"
	FieldRemotePolicy   = "remote_policy"
)

// EnumFields are the fields constrained to a fixed vocabulary. Every one of them
// must offer "unknown", or the model is forced to guess — which is how confident
// nonsense gets into the score. Exported so the schema and its tests share one
// source of truth rather than restating the list.
var EnumFields = []string{FieldSeniority, FieldRoleFamily, FieldEmploymentType, FieldRemotePolicy}

// UnknownValue is the escape hatch every enum field must provide.
const UnknownValue = "unknown"

// Requirement levels the model may assign to a skill.
const (
	LevelRequired  = "required"
	LevelPreferred = "preferred"
	LevelMentioned = "mentioned"
)

// Skill is one extracted technology or competency.
type Skill struct {
	// Name as the posting wrote it. Canonicalisation happens afterwards against
	// our own ontology — the model is not asked to guess our slugs.
	Name  string `json:"name"`
	Level string `json:"level"`
}

// Result is the validated extraction. Every field is optional because a posting
// that does not state something must not be filled in: blueprint §3 forbids
// inventing a value, and that applies to the model's output too.
type Result struct {
	Seniority        string   `json:"seniority"`
	RoleFamily       string   `json:"role_family"`
	EmploymentType   string   `json:"employment_type"`
	RemotePolicy     string   `json:"remote_policy"`
	YearsExperience  *int     `json:"years_experience_min"`
	Skills           []Skill  `json:"skills"`
	Responsibilities []string `json:"responsibilities"`
	Requirements     []string `json:"requirements"`
	// SalaryStated is true only when the POSTING states pay. The model is
	// explicitly told not to estimate: an inferred salary shown next to a real
	// one is indistinguishable to the user and is the error they forgive least.
	SalaryStated bool   `json:"salary_stated"`
	SalaryText   string `json:"salary_text"`
}

// JSONSchema constrains the model's response. Passed as output_config.format, so
// a malformed shape is rejected by the API rather than by our parser.
//
// additionalProperties is false throughout: an unexpected key means the model
// answered a different question than we asked, and silently ignoring it is how
// junk reaches the score.
//
// The repeated literals here are JSON Schema vocabulary, not application
// strings. Hoisting "string" and "items" into Go constants would make this
// harder to read against the spec it mirrors, which is the only thing that
// makes a schema reviewable.
//
//nolint:goconst // JSON Schema keywords read better inline
func JSONSchema() map[string]any {
	return object(
		[]string{
			FieldSeniority, FieldRoleFamily, FieldEmploymentType, FieldRemotePolicy,
			kFieldYears, kFieldSkill, "responsibilities", "requirements",
			"salary_stated", "salary_text",
		},
		map[string]any{
			// "unknown" is a first-class answer everywhere. Forcing a choice is
			// what produces confident nonsense.
			FieldSeniority: enumOf("intern", "junior", "mid", "senior", "staff",
				"principal", UnknownValue),
			FieldRoleFamily: enumOf("backend", "frontend", "fullstack", "mobile",
				"data", "ml", "platform", "security", "qa", "design", "product",
				"sales", "support", "marketing", "people", "finance", "engineering",
				UnknownValue),
			FieldEmploymentType: enumOf("full_time", "part_time", "contract",
				"internship", UnknownValue),
			FieldRemotePolicy: enumOf("remote", "hybrid", "onsite", UnknownValue),
			kFieldYears: nullableInt(0, 50,
				"null unless the posting states a minimum"),
			kFieldSkill: array(40, object(
				[]string{kFieldName, "level"},
				map[string]any{
					kFieldName: str(60),
					"level":    enumOf(LevelRequired, LevelPreferred, LevelMentioned),
				},
			)),
			"responsibilities": array(15, str(300)),
			"requirements":     array(15, str(300)),
			"salary_stated":    boolean(),
			"salary_text": strWithDesc(200,
				"verbatim from the posting; empty when salary_stated is false"),
		},
	)
}

// Instructions is the stable prefix.
//
// It must be byte-identical on every call or prompt caching silently stops
// working — verified by asserting cache_read_tokens is non-zero in production.
// Nothing volatile (no timestamps, no ids, no posting text) may appear here.
const Instructions = `You extract structured facts from a job posting.

Rules:
- Report only what the posting states. Never infer, estimate, or fill gaps.
- Use "unknown" for any field the posting does not state. "unknown" is always
  preferable to a guess.
- years_experience_min must be null unless the posting states a minimum.
- salary_stated is true only if the posting itself gives pay information. Never
  estimate a salary. If it is absent, salary_stated is false and salary_text is
  empty.
- skills: name the technology or competency as the posting writes it. Do not
  translate to a canonical form and do not add skills that are merely implied.
- A skill is something a person can be said to HAVE: a named technology, tool,
  platform, language, protocol, standard, certification, or a named methodology
  or domain of expertise. Include it as the posting writes it.
  Do NOT list, as skills:
    * generic activity or department nouns - "sales", "marketing", "design",
      "engineering", "product", "automation", "documentation" on its own,
      "demos", "logs", "meetings", "collaboration", "communication"
    * responsibilities or artifacts - "hands-on labs", "partner portals",
      "customer meetings", "quarterly reviews". Those belong in
      responsibilities, not skills.
    * the hiring company's own name or products, unless the posting requires
      prior experience with them
  A phrase you would not write on a CV under "Skills" is not a skill here. When
  in doubt, leave it out: an omitted skill costs less than an invented one.
- level is "required" only if the posting requires it, "preferred" for
  nice-to-haves, "mentioned" for anything named without either framing.
- responsibilities and requirements: short verbatim-ish phrases, not prose.

Return only the structured object.`
