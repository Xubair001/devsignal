package matching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/enrich"
	"github.com/Xubair001/devsignal/internal/retrieve"
	"github.com/Xubair001/devsignal/internal/store"
)

// Clock is injected so nothing in here reads the wall clock directly. Fit must
// not depend on time at all; priority must, and the only way to test that
// distinction is to control it (hard rule 14).
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// Service runs the matcher end to end: retrieve, gate, score, order.
type Service struct {
	pool       *pgxpool.Pool
	q          *store.Queries
	retrieve   *retrieve.Service
	saturation Saturation
	clock      Clock
	log        *slog.Logger
}

// New builds the matching service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{
		pool: pool, q: store.New(pool),
		retrieve: retrieve.New(pool),
		clock:    systemClock{},
		log:      log,
	}
}

// WithClock replaces the clock, for tests.
func (s *Service) WithClock(c Clock) *Service { s.clock = c; return s }

// WithSaturation supplies the impression history that feeds the priority
// penalty. Without it a posting the user has scrolled past ten times keeps its
// place, which is the behaviour the penalty exists to fix.
func (s *Service) WithSaturation(sat Saturation) *Service { s.saturation = sat; return s }

// Saturation supplies how many distinct days each posting was already shown to
// this user without being acted on.
//
// An interface so matching does not depend on the engagement package — the
// dependency runs the other way, since engagement records what matching produced.
// Nil means no history, which is correct before step 17's log has anything in it.
type Saturation interface {
	SaturationFor(ctx context.Context, userID pgtype.UUID) (map[string]int, error)
}

// Match is one scored, eligible posting.
type Match struct {
	Opportunity store.Opportunity
	Fit         Fit
	// Priority orders the feed. Never rendered as a match — see priority.go.
	Priority float64
	// Channels records which retrieval channels surfaced it, kept for the
	// operational "why is this here" question.
	Channels []string
}

// Excluded is a posting retrieval found but the gate rejected.
//
// Returned alongside the matches rather than dropped, because "why am I not
// seeing X" is unanswerable otherwise, and because an operator needs to see when
// a gate is excluding far more than expected.
type Excluded struct {
	Opportunity store.Opportunity
	Eligibility Eligibility
}

// Result is one run of the matcher.
type Result struct {
	Matches  []Match
	Excluded []Excluded
	// Retrieval carries stage 1's own report, so a thin feed can be attributed to
	// retrieval rather than blamed on scoring.
	Retrieval *retrieve.Result
	// CacheHits counts scores served from fit_score rather than recomputed.
	CacheHits int
	// Passed is how many candidates cleared the gate, BEFORE any display limit.
	// Reported separately because len(Matches) is post-limit, and conflating the
	// two makes an operational report understate what the matcher actually did.
	Passed int
	// ProfileVersion the run was computed against.
	ProfileVersion int32
}

// ErrNoProfile reports that the user has no profile to match against.
var ErrNoProfile = errors.New("matching: user has no profile")

// MatchForUser is the whole pipeline for one user.
//
// Ordering: retrieval bounds the work, the gate removes what the user cannot
// apply to, fit scores what is left, and priority orders it. The gate runs BEFORE
// scoring on purpose — scoring an ineligible posting wastes the work and, worse,
// invites someone to later "just show it lower down", which is exactly the
// failure the gate exists to prevent.
func (s *Service) MatchForUser(ctx context.Context, userID pgtype.UUID, limit int) (*Result, error) {
	prof, err := s.q.GetProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoProfile
		}
		return nil, fmt.Errorf("matching: loading profile: %w", err)
	}

	retrieved, _, err := s.retrieve.RetrieveForProfile(ctx, userID, embed.LocalVersion,
		retrieve.DefaultMaxCandidates)
	if err != nil {
		return nil, fmt.Errorf("matching: retrieval: %w", err)
	}

	res := &Result{Retrieval: retrieved, ProfileVersion: prof.ProfileVersion}
	if len(retrieved.Candidates) == 0 {
		return res, nil
	}

	ids := make([]pgtype.UUID, 0, len(retrieved.Candidates))
	channels := make(map[string][]string, len(retrieved.Candidates))
	for _, c := range retrieved.Candidates {
		ids = append(ids, c.ID)
		channels[c.ID.String()] = c.Channels
	}

	profile, err := s.loadProfile(ctx, userID, prof)
	if err != nil {
		return nil, err
	}
	candidates, err := s.loadCandidates(ctx, userID, ids)
	if err != nil {
		return nil, err
	}

	// Cached scores for versions that still match. A stale row is a miss, not a
	// wrong answer served quickly.
	cached, err := s.cachedScores(ctx, userID, prof.ProfileVersion)
	if err != nil {
		return nil, err
	}

	// Impression history. A failure here costs ordering quality, not correctness,
	// so it degrades to "no history" rather than failing the feed.
	var shownDays map[string]int
	if s.saturation != nil {
		var serr error
		shownDays, serr = s.saturation.SaturationFor(ctx, userID)
		if serr != nil {
			s.log.Warn("loading impression history; ordering without saturation",
				"user_id", userID.String(), "err", serr)
		}
	}

	now := s.clock.Now()

	// Writes are COLLECTED and flushed once, not issued per candidate.
	//
	// The per-candidate version was an N+1 write that the load test caught: over
	// 188 candidates a single feed request issued 376 INSERTs, one network round
	// trip each, and took 842ms. Nothing about the statements changed — only how
	// many times the request waits for the network.
	eligWrites := make([]store.PutEligibilityResultBatchParams, 0, len(candidates))
	fitWrites := make([]store.PutFitScoreBatchParams, 0, len(candidates))

	for _, c := range candidates {
		elig := CheckEligibility(profile, c)
		eligWrites = append(eligWrites, store.PutEligibilityResultBatchParams{
			UserID: userID, OpportunityID: c.Opportunity.ID,
			ProfileVersion: prof.ProfileVersion, OpportunityVersion: c.Opportunity.Version,
			Eligible: elig.Eligible, FailedChecks: elig.FailedChecks(),
		})
		if !elig.Eligible {
			res.Excluded = append(res.Excluded, Excluded{Opportunity: c.Opportunity, Eligibility: elig})
			continue
		}

		fit, hit := cached[c.Opportunity.ID.String()]
		if hit {
			res.CacheHits++
		} else {
			fit = ComputeFit(profile, c)
			if w, err := fitParams(userID, c, prof.ProfileVersion, fit); err == nil {
				fitWrites = append(fitWrites, w)
			} else {
				s.log.Error("encoding fit breakdown", "err", err)
			}
		}

		res.Passed++
		res.Matches = append(res.Matches, Match{
			Opportunity: c.Opportunity,
			Fit:         fit,
			Priority: Priority(fit.Score, PrioritySignals{
				FirstSeenAt: c.Opportunity.FirstSeenAt.Time,
				// TimesShownAndIgnored comes from the engagement log. ClosesAt stays
				// absent until a source states one: manufactured urgency is exactly
				// the invented signal hard rule 3 forbids.
				TimesShownAndIgnored: shownDays[c.Opportunity.ID.String()],
			}, now),
			Channels: channels[c.Opportunity.ID.String()],
		})
	}

	// One round trip each. Failures are logged rather than returned: a missing
	// cache row costs a recomputation and a missing audit row costs a diagnostic,
	// while failing the request costs the user their feed.
	s.flushEligibility(ctx, eligWrites)
	s.flushFitScores(ctx, fitWrites)

	// Priority orders the feed. Ties broken on id so the order is reproducible.
	sort.SliceStable(res.Matches, func(i, j int) bool {
		if res.Matches[i].Priority != res.Matches[j].Priority {
			return res.Matches[i].Priority > res.Matches[j].Priority
		}
		return res.Matches[i].Opportunity.ID.String() < res.Matches[j].Opportunity.ID.String()
	})
	if limit > 0 && len(res.Matches) > limit {
		res.Matches = res.Matches[:limit]
	}
	return res, nil
}

// loadProfile assembles the right-hand side.
func (s *Service) loadProfile(ctx context.Context, userID pgtype.UUID, prof store.Profile) (Profile, error) {
	skillIDs, err := s.q.GetProfileSkillIDs(ctx, userID)
	if err != nil {
		return Profile{}, fmt.Errorf("matching: loading profile skills: %w", err)
	}
	skills := make([]string, 0, len(skillIDs))
	for _, id := range skillIDs {
		skills = append(skills, id.String())
	}

	p := Profile{Profile: prof, Skills: skills}

	// work_authorization is free-form jsonb written by the profile API. Decoded
	// defensively: a shape we do not recognise must leave the user unconstrained
	// rather than excluded, since the gate's default has to be "pass".
	if len(prof.WorkAuthorization) > 0 {
		var wa struct {
			Countries        []string `json:"countries"`
			NeedsSponsorship *bool    `json:"needs_sponsorship"`
		}
		if err := json.Unmarshal(prof.WorkAuthorization, &wa); err == nil {
			p.WorkAuthCountries = wa.Countries
			// Unknown is NOT true: assuming a user needs sponsorship would hide
			// most of the corpus from them without being asked.
			p.NeedsSponsorship = wa.NeedsSponsorship != nil && *wa.NeedsSponsorship
		}
	}
	return p, nil
}

// loadCandidates fetches the postings, their extracted skills and their distance
// from the profile vector, in three queries rather than three per candidate.
func (s *Service) loadCandidates(
	ctx context.Context, userID pgtype.UUID, ids []pgtype.UUID,
) ([]Candidate, error) {
	emb, err := s.q.GetProfileEmbedding(ctx, store.GetProfileEmbeddingParams{
		UserID: userID, EmbeddingVersion: embed.LocalVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("matching: loading profile vector: %w", err)
	}

	rows, err := s.q.GetOpportunitiesForScoring(ctx, store.GetOpportunitiesForScoringParams{
		QueryVector:      emb.Embedding,
		EmbeddingVersion: embed.LocalVersion,
		OpportunityIds:   ids,
	})
	if err != nil {
		return nil, fmt.Errorf("matching: loading candidates: %w", err)
	}

	skillRows, err := s.q.GetOpportunitySkills(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("matching: loading opportunity skills: %w", err)
	}
	required := map[string][]string{}
	preferred := map[string][]string{}
	for _, r := range skillRows {
		key := r.OpportunityID.String()
		switch r.RequirementLevel {
		case enrich.LevelRequired:
			required[key] = append(required[key], r.SkillID.String())
		case enrich.LevelPreferred:
			preferred[key] = append(preferred[key], r.SkillID.String())
		}
	}

	out := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		key := r.Opportunity.ID.String()
		c := Candidate{
			Opportunity:     r.Opportunity,
			RequiredSkills:  required[key],
			PreferredSkills: preferred[key],
		}
		// Distance is nil when the LEFT JOIN found no vector for this version.
		// Cosine distance is 1 - similarity, so convert back rather than feeding a
		// distance into a factor documented as a similarity.
		sim := 1 - r.Distance
		c.SemanticSimilarity = &sim
		out = append(out, c)
	}
	return out, nil
}

// cachedScores returns scores whose every version still matches.
func (s *Service) cachedScores(
	ctx context.Context, userID pgtype.UUID, profileVersion int32,
) (map[string]Fit, error) {
	rows, err := s.q.GetFitScores(ctx, store.GetFitScoresParams{
		UserID:           userID,
		WeightsVersion:   WeightsVersion,
		ProfileVersion:   profileVersion,
		EmbeddingVersion: embed.LocalVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("matching: loading cached scores: %w", err)
	}
	out := make(map[string]Fit, len(rows))
	for _, r := range rows {
		var factors []FactorScore
		if err := json.Unmarshal(r.Factors, &factors); err != nil {
			// A breakdown we cannot decode is not usable as an explanation, so
			// treat it as a miss and recompute rather than showing a score with no
			// working attached.
			s.log.Warn("undecodable cached breakdown; recomputing",
				"opportunity_id", r.OpportunityID.String(), "err", err)
			continue
		}
		out[r.OpportunityID.String()] = Fit{
			Score: int(r.Score), MaxPossible: int(r.MaxPossible),
			Factors: factors, WeightsVersion: WeightsVersion,
		}
	}
	return out, nil
}

// fitParams builds one cache row, or reports why it could not.
func fitParams(
	userID pgtype.UUID, c Candidate, profileVersion int32, f Fit,
) (store.PutFitScoreBatchParams, error) {
	factors, err := json.Marshal(f.Factors)
	if err != nil {
		return store.PutFitScoreBatchParams{}, err
	}
	return store.PutFitScoreBatchParams{
		UserID:             userID,
		OpportunityID:      c.Opportunity.ID,
		WeightsVersion:     WeightsVersion,
		ProfileVersion:     profileVersion,
		OpportunityVersion: c.Opportunity.Version,
		EmbeddingVersion:   embed.LocalVersion,
		Score:              int16(f.Score),
		MaxPossible:        int16(f.MaxPossible),
		Factors:            factors,
	}, nil
}

// flushFitScores writes the score cache in one round trip.
func (s *Service) flushFitScores(ctx context.Context, rows []store.PutFitScoreBatchParams) {
	if len(rows) == 0 {
		return
	}
	br := s.q.PutFitScoreBatch(ctx, rows)
	defer func() { _ = br.Close() }()
	br.Exec(func(i int, err error) {
		if err != nil {
			s.log.Error("caching fit score", "index", i, "err", err)
		}
	})
}

// flushEligibility writes the gate's verdicts in one round trip.
//
// Passes are stored as well as failures: the difference between "excluded" and
// "never considered" is what makes the operational view trustworthy.
func (s *Service) flushEligibility(
	ctx context.Context, rows []store.PutEligibilityResultBatchParams,
) {
	if len(rows) == 0 {
		return
	}
	br := s.q.PutEligibilityResultBatch(ctx, rows)
	defer func() { _ = br.Close() }()
	br.Exec(func(i int, err error) {
		if err != nil {
			s.log.Error("recording eligibility", "index", i, "err", err)
		}
	})
}
