// Package retrieve is stage 1 of the two-stage matcher: it turns a profile into a
// bounded candidate set for the scorer to rank.
//
// The bound is the reason it exists. Scoring every user against every posting is
// the full cross product — at the blueprint's target of 100K users and 500K
// postings that is 5x10^10 pairs, roughly 139 CPU-hours per pass. Capping recall
// at 500 candidates per user turns the same pass into about 500 CPU-seconds.
//
// Nothing here ranks. Ordering within a channel only decides what survives the
// cap; the fit score (step 15) is the only thing that ranks, and it sees the
// union without knowing which channel produced what.
package retrieve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Xubair001/devsignal/internal/store"
)

// DefaultMaxCandidates is the blueprint's stage-1 cap (§18).
const DefaultMaxCandidates = 500

// DefaultMaxStaleness drops postings not seen recently. Retrieval is where
// liveness is enforced cheaply; a stale posting that reaches the scorer costs a
// score and then gets filtered anyway.
const DefaultMaxStaleness = 14 * 24 * time.Hour

// perChannelMultiplier over-fetches from each channel relative to the final cap.
//
// The union is capped, so a channel limited to exactly the cap would let whichever
// channel is queried first fill it and starve the other. Over-fetching keeps both
// channels represented. It is not a fix for kNN under-return — that is what
// iterativeScan below addresses — and it costs one extra row read per candidate.
const perChannelMultiplier = 2

// iterativeScan makes the HNSW path safe under a filter.
//
// Measured on 50k vectors where the predicate matched 1% of rows: with the
// default (off), asking for 100 candidates returned 4, because HNSW walks the
// graph to its ef_search budget and the filter is applied to whatever that walk
// happened to touch. With strict_order the same query returned 100. Cost was
// under a millisecond at that size.
//
// strict_order rather than relaxed_order: at this scale the two measured within
// noise of each other (13.1ms vs 14.0ms), and exact distance ordering makes the
// candidate set reproducible, which the eval harness in step 16 depends on.
//
// This is not a complete guarantee. Iterative scan is bounded by
// hnsw.max_scan_tuples, so a predicate selective enough will still under-return
// if the planner insists on the index — at 25 eligible rows in 50k, a forced
// index scan returned 4 of them. The reason it is safe in practice is that the
// planner is left free to choose: with an eligible set that small it uses an
// exact scan and returns all 25. Coverage is still reported per channel, because
// "the plan changed" is not something to find out from a user's empty feed.
const iterativeScan = "strict_order"

// Channel names appear in coverage reports and in the explanation trail, so they
// are part of the contract, not debug strings.
const (
	ChannelVector  = "vector"
	ChannelKeyword = "keyword"
)

// Criteria are the hard predicates. Every zero value means "unconstrained": a
// filter the user never set must not silently empty their feed.
type Criteria struct {
	Countries       []string
	WorkMode        *string
	EmploymentTypes []string
	Languages       []string
	MaxStaleness    time.Duration
	MaxCandidates   int
	// Terms drive the keyword channel. Empty disables that channel rather than
	// matching everything.
	Terms string
}

// Candidate is one retrieved posting.
type Candidate struct {
	ID          pgtype.UUID
	TitleRaw    string
	CompanyID   pgtype.UUID
	FirstSeenAt pgtype.Timestamptz
	// Channels that surfaced this candidate. More than one is a useful signal
	// for step 15 and the reason the union tracks provenance at all.
	Channels []string
	// Distance is the cosine distance from the profile vector, when the vector
	// channel found it. Not a score.
	Distance float64
	// Rank is the full-text rank, when the keyword channel found it.
	Rank float64
}

// FoundBy reports whether a channel surfaced this candidate.
func (c Candidate) FoundBy(channel string) bool {
	for _, ch := range c.Channels {
		if ch == channel {
			return true
		}
	}
	return false
}

// ChannelCoverage records what a channel was asked for and what it gave back.
//
// This exists because the failure mode of retrieval is silence. A channel that
// returns 8 of 200 requested has either exhausted a small eligible set or lost
// candidates to an index walk, and those are opposite problems with identical
// appearances. Eligible is the denominator that separates them.
type ChannelCoverage struct {
	Channel   string
	Requested int
	Returned  int
	Err       error
	// UniverseIsEligibleSet says whether the eligible count is the right
	// denominator for this channel.
	//
	// True for the vector channel: kNN can return any eligible posting, so
	// returning fewer than requested while eligible rows remain means candidates
	// were lost in the index walk. False for the keyword channel, whose universe
	// is the eligible postings whose TITLE matches the terms — a smaller set
	// nothing here counts. Measured against a real board, only 34 of 199 eligible
	// titles matched, so comparing 34 against 199 would report a fault on every
	// healthy run, and a warning that always fires is worse than none.
	UniverseIsEligibleSet bool
}

// Underfilled reports a channel that returned less than it asked for while
// candidates it could have returned remained. Not necessarily a fault — but
// never uninteresting.
func (c ChannelCoverage) Underfilled(eligible int) bool {
	if c.Err != nil || !c.UniverseIsEligibleSet {
		return false
	}
	return c.Returned < c.Requested && eligible > c.Returned
}

// Result is the candidate set plus enough about how it was produced to tell an
// empty market from a broken query.
type Result struct {
	Candidates []Candidate
	// Eligible is how many postings passed the hard predicates, ignoring recall.
	Eligible int
	Coverage []ChannelCoverage
	// Truncated reports that the union exceeded the cap. Downstream cannot treat
	// the set as exhaustive when this is true.
	Truncated bool
}

// CoverageRatio is the fraction of the eligible corpus the union returned. 1.0
// means every eligible posting was retrieved, which happens only in small
// corpora.
func (r Result) CoverageRatio() float64 {
	if r.Eligible == 0 {
		return 0
	}
	return float64(len(r.Candidates)) / float64(r.Eligible)
}

// Service runs stage 1.
type Service struct {
	pool *pgxpool.Pool
}

// New builds a retrieval service.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// ErrNoVector reports that a profile has no embedding for the requested version,
// so the vector channel cannot run. Callers decide whether keyword-only recall
// is acceptable; retrieval will not silently pretend the channel was empty.
var ErrNoVector = errors.New("retrieve: profile has no embedding for this version")

// Retrieve unions the recall channels under one set of hard predicates.
//
// One transaction for both channels, for two reasons: hnsw.iterative_scan is set
// with SET LOCAL and must apply to the vector channel, and both channels then
// observe the same snapshot — otherwise a posting closing between them makes the
// union self-inconsistent.
func (s *Service) Retrieve(
	ctx context.Context, vector pgvector.Vector, embeddingVersion string, c Criteria,
) (*Result, error) {
	c = c.withDefaults()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("retrieve: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// SET LOCAL, so it reverts with the transaction and cannot leak onto a
	// pooled connection that some unrelated query then inherits.
	if _, err := tx.Exec(ctx, "SET LOCAL hnsw.iterative_scan = "+iterativeScan); err != nil {
		return nil, fmt.Errorf("retrieve: enabling iterative scan: %w", err)
	}

	q := store.New(tx)
	staleness := interval(c.MaxStaleness)
	perChannel := c.MaxCandidates * perChannelMultiplier

	eligible, err := q.CountEligibleOpportunities(ctx, store.CountEligibleOpportunitiesParams{
		Countries: c.Countries, WorkMode: c.WorkMode,
		EmploymentTypes: c.EmploymentTypes, Languages: c.Languages,
		MaxStaleness: staleness,
	})
	if err != nil {
		return nil, fmt.Errorf("retrieve: counting eligible: %w", err)
	}

	res := &Result{Eligible: int(eligible)}
	merged := map[string]*Candidate{}

	// Channel 1: vector.
	vrows, verr := q.RetrieveByVector(ctx, store.RetrieveByVectorParams{
		QueryVector: vector, EmbeddingVersion: embeddingVersion,
		Countries: c.Countries, WorkMode: c.WorkMode,
		EmploymentTypes: c.EmploymentTypes, Languages: c.Languages,
		MaxStaleness: staleness, MaxCandidates: int32(perChannel),
	})
	res.Coverage = append(res.Coverage, ChannelCoverage{
		Channel: ChannelVector, Requested: perChannel, Returned: len(vrows), Err: verr,
		UniverseIsEligibleSet: true,
	})
	if verr != nil {
		return nil, fmt.Errorf("retrieve: vector channel: %w", verr)
	}
	for _, r := range vrows {
		add(merged, ChannelVector, Candidate{
			ID: r.ID, TitleRaw: r.TitleRaw, CompanyID: r.CompanyID,
			FirstSeenAt: r.FirstSeenAt, Distance: r.Distance,
		})
	}

	// Channel 2: keyword. Skipped rather than run with empty terms, which would
	// match nothing and report a spurious zero-return.
	if strings.TrimSpace(c.Terms) != "" {
		krows, kerr := q.RetrieveByKeyword(ctx, store.RetrieveByKeywordParams{
			Terms:     c.Terms,
			Countries: c.Countries, WorkMode: c.WorkMode,
			EmploymentTypes: c.EmploymentTypes, Languages: c.Languages,
			MaxStaleness: staleness, MaxCandidates: int32(perChannel),
		})
		res.Coverage = append(res.Coverage, ChannelCoverage{
			Channel: ChannelKeyword, Requested: perChannel, Returned: len(krows), Err: kerr,
		})
		if kerr != nil {
			return nil, fmt.Errorf("retrieve: keyword channel: %w", kerr)
		}
		for _, r := range krows {
			add(merged, ChannelKeyword, Candidate{
				ID: r.ID, TitleRaw: r.TitleRaw, CompanyID: r.CompanyID,
				FirstSeenAt: r.FirstSeenAt, Rank: r.Rank,
			})
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("retrieve: commit: %w", err)
	}

	res.Candidates, res.Truncated = capped(merged, c.MaxCandidates)
	return res, nil
}

// RetrieveForProfile loads the stored profile vector and retrieves with criteria
// derived from the profile itself.
//
// It returns the profile's own version alongside the result: a caller caching
// candidates has to know which profile revision produced them, since the
// blueprint re-scores on profile_version change and nothing else.
func (s *Service) RetrieveForProfile(
	ctx context.Context, userID pgtype.UUID, embeddingVersion string, maxCandidates int,
) (*Result, int32, error) {
	q := store.New(s.pool)

	prof, err := q.GetProfile(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("retrieve: loading profile: %w", err)
	}
	emb, err := q.GetProfileEmbedding(ctx, store.GetProfileEmbeddingParams{
		UserID: userID, EmbeddingVersion: embeddingVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, prof.ProfileVersion, ErrNoVector
		}
		return nil, prof.ProfileVersion, fmt.Errorf("retrieve: loading profile vector: %w", err)
	}

	c := CriteriaFromProfile(prof)
	c.MaxCandidates = maxCandidates
	res, err := s.Retrieve(ctx, emb.Embedding, embeddingVersion, c)
	return res, prof.ProfileVersion, err
}

// CriteriaFromProfile maps stored preferences onto the hard predicates.
//
// Kept separate from Retrieve so the mapping is testable without a database, and
// so an admin tool can show an operator exactly which predicates a user's
// profile produces — the honest answer to "why am I not seeing X".
func CriteriaFromProfile(p store.Profile) Criteria {
	return Criteria{
		Countries:       p.TargetCountries,
		WorkMode:        p.WorkModePreference,
		EmploymentTypes: p.TargetEmploymentTypes,
		Languages:       p.Languages,
		// Role families are the user's own words for what they want, which is
		// exactly what full-text recall needs. OR, not AND: someone targeting
		// backend and platform roles wants both, not only postings mentioning
		// both.
		Terms: strings.Join(p.TargetRoleFamilies, " OR "),
	}
}

func (c Criteria) withDefaults() Criteria {
	if c.MaxCandidates <= 0 {
		c.MaxCandidates = DefaultMaxCandidates
	}
	if c.MaxStaleness <= 0 {
		c.MaxStaleness = DefaultMaxStaleness
	}
	// Nil slices reach Postgres as NULL, and cardinality(NULL) is NULL, not 0 —
	// which would make every predicate NULL and the whole WHERE clause drop
	// every row. Empty non-nil slices are what "unconstrained" has to look like.
	if c.Countries == nil {
		c.Countries = []string{}
	}
	if c.EmploymentTypes == nil {
		c.EmploymentTypes = []string{}
	}
	if c.Languages == nil {
		c.Languages = []string{}
	}
	return c
}

// add merges a candidate into the union, recording every channel that found it.
func add(into map[string]*Candidate, channel string, c Candidate) {
	key := c.ID.String()
	if existing, ok := into[key]; ok {
		existing.Channels = append(existing.Channels, channel)
		// Keep whichever channel supplied each measure; they are not comparable
		// and must not overwrite one another.
		if c.Distance != 0 {
			existing.Distance = c.Distance
		}
		if c.Rank != 0 {
			existing.Rank = c.Rank
		}
		return
	}
	c.Channels = []string{channel}
	into[key] = &c
}

// capped flattens the union to a deterministic, bounded slice.
//
// Candidates found by more than one channel come first. That is not a ranking
// claim — it is the only defensible way to spend a cap, since agreement between
// two channels that fail differently is the strongest evidence retrieval has
// before anything is scored.
//
// Distance breaks the tie next, and it matters more than it looks. Against a
// single company's board most candidates are found by both channels, so channel
// count alone leaves nearly the whole set tied; without a second key the cap
// would then keep whichever postings happened to have the lowest UUIDs. Nearest
// first is the only signal available here that relates to the user at all.
func capped(merged map[string]*Candidate, limit int) ([]Candidate, bool) {
	out := make([]Candidate, 0, len(merged))
	for _, c := range merged {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Channels) != len(out[j].Channels) {
			return len(out[i].Channels) > len(out[j].Channels)
		}
		// Only comparable when both came from the vector channel. A keyword-only
		// candidate has no distance, and 0 would otherwise read as "identical".
		iv, jv := out[i].FoundBy(ChannelVector), out[j].FoundBy(ChannelVector)
		if iv && jv && out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		if iv != jv {
			return iv
		}
		// Ties broken on ID so the set is reproducible; the eval harness compares
		// candidate sets across runs and map iteration order would defeat it.
		return out[i].ID.String() < out[j].ID.String()
	})
	if len(out) > limit {
		return out[:limit], true
	}
	return out, false
}

func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}
