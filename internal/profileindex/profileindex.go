// Package profileindex keeps a profile's vector current.
//
// It is separate from internal/profile because the two have different failure
// modes and, before long, different execution models. Saving a profile is a
// synchronous write the user is waiting on; embedding it is derived work that
// will become a network call the moment a hosted embedder replaces the local
// one. Keeping them apart means a slow or unavailable embedder degrades matching
// quality rather than blocking the user from editing their own preferences.
package profileindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/normalize"
	"github.com/Xubair001/devsignal/internal/store"
)

// Embedder is the same interface the opportunity side uses, so the two never
// drift onto different models — a profile vector and a posting vector must come
// from one space or the distance between them is meaningless.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	ModelID() string
	Version() string
}

// Service refreshes profile vectors.
type Service struct {
	pool     *pgxpool.Pool
	q        *store.Queries
	embedder Embedder
	log      *slog.Logger
}

// New builds the indexer.
func New(pool *pgxpool.Pool, e Embedder, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), embedder: e, log: log}
}

// ErrEmptyProfile reports that a profile carries no text to embed.
//
// Returned rather than storing a zero vector: a zero vector is equidistant from
// everything, so it would produce a full-corpus kNN dressed up as a match. An
// empty profile should yield no vector channel at all.
var ErrEmptyProfile = errors.New("profileindex: profile has no text to embed")

// Refresh recomputes and stores the vector for one profile.
//
// Idempotent, and cheap to call after every profile write: the upsert is keyed on
// (user_id, embedding_version), and the stored profile_version records which
// revision the vector was built from.
func (s *Service) Refresh(ctx context.Context, userID pgtype.UUID) error {
	prof, err := s.q.GetProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("profileindex: no profile for user: %w", err)
		}
		return fmt.Errorf("profileindex: loading profile: %w", err)
	}
	skills, err := s.q.ListProfileSkills(ctx, userID)
	if err != nil {
		return fmt.Errorf("profileindex: loading skills: %w", err)
	}

	text := ProfileText(prof, skills)
	if strings.TrimSpace(text) == "" {
		return ErrEmptyProfile
	}

	vec, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("profileindex: embedding: %w", err)
	}
	if err := s.q.PutProfileEmbedding(ctx, store.PutProfileEmbeddingParams{
		UserID:           userID,
		EmbeddingModel:   s.embedder.ModelID(),
		EmbeddingVersion: s.embedder.Version(),
		EmbeddingDim:     int32(len(vec)),
		Embedding:        pgvector.NewVector(vec),
		ProfileVersion:   prof.ProfileVersion,
	}); err != nil {
		return fmt.Errorf("profileindex: storing vector: %w", err)
	}

	// user_id and version only: profile text is PII and does not belong in logs.
	s.log.Info("profile vector refreshed",
		"user_id", userID.String(),
		"profile_version", prof.ProfileVersion,
		"embedding_version", s.embedder.Version())
	return nil
}

// ProfileText renders the profile into the string that gets embedded.
//
// Exported and pure so it can be tested directly and, more importantly, shown to
// a user asking what the matcher actually read about them. The composition is
// deliberate:
//
//   - Headline and role families first: they carry the user's own words for the
//     work they want, which is what the posting text is most likely to echo.
//   - Skills repeated once per proficiency step, up to a small cap. The embedder
//     uses sublinear term frequency, so repetition raises a skill's weight
//     without letting a long skill list drown the rest of the profile.
//   - Salary, work authorization and country preferences are excluded. They are
//     hard predicates, enforced exactly in SQL; putting them in the vector would
//     turn an exact constraint into a fuzzy nudge and double-count it.
func ProfileText(p store.Profile, skills []store.ListProfileSkillsRow) string {
	var b strings.Builder

	if p.Headline != nil {
		b.WriteString(*p.Headline)
		b.WriteString("\n")
	}
	if len(p.TargetRoleFamilies) > 0 {
		b.WriteString(strings.Join(p.TargetRoleFamilies, " "))
		b.WriteString("\n")
	}
	// Vocabulary from normalize, which owns the ladder.
	if w := normalize.SeniorityTerms(p.SeniorityOrdinal); w != "" {
		b.WriteString(w)
		b.WriteString("\n")
	}
	if p.IsManagement {
		b.WriteString("engineering manager leadership\n")
	}
	for _, sk := range skills {
		reps := skillRepetitions(sk.Proficiency)
		for i := 0; i < reps; i++ {
			b.WriteString(sk.DisplayName)
			b.WriteString(" ")
		}
	}
	return strings.TrimSpace(b.String())
}

// maxSkillRepetitions caps the weight one skill can take. Three is enough to
// separate a primary language from something tried once, and low enough that
// twenty skills cannot bury the headline.
const maxSkillRepetitions = 3

func skillRepetitions(proficiency *int16) int {
	if proficiency == nil {
		return 1
	}
	r := int(*proficiency)
	if r < 1 {
		return 1
	}
	if r > maxSkillRepetitions {
		return maxSkillRepetitions
	}
	return r
}

// Local returns the embedder the rest of the system uses, so a caller cannot
// accidentally index profiles with a different model than the postings.
func Local() Embedder { return embed.NewLocal() }
