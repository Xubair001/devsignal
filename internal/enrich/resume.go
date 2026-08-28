package enrich

// Resume extraction versions. Separate from the posting ones on purpose: the two
// prompts change independently, and a shared version would invalidate one
// task's cache every time the other was edited.
const (
	ResumePromptVersion = "rp-2026-08-27"
	ResumeSchemaVersion = "rs-2026-08-27"
)

// ResumeSkillsTask reads a candidate's skills out of REDACTED resume text.
//
// A separate task from the posting one rather than a reused prompt, because the
// asymmetry matters. A posting states requirements; a resume states claims. The
// posting prompt asks for `level: required | preferred | mentioned`, which is a
// property of a job advert and meaningless about a person — asking for it here
// would get an invented answer.
//
// The instructions say the text is redacted, so the model does not treat gaps as
// something to reconstruct. Asking a model to fill in a redacted name is exactly
// the behaviour we removed the name to prevent.
func ResumeSkillsTask() Task {
	return Task{
		Name:          "resume_skills",
		Instructions:  ResumeInstructions,
		Schema:        ResumeSkillsSchema,
		PromptVersion: ResumePromptVersion,
		SchemaVersion: ResumeSchemaVersion,
		// Smaller than a posting's: this returns a skill list, not a full record.
		MaxOutputTokens: 4096,
	}
}

// ResumeInstructions is the stable prefix for resume extraction.
const ResumeInstructions = `You extract a candidate's skills from their resume.

The text has been REDACTED before reaching you: the name, contact details,
phone numbers, URLs and any long identifier have been removed, and the first
lines of the document were dropped. Gaps are deliberate.

Rules:
- Report only what the resume states. Never infer, estimate or fill gaps.
- Never attempt to reconstruct a removed name, address, contact detail or
  identifier, and never mention that anything is missing.
- skills: name each technology, tool, platform, language, protocol, standard,
  certification, or named methodology or domain of expertise the resume claims
  the person has. Write it as the resume writes it.
  Do NOT list, as skills:
    * generic activity or department nouns - "sales", "engineering", "product",
      "communication", "teamwork", "problem solving"
    * employers, job titles, schools, or degree names
    * responsibilities and achievements. "Led a team of six" is not a skill.
  A phrase you would not write on a CV under "Skills" is not a skill here.
- years_experience_min: the total years of professional experience the resume
  states or that its dated roles clearly span. null when it cannot be read from
  the document without guessing.
- seniority: the most senior level the resume evidences, or "unknown".
- When in doubt, leave it out: an omitted skill costs less than an invented one.

Return only the structured object.`

// ResumeSkillsSchema constrains the response.
//
// Deliberately narrow. Everything a resume could yield that we do not need —
// employers, dates, education, locations — is absent from the schema, so the
// model cannot return it and we cannot accidentally store it. Minimizing what
// LEAVES is privacy rule 2; minimizing what can come BACK is the same rule
// applied in the other direction.
func ResumeSkillsSchema() map[string]any {
	return object(
		[]string{kFieldSkill, kFieldYears, "seniority"},
		map[string]any{
			kFieldSkill: array(60, object(
				[]string{kFieldName},
				map[string]any{kFieldName: str(60)},
			)),
			kFieldYears: nullableInt(0, 70,
				"null unless the resume states or clearly spans it"),
			"seniority": enumOf(
				"intern", "junior", "mid", "senior", "staff", "principal", UnknownValue,
			),
		},
	)
}

// ResumeSkills is the parsed response.
type ResumeSkills struct {
	Skills []struct {
		Name string `json:"name"`
	} `json:"skills"`
	YearsExperienceMin *int   `json:"years_experience_min"`
	Seniority          string `json:"seniority"`
}
