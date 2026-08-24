package stages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/store"
)

// Embedder runs on rows in state 'enriched' and advances them to 'embedded'.
//
// 'enriched' is degradable: a posting with no vector is invisible to semantic
// retrieval but still reachable by the structured filters, which is strictly
// better than not being visible at all.
type Embedder struct {
	q   *store.Queries
	emb embed.Embedder
	log *slog.Logger
}

func NewEmbedder(pool *pgxpool.Pool, e embed.Embedder, log *slog.Logger) *Embedder {
	return &Embedder{q: store.New(pool), emb: e, log: log}
}

func (e *Embedder) Handle(ctx context.Context, it pipeline.Item) error {
	row, err := e.q.GetOpportunityTextForEmbedding(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	next, err := pipeline.Next(it.State)
	if err != nil {
		return err
	}

	// Title first and description after: the title is the most discriminating
	// text a posting has, and prefixing it gives it weight in the unigrams
	// without needing a separate weighting scheme.
	text := strings.TrimSpace(row.TitleRaw)
	if row.DescriptionText != nil {
		text += " " + *row.DescriptionText
	}

	vec, err := e.emb.Embed(ctx, text)
	if err != nil {
		if errors.Is(err, embed.ErrEmptyText) {
			// Nothing to embed is not a failure. Advance so the posting stays
			// reachable by structured filters rather than parking it.
			e.log.Debug("no embeddable text; advancing", "id", it.ID.String())
			return e.advance(ctx, it, next)
		}
		return err
	}
	if len(vec) != e.emb.Dim() {
		// A dimension mismatch would be rejected by the column, but failing here
		// gives a message that names the cause.
		return fmt.Errorf("embed: got %d dimensions, column expects %d", len(vec), e.emb.Dim())
	}

	if err := e.q.PutOpportunityEmbedding(ctx, store.PutOpportunityEmbeddingParams{
		OpportunityID:    it.ID,
		EmbeddingModel:   e.emb.ModelID(),
		EmbeddingVersion: e.emb.Version(),
		EmbeddingDim:     int32(len(vec)),
		Embedding:        pgvector.NewVector(vec),
	}); err != nil {
		return fmt.Errorf("storing embedding: %w", err)
	}

	return e.advance(ctx, it, next)
}

func (e *Embedder) advance(ctx context.Context, it pipeline.Item, next pipeline.State) error {
	n, err := e.q.AdvanceAfterEmbedding(ctx, store.AdvanceAfterEmbeddingParams{
		NextState: string(next), ID: it.ID, Version: it.Version,
		CurrentState: string(it.State),
	})
	if err != nil {
		return fmt.Errorf("advancing: %w", err)
	}
	if n == 0 {
		return pipeline.ErrVersionConflict
	}
	// The embedding write and the state change are separate statements, but the
	// write is idempotent on (opportunity_id, version), so a crash between them
	// costs a re-embed and nothing else.
	return pipeline.ErrHandled
}
