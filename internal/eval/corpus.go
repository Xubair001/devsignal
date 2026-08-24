// Package eval is the evaluation harness that gates every scoring change.
//
// What it is, precisely, so nobody reads more into a number than it carries:
//
// This is a REGRESSION harness. It answers "did this change move the ranking on a
// frozen set of postings and personas", which is exactly what hard rule 16 needs
// from CI. It does not answer "is the ranking good". The judgements are derived
// from a stated rubric (see judgements.go), not from humans and not from observed
// behaviour, so they encode what we currently believe a good match looks like.
// A scorer that agrees with the rubric scores well by construction.
//
// That circularity is named rather than hidden because the honest use of these
// numbers is narrow: NDCG@10 moving is a signal to go and look at which pairs
// moved, not a claim about product quality. Real quality measurement needs
// behavioural labels, which arrive with the engagement log in step 17 and then
// replace the rubric labels here. Coverage is the least circular metric of the
// four — it asks whether retrieval returned the judged-relevant postings at all,
// which is a recall question the scorer cannot influence.
package eval

import (
	"embed"
	"encoding/json"
	"fmt"
	"time"
)

//go:embed testdata/corpus.json testdata/personas.json testdata/judgements.json testdata/baseline.json
var fixtures embed.FS

// Posting is one frozen corpus entry.
//
// Identified by (ats_type, ats_job_id) rather than by opportunity_id. That matters:
// opportunity ids are generated at ingest and differ between every environment, so
// a judgement pinned to a local UUID would be meaningless in CI. The ATS pair is
// the stable global identifier the rest of the system already relies on for dedup.
type Posting struct {
	ATSType         string     `json:"ats_type"`
	ATSJobID        string     `json:"ats_job_id"`
	BoardToken      string     `json:"board_token"`
	Title           string     `json:"title"`
	DescriptionHTML string     `json:"description_html"`
	LocationRaw     string     `json:"location_raw"`
	WorkMode        string     `json:"work_mode"`
	Language        string     `json:"language"`
	ApplyURL        string     `json:"apply_url"`
	PostedAt        *time.Time `json:"posted_at,omitempty"`
}

// Key is the stable identity used by judgements.
func (p Posting) Key() string { return p.ATSType + ":" + p.ATSJobID }

// Persona is a synthetic but realistic profile.
//
// Deliberately built to match what the corpus actually contains rather than an
// idealised set: a persona nothing in the corpus could satisfy measures the
// corpus, not the scorer.
type Persona struct {
	ID       string `json:"id"`
	Headline string `json:"headline"`
	// ConstructedFrom records where the persona came from, per the blueprint's
	// eval_profile(constructed_from). Provenance on a fixture is what stops it
	// being mistaken for observed data later.
	ConstructedFrom       string   `json:"constructed_from"`
	SeniorityOrdinal      *int16   `json:"seniority_ordinal,omitempty"`
	IsManagement          bool     `json:"is_management"`
	TargetRoleFamilies    []string `json:"target_role_families"`
	TargetCountries       []string `json:"target_countries"`
	WorkModePreference    *string  `json:"work_mode_preference,omitempty"`
	Languages             []string `json:"languages"`
	TargetEmploymentTypes []string `json:"target_employment_types"`
	Skills                []string `json:"skills"`
	MinSalaryMinor        *int64   `json:"min_salary_minor,omitempty"`
	SalaryCurrency        *string  `json:"salary_currency,omitempty"`
	SalaryPeriod          *string  `json:"salary_period,omitempty"`
}

// Judgement is one labelled (persona, posting) pair.
type Judgement struct {
	PersonaID  string `json:"persona_id"`
	PostingKey string `json:"posting_key"`
	// Relevance is 0 irrelevant, 1 marginal, 2 good, 3 excellent — the blueprint's
	// scale.
	Relevance int `json:"relevance"`
	// Rationale is the rubric clause that produced the label. Stored so a
	// disputed label can be argued with rather than merely disagreed with.
	Rationale string `json:"rationale"`
}

// Baseline is the committed metric set that CI compares against.
type Baseline struct {
	NDCG10        float64 `json:"ndcg_at_10"`
	Precision7    float64 `json:"precision_at_7"`
	Coverage      float64 `json:"coverage"`
	EligibilityFP int     `json:"eligibility_false_positives"`
	// WeightsVersion the baseline was measured under. A baseline from another
	// weight set is not a baseline, it is a different experiment.
	WeightsVersion   string `json:"weights_version"`
	EmbeddingVersion string `json:"embedding_version"`
	RecordedAt       string `json:"recorded_at"`
	Note             string `json:"note"`
}

// LoadCorpus returns the frozen postings.
func LoadCorpus() ([]Posting, error) {
	return loadJSON[[]Posting]("testdata/corpus.json")
}

// LoadPersonas returns the frozen personas.
func LoadPersonas() ([]Persona, error) {
	return loadJSON[[]Persona]("testdata/personas.json")
}

// LoadJudgements returns the frozen labels.
func LoadJudgements() ([]Judgement, error) {
	return loadJSON[[]Judgement]("testdata/judgements.json")
}

// LoadBaseline returns the committed metrics.
func LoadBaseline() (Baseline, error) {
	return loadJSON[Baseline]("testdata/baseline.json")
}

func loadJSON[T any](path string) (T, error) {
	var out T
	b, err := fixtures.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("eval: reading %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("eval: decoding %s: %w", path, err)
	}
	return out, nil
}
