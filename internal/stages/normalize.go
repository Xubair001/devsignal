// Package stages binds the pure logic in normalize and dedupe to the database.
//
// The pure packages stay free of database and network dependencies so they
// remain fast to test and safe to re-run over the whole corpus; this package is
// the only place that knows about rows.
package stages

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dedupe"
	"github.com/Xubair001/devsignal/internal/normalize"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/store"
)

// Normalizer derives structured fields and the dedup signature. It runs on rows
// in state 'parsed' and advances them to 'normalized'.
type Normalizer struct {
	q   *store.Queries
	log *slog.Logger
}

func NewNormalizer(pool *pgxpool.Pool, log *slog.Logger) *Normalizer {
	return &Normalizer{q: store.New(pool), log: log}
}

func (n *Normalizer) Handle(ctx context.Context, it pipeline.Item) error {
	row, err := n.q.GetOpportunityForNormalize(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}

	title := normalize.ParseTitle(row.TitleRaw)
	loc := normalize.ParseLocation(deref(row.LocationRegion))

	// The signature covers title plus description: description alone is mostly
	// boilerplate shared across a company's postings, so it cannot carry identity
	// on its own.
	sig := dedupe.SimHash(row.TitleRaw + " " + deref(row.DescriptionText))

	block := dedupe.BlockKey(row.CompanyID.String(), row.TitleRaw, derefOr(loc.Country, ""))

	affected, err := n.q.ApplyNormalization(ctx, store.ApplyNormalizationParams{
		TitleNormalized:      title.Normalized,
		RoleFamily:           title.RoleFamily,
		SeniorityOrdinal:     title.Seniority,
		IsManagement:         title.IsManagement,
		WorkMode:             emptyToNil(loc.WorkMode),
		LocationCountry:      loc.Country,
		LocationCity:         loc.City,
		RemoteGeoScope:       joinScope(loc.GeoScope),
		Simhash:              signedHash(sig),
		BlockKey:             &block,
		NormalizationVersion: strptr(normalize.Version),
		ID:                   it.ID,
		Version:              it.Version,
	})
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if affected == 0 {
		// Someone else advanced this row. Correct behaviour is to yield, not to
		// force the write.
		return pipeline.ErrVersionConflict
	}
	return nil
}

// signedHash reinterprets the unsigned signature as int64 for storage.
// Postgres has no uint64, so the bits are preserved rather than the magnitude —
// which is all Hamming distance needs.
func signedHash(h uint64) *int64 {
	v := int64(h)
	return &v
}

func joinScope(scope []string) *string {
	if len(scope) == 0 {
		return nil
	}
	out := scope[0]
	for _, s := range scope[1:] {
		out += "," + s
	}
	return &out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strptr(s string) *string { return &s }
