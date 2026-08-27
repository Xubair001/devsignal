package stages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/enrich"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/store"
)

// Enricher runs extraction on rows in state 'deduped' and advances them to
// 'enriched'.
//
// 'deduped' is a degradable state: if the model is unavailable or a posting is
// unparseable, the record still becomes visible with no extracted skills. A
// posting with no skills beats an invisible posting, and making an external
// provider a hard prerequisite for visibility is the failure the whole
// degrade-don't-block rule exists to prevent.
type Enricher struct {
	q   *store.Queries
	svc *enrich.Service
	log *slog.Logger
	// Lane picks latency versus cost. Hot keeps the freshness SLO; cold is for
	// backfill, where a 24-hour turnaround costs nothing that matters.
	Lane string
}

func NewEnricher(pool *pgxpool.Pool, svc *enrich.Service, log *slog.Logger) *Enricher {
	return &Enricher{q: store.New(pool), svc: svc, log: log, Lane: enrich.LaneHot}
}

func (e *Enricher) Handle(ctx context.Context, it pipeline.Item) error {
	row, err := e.q.GetOpportunityForEnrichment(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}

	next, err := pipeline.Next(it.State)
	if err != nil {
		return err
	}

	text := ""
	if row.DescriptionText != nil {
		text = *row.DescriptionText
	}

	// Nothing worth paying for. Advance rather than fail: a stub posting is not
	// an error, and a model call on it would return an empty result we would then
	// cache.
	if len(row.ContentHash) == 0 || len(text) < enrich.MinTextToExtract {
		e.log.Debug("skipping extraction: nothing to extract",
			"id", it.ID.String(), "chars", len(text))
		return e.advance(ctx, it, next, row.ContentHash)
	}

	// No extraction provider configured. Hard rule 7: enrichment failure must not
	// prevent a posting reaching `ready` with a degraded quality flag, and that
	// applies to a provider that was never configured as much as to one that
	// failed. Debug rather than Warn: the worker says this once at startup, and
	// repeating it per posting would bury everything else in the log.
	if e.svc == nil {
		e.log.Debug("skipping extraction: no provider configured", "id", it.ID.String())
		return e.advance(ctx, it, next, row.ContentHash)
	}

	out, err := e.svc.Extract(ctx, row.ContentHash, text, e.Lane)
	if err != nil {
		// A systemic fault fails identically for every posting, so it must not
		// consume this one's retry budget — and it must stay loud, because a
		// missing credential silently degrading the whole corpus is worse than a
		// slow one.
		if errors.Is(err, enrich.ErrProviderUnavailable) {
			return fmt.Errorf("%w: %w", pipeline.ErrRetryLater, err)
		}
		if errors.Is(err, enrich.ErrInvalidOutput) || errors.Is(err, enrich.ErrEmptyInput) {
			// The model answered, but not usably. Record it against this posting
			// so one bad document does not look like a broken model, then let the
			// worker retry and eventually degrade.
			msg := err.Error()
			if len(msg) > 500 {
				msg = msg[:500]
			}
			if rerr := e.q.RecordExtractionFailure(ctx, store.RecordExtractionFailureParams{
				ID: it.ID, ExtractionError: &msg,
			}); rerr != nil {
				e.log.Warn("recording extraction failure", "err", rerr)
			}
		}
		return err
	}

	// Skills first, then advance. If this crashes between the two, the row stays
	// in 'deduped' and re-runs — and because the extraction is cached, the retry
	// costs nothing.
	if err := e.svc.ApplySkills(ctx, it.ID, out.Result, out.ModelID); err != nil {
		return fmt.Errorf("applying skills: %w", err)
	}

	e.log.Debug("enriched", "id", it.ID.String(),
		"cache_hit", out.CacheHit, "skills", len(out.Result.Skills))

	return e.advance(ctx, it, next, row.ContentHash)
}

func (e *Enricher) advance(ctx context.Context, it pipeline.Item, next pipeline.State, hash []byte) error {
	n, err := e.q.AttachExtractionToOpportunity(ctx, store.AttachExtractionToOpportunityParams{
		ContentHash: hash, NextState: string(next),
		ID: it.ID, Version: it.Version, CurrentState: string(it.State),
	})
	if err != nil {
		return fmt.Errorf("attaching extraction: %w", err)
	}
	if n == 0 {
		return pipeline.ErrVersionConflict
	}
	// State advanced in the same statement as the write, so the worker must not
	// advance it again with a now-stale version.
	return pipeline.ErrHandled
}
