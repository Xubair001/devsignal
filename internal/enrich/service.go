package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/skill"
	"github.com/Xubair001/devsignal/internal/store"
)

// OntologyVersion tags skills created by extraction, so a taxonomy change is
// detectable rather than silently mixed with older output.
const OntologyVersion = "extracted-2026-08-24"

// Lane separates latency from cost.
const (
	LaneHot  = "hot" // synchronous; keeps the freshness SLO
	LaneCold = "cold"
)

type Service struct {
	pool     *pgxpool.Pool
	q        *store.Queries
	provider Provider
	// ontology normalizes the model's words onto our canonical vocabulary. Nil
	// is tolerated — extraction still works and records the raw phrases — because
	// a failure to load a word list must not stop the pipeline (hard rule 7).
	ontology *skill.Ontology
	log      *slog.Logger
}

func NewService(pool *pgxpool.Pool, p Provider, log *slog.Logger) *Service {
	s := &Service{pool: pool, q: store.New(pool), provider: p, log: log}
	o, err := skill.Load()
	if err != nil {
		// Loud, and non-fatal. Every extracted phrase becomes its own skill in
		// this state, which degrades matching rather than breaking ingestion.
		log.Error("skill ontology failed to load; extracted skills will not be "+
			"normalized", "err", err)
		return s
	}
	s.ontology = o
	return s
}

// normalizedAlias is the lookup key stored in skill_alias.
//
// One function, used by both the seeder and extraction, so the two conventions
// cannot drift. If they did, half the alias lookups would miss and the symptom
// would be "matching got worse" with no obvious cause.
func normalizedAlias(raw string) string {
	if n := skill.Normalize(raw); n != "" {
		return n
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// Outcome reports whether a call was actually paid for. Surfaced so the cache
// hit rate is observable rather than assumed — an ineffective cache looks
// identical to an effective one from the outside.
type Outcome struct {
	Result   Result
	CacheHit bool
	Usage    Usage
	ModelID  string
}

// Extract returns the extraction for a posting, calling the model only when the
// cache genuinely misses.
//
// The cache key is (content_hash, prompt_version, model_id, schema_version). Any
// other reason to re-extract is a bug: it would make the fit score move for a
// posting that did not change.
func (s *Service) Extract(ctx context.Context, contentHash []byte, text, lane string) (*Outcome, error) {
	if len(contentHash) == 0 {
		return nil, fmt.Errorf("enrich: content hash is required for caching")
	}

	cached, err := s.q.GetExtraction(ctx, store.GetExtractionParams{
		ContentHash: contentHash, PromptVersion: PromptVersion,
		ModelID: s.provider.ModelID(), SchemaVersion: SchemaVersion,
	})
	if err == nil {
		var r Result
		if uerr := json.Unmarshal(cached.Normalized, &r); uerr == nil {
			return &Outcome{Result: r, CacheHit: true, ModelID: cached.ModelID}, nil
		}
		// A corrupt cache row is worse than a miss: fall through and re-extract
		// rather than serve something we cannot parse.
		s.log.Warn("cached extraction unreadable; re-extracting",
			"model_id", cached.ModelID)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("enrich: cache lookup: %w", err)
	}

	raw, err := s.provider.Extract(ctx, text)
	if err != nil {
		// Classified here, not in the provider, so every implementation gets the
		// same systemic-versus-document policy.
		return nil, ClassifyProviderError(err)
	}

	result, err := Validate(raw.JSON)
	if err != nil {
		// Deliberately NOT cached. Caching a rejected output would make a bad
		// extraction permanent, and a prompt fix could never take effect.
		return nil, err
	}

	normalized, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("enrich: marshalling result: %w", err)
	}

	if err := s.q.PutExtraction(ctx, store.PutExtractionParams{
		ContentHash: contentHash, PromptVersion: PromptVersion,
		ModelID: raw.Model, SchemaVersion: SchemaVersion,
		RawOutput: raw.JSON, Normalized: normalized,
		InputTokens: int32(raw.Usage.InputTokens), OutputTokens: int32(raw.Usage.OutputTokens),
		CacheReadTokens: int32(raw.Usage.CacheReadTokens), Lane: lane,
	}); err != nil {
		return nil, fmt.Errorf("enrich: caching extraction: %w", err)
	}

	// Token counts, never content.
	s.log.Info("extraction completed",
		"model_id", raw.Model, "lane", lane,
		"input_tokens", raw.Usage.InputTokens, "output_tokens", raw.Usage.OutputTokens,
		"cache_read_tokens", raw.Usage.CacheReadTokens, "skills", len(result.Skills))

	return &Outcome{Result: result, CacheHit: false, Usage: raw.Usage, ModelID: raw.Model}, nil
}

// ApplySkills maps extracted skill names onto our ontology and replaces the
// posting's skill rows.
//
// Replace rather than merge: a re-extraction that dropped a skill must remove it,
// or corrections can only ever add.
func (s *Service) ApplySkills(ctx context.Context, oppID pgtype.UUID, result Result, modelID string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	if err := q.ReplaceOpportunitySkills(ctx, oppID); err != nil {
		return fmt.Errorf("clearing skills: %w", err)
	}

	seen := map[string]bool{}
	for _, sk := range result.Skills {
		// Resolve against the committed vocabulary FIRST.
		//
		// Without this step the model's own words become the skill: "Go",
		// "Golang" and "Go (Golang)" each got their own row, and the skill
		// factors could then never match a profile. Measured before the ontology
		// existed: 10 postings produced 91 distinct skills with almost no
		// overlap, so 45 of the fit model's 100 points were unreachable even
		// with extraction working perfectly.
		slug, resolved := sk.Name, false
		display := strings.TrimSpace(sk.Name)
		version := OntologyVersion
		if s.ontology != nil {
			if canonical, ok := s.ontology.Resolve(sk.Name); ok {
				slug, resolved = canonical, true
				version = skill.OntologyVersion
			}
		}
		if !resolved {
			// Unknown to the vocabulary. Kept as its own skill rather than
			// discarded: we paid for this evidence, and an unresolved phrase is a
			// candidate for the vocabulary. It carries the EXTRACTED ontology
			// version, which is what makes `--role=skills --unresolved` able to
			// list exactly these for review.
			slug = Slugify(sk.Name)
		}
		if slug == "" || seen[slug+"|"+sk.Level] {
			continue
		}
		seen[slug+"|"+sk.Level] = true

		skillID, err := q.UpsertSkillByAlias(ctx, store.UpsertSkillByAliasParams{
			Alias: normalizedAlias(sk.Name), Slug: slug,
			DisplayName: display, OntologyVersion: version,
		})
		if err != nil {
			return fmt.Errorf("resolving skill %q: %w", sk.Name, err)
		}
		// Record the NORMALIZED alias, so the next posting writing it differently
		// resolves to the same skill without another round trip. Normalized
		// because the seeded aliases are, and two conventions in one column means
		// half the lookups miss.
		if err := q.LinkSkillAlias(ctx, store.LinkSkillAliasParams{
			SkillID: skillID, Alias: normalizedAlias(sk.Name),
		}); err != nil {
			return fmt.Errorf("linking alias %q: %w", sk.Name, err)
		}

		conf := float32(1)
		pv := PromptVersion
		mid := modelID
		if err := q.InsertOpportunitySkill(ctx, store.InsertOpportunitySkillParams{
			OpportunityID: oppID, SkillID: skillID, RequirementLevel: sk.Level,
			ExtractionConfidence: &conf, OntologyVersion: version,
			ModelID: &mid, PromptVersion: &pv,
		}); err != nil {
			return fmt.Errorf("inserting skill: %w", err)
		}
	}
	return tx.Commit(ctx)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify canonicalises an extracted skill name.
//
// Deliberately conservative: it normalises case and separators but never tries
// to merge synonyms. "React.js" and "React" collapse; "Postgres" and
// "PostgreSQL" do not, because guessing synonymy is how distinct skills get
// merged. Real synonym mapping belongs in the ontology's alias table, curated.
func Slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	// Keep the characters that carry meaning in technology names.
	s = strings.ReplaceAll(s, "c++", "cpp")
	s = strings.ReplaceAll(s, "c#", "csharp")
	s = strings.ReplaceAll(s, ".js", "")
	s = strings.ReplaceAll(s, ".net", "dotnet")
	s = nonSlug.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
