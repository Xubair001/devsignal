package eval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/normalize"
	"github.com/Xubair001/devsignal/internal/profileindex"
	"github.com/Xubair001/devsignal/internal/store"
)

// Harness runs the frozen fixtures through the real system.
//
// Through the REAL retrieval, gate and scorer against a real Postgres, not a
// simulation of them. That is the whole point: a harness that reimplements
// retrieval measures the reimplementation, and the bug that matters is always in
// the path production actually takes. It costs a database and about a second.
//
// The database is loaded from the frozen corpus rather than read from whatever
// happens to be ingested locally. Otherwise NDCG moves when the corpus moves, the
// gate becomes noise, and nobody can tell a scoring regression from a new job
// being posted.
type Harness struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewHarness builds a harness over an already-migrated database.
func NewHarness(pool *pgxpool.Pool, log *slog.Logger) *Harness {
	return &Harness{pool: pool, log: log}
}

// evalTenantSlug marks everything the harness creates so Reset can remove it
// without touching anything else in the database.
const evalCompanyDomain = "eval-fixture.invalid"

// Run loads the fixtures, scores every persona, and returns the metrics.
func (h *Harness) Run(ctx context.Context) (Metrics, error) {
	corpus, err := LoadCorpus()
	if err != nil {
		return Metrics{}, err
	}
	personas, err := LoadPersonas()
	if err != nil {
		return Metrics{}, err
	}
	judgements, err := LoadJudgements()
	if err != nil {
		return Metrics{}, err
	}

	if err := h.Reset(ctx); err != nil {
		return Metrics{}, err
	}
	keyByID, err := h.loadCorpus(ctx, corpus)
	if err != nil {
		return Metrics{}, err
	}

	// Judgements grouped per persona.
	rel := map[string]RelevanceMap{}
	for _, j := range judgements {
		if rel[j.PersonaID] == nil {
			rel[j.PersonaID] = RelevanceMap{}
		}
		rel[j.PersonaID][j.PostingKey] = j.Relevance
	}

	svc := matching.New(h.pool, h.log)
	var per []PersonaMetrics
	used := 0

	for _, p := range personas {
		userID, err := h.loadPersona(ctx, p)
		if err != nil {
			return Metrics{}, fmt.Errorf("eval: loading persona %s: %w", p.ID, err)
		}

		// No limit: the harness needs the whole ranked list to measure coverage,
		// and the cutoffs are applied by the metrics rather than by the query.
		res, err := svc.MatchForUser(ctx, userID, 0)
		if err != nil {
			if errors.Is(err, matching.ErrNoProfile) {
				return Metrics{}, fmt.Errorf("eval: persona %s did not load", p.ID)
			}
			return Metrics{}, fmt.Errorf("eval: matching persona %s: %w", p.ID, err)
		}

		pm := PersonaMetrics{PersonaID: p.ID, Returned: len(res.Matches)}
		r := rel[p.ID]
		used += len(r)

		ranked := make([]Ranked, 0, len(res.Matches))
		retrieved := map[string]bool{}
		for _, m := range res.Matches {
			key := keyByID[m.Opportunity.ID.String()]
			ranked = append(ranked, Ranked{Key: key, Eligible: true})
			retrieved[key] = true
		}
		// Coverage counts what RETRIEVAL returned, which includes postings the gate
		// then excluded — the question is whether the scorer ever had the chance.
		for _, e := range res.Excluded {
			retrieved[keyByID[e.Opportunity.ID.String()]] = true
		}

		// An eligibility false positive is a judged-relevant posting that the gate
		// excluded. The gate is allowed to exclude things; it is not allowed to
		// exclude a posting the label set says the user should want, because that
		// means a hard predicate is wrong.
		for _, e := range res.Excluded {
			if r[keyByID[e.Opportunity.ID.String()]] >= relevantThreshold {
				pm.EligibilityFP++
				h.log.Warn("eligibility excluded a judged-relevant posting",
					"persona", p.ID,
					"posting", keyByID[e.Opportunity.ID.String()],
					"title", e.Opportunity.TitleRaw,
					"checks", e.Eligibility.FailedChecks())
			}
		}

		pm.CoverageFound, pm.CoverageTotal = Coverage(retrieved, r)
		pm.Precision7 = PrecisionAtK(ranked, r, PrecisionCutoff)
		n := NDCG(ranked, r, NDCGCutoff)
		if math.IsNaN(n) {
			pm.Skipped = true
		} else {
			pm.NDCG10 = n
		}
		per = append(per, pm)
	}

	m := Aggregate(per)
	m.JudgementsUsed = used
	return m, nil
}

// Reset removes everything a previous harness run created.
//
// Scoped to the fixture company rather than truncating tables: the eval database
// is disposable, but a harness that wipes tables wholesale is one accidental
// DATABASE_URL away from being a data-loss incident.
func (h *Harness) Reset(ctx context.Context) error {
	_, err := h.pool.Exec(ctx, `
		WITH c AS (SELECT id FROM company WHERE canonical_domain = $1)
		DELETE FROM opportunity WHERE company_id IN (SELECT id FROM c)`, evalCompanyDomain)
	if err != nil {
		return fmt.Errorf("eval: clearing fixture postings: %w", err)
	}
	if _, err := h.pool.Exec(ctx,
		`DELETE FROM app_user WHERE email LIKE 'persona-%@eval-fixture.invalid'`); err != nil {
		return fmt.Errorf("eval: clearing fixture users: %w", err)
	}
	return nil
}

// loadCorpus inserts the frozen postings as ready, embedded opportunities and
// returns the id-to-key mapping the metrics need.
//
// Inserted directly rather than driven through ingestion: the harness is measuring
// ranking, and running the pipeline would make eval depend on extraction being
// configured, which it is not in CI. The consequence is stated in the run output —
// with no extracted skills, the skill factors are unavailable and the fit score
// covers less of the model than it will in production.
func (h *Harness) loadCorpus(ctx context.Context, corpus []Posting) (map[string]string, error) {
	var companyID pgtype.UUID
	err := h.pool.QueryRow(ctx, `
		INSERT INTO company (canonical_domain, display_name) VALUES ($1, 'Eval Fixture Co')
		ON CONFLICT (canonical_domain) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING id`, evalCompanyDomain).Scan(&companyID)
	if err != nil {
		return nil, fmt.Errorf("eval: fixture company: %w", err)
	}

	q := store.New(h.pool)
	embedder := embed.NewLocal()
	keyByID := make(map[string]string, len(corpus))

	// A fixed base time so first_seen_at is deterministic. Priority reads it, and
	// a corpus whose ages shift with the wall clock would make the ranking move
	// between runs for reasons unrelated to any change.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i, p := range corpus {
		title := normalize.ParseTitle(p.Title)
		text := p.Title + "\n" + p.DescriptionHTML

		var id pgtype.UUID
		err := h.pool.QueryRow(ctx, `
			INSERT INTO opportunity (
			  company_id, title_raw, title_normalized, description_text,
			  role_family, seniority_ordinal, is_management, work_mode, language,
			  pipeline_state, first_seen_at, last_seen_at, liveness_checked_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'ready',$10,now(),now())
			RETURNING id`,
			companyID, p.Title, title.Normalized, text,
			title.RoleFamily, title.Seniority, title.IsManagement,
			nilIfEmpty(p.WorkMode), nilIfEmpty(p.Language),
			base.Add(time.Duration(i)*time.Minute),
		).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("eval: inserting %s: %w", p.Key(), err)
		}

		vec, err := embedder.Embed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("eval: embedding %s: %w", p.Key(), err)
		}
		if err := q.PutOpportunityEmbedding(ctx, store.PutOpportunityEmbeddingParams{
			OpportunityID: id, EmbeddingModel: embedder.ModelID(),
			EmbeddingVersion: embedder.Version(), EmbeddingDim: int32(len(vec)),
			Embedding: pgvector.NewVector(vec),
		}); err != nil {
			return nil, fmt.Errorf("eval: storing vector for %s: %w", p.Key(), err)
		}
		keyByID[id.String()] = p.Key()
	}
	return keyByID, nil
}

// loadPersona creates the tenant, user and profile, and builds the profile vector
// through the real indexer so the semantic factor sees what production would.
func (h *Harness) loadPersona(ctx context.Context, p Persona) (pgtype.UUID, error) {
	var tenantID, userID pgtype.UUID
	if err := h.pool.QueryRow(ctx,
		`INSERT INTO tenant (display_name) VALUES ($1) RETURNING id`,
		"Eval "+p.ID).Scan(&tenantID); err != nil {
		return userID, fmt.Errorf("tenant: %w", err)
	}
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO app_user (tenant_id, email, password_hash)
		VALUES ($1, $2, 'eval') RETURNING id`,
		tenantID, "persona-"+p.ID+"@"+evalCompanyDomain).Scan(&userID); err != nil {
		return userID, fmt.Errorf("user: %w", err)
	}

	if _, err := h.pool.Exec(ctx, `
		INSERT INTO profile (
		  user_id, tenant_id, headline, seniority_ordinal, is_management,
		  target_role_families, target_countries, work_mode_preference,
		  languages, target_employment_types, min_salary_minor,
		  salary_currency, salary_period)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		userID, tenantID, p.Headline, p.SeniorityOrdinal, p.IsManagement,
		nonNil(p.TargetRoleFamilies), nonNil(p.TargetCountries), p.WorkModePreference,
		nonNil(p.Languages), nonNil(p.TargetEmploymentTypes), p.MinSalaryMinor,
		p.SalaryCurrency, p.SalaryPeriod,
	); err != nil {
		return userID, fmt.Errorf("profile: %w", err)
	}

	// Persona skills become profile_skill rows via the skill table, so the
	// required-skills factor has a real left-hand side. The opportunity side stays
	// empty because extraction has not run — which is exactly why the run output
	// reports fit coverage.
	for _, slug := range p.Skills {
		var skillID pgtype.UUID
		if err := h.pool.QueryRow(ctx, `
			INSERT INTO skill (canonical_slug, display_name, ontology_version)
			VALUES ($1, $2, 'eval')
			ON CONFLICT (canonical_slug) DO UPDATE SET display_name = EXCLUDED.display_name
			RETURNING id`, slug, slug).Scan(&skillID); err != nil {
			return userID, fmt.Errorf("skill %s: %w", slug, err)
		}
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO profile_skill (user_id, skill_id, origin)
			VALUES ($1,$2,'manual') ON CONFLICT DO NOTHING`, userID, skillID); err != nil {
			return userID, fmt.Errorf("profile skill %s: %w", slug, err)
		}
	}

	if err := profileindex.New(h.pool, profileindex.Local(), h.log).Refresh(ctx, userID); err != nil {
		return userID, fmt.Errorf("profile vector: %w", err)
	}
	return userID, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
