package skill

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
)

// SeedReport is what an operator reads after seeding.
type SeedReport struct {
	Skills  int
	Aliases int
	Edges   int
	// TotalSkills counts everything in the table, including skills extraction
	// created that the vocabulary does not cover. The gap between this and
	// Skills is the review queue.
	TotalSkills  int64
	TotalAliases int64
	TotalEdges   int64
}

// Seed writes the committed ontology to the database.
//
// One transaction, and idempotent: running it twice changes nothing, and running
// it after a vocabulary edit converges the database onto the file. That matters
// because the alternative — a migration per vocabulary change — would put a
// hand-edited word list into the schema history, where it cannot be reviewed as
// data or corrected without another migration.
//
// Aliases are written NORMALIZED. The lookup in Resolve normalizes too, so the
// two agree by construction rather than by remembering to.
func Seed(ctx context.Context, pool *pgxpool.Pool, o *Ontology) (*SeedReport, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(pool).WithTx(tx)

	rep := &SeedReport{}
	ids := make(map[string]pgtype.UUID, len(o.Entries))

	for _, e := range o.Entries {
		id, err := q.SeedSkill(ctx, store.SeedSkillParams{
			Slug: e.Slug, DisplayName: e.DisplayName, OntologyVersion: OntologyVersion,
		})
		if err != nil {
			return nil, fmt.Errorf("seeding skill %q: %w", e.Slug, err)
		}
		ids[e.Slug] = id
		rep.Skills++
	}

	// Aliases after every skill exists: an alias may point at a skill defined
	// later in the file, and the file's order is for humans.
	for alias, slug := range o.byAlias {
		id, ok := ids[slug]
		if !ok {
			return nil, fmt.Errorf("alias %q points at unknown slug %q", alias, slug)
		}
		if err := q.SeedSkillAlias(ctx, store.SeedSkillAliasParams{
			SkillID: id, Alias: alias,
		}); err != nil {
			return nil, fmt.Errorf("seeding alias %q: %w", alias, err)
		}
		rep.Aliases++
	}

	for _, ed := range o.Edges {
		from, to := ids[ed.From], ids[ed.To]
		if err := q.SeedSkillEdge(ctx, store.SeedSkillEdgeParams{
			FromSkillID: from, ToSkillID: to, Relation: ed.Relation,
		}); err != nil {
			return nil, fmt.Errorf("seeding edge %s->%s: %w", ed.From, ed.To, err)
		}
		rep.Edges++
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	counts, err := store.New(pool).CountSkillsAndAliases(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting: %w", err)
	}
	rep.TotalSkills, rep.TotalAliases, rep.TotalEdges =
		counts.Skills, counts.Aliases, counts.Edges
	return rep, nil
}

// Unresolved lists skills extraction invented that the vocabulary does not know.
//
// The growth path for the ontology: a phrase on forty postings is worth adding,
// one on a single posting is usually noise. Reviewing this is how the vocabulary
// stays evidence-driven rather than a guess about what postings say.
func Unresolved(
	ctx context.Context, pool *pgxpool.Pool, limit int32,
) ([]store.UnresolvedExtractedSkillsRow, error) {
	return store.New(pool).UnresolvedExtractedSkills(ctx,
		store.UnresolvedExtractedSkillsParams{
			OntologyVersion: OntologyVersion, MaxRows: limit,
		})
}
