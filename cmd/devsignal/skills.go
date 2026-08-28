package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/config"
	"github.com/Xubair001/devsignal/internal/skill"
)

// skillsRun seeds the ontology and reports on it.
//
// One role with three modes rather than three roles, because they are the same
// operational question asked at different moments: what does the vocabulary
// contain, what is it failing to recognise, and what is the market asking for.
func skillsRun(
	ctx context.Context, _ *config.Config, log *slog.Logger, pool *pgxpool.Pool, f flags,
) error {
	switch {
	case f.unresolved:
		return skillsUnresolved(ctx, pool)
	case f.demand:
		return skillsDemand(ctx, pool)
	default:
		return skillsSeed(ctx, pool, log)
	}
}

func skillsSeed(ctx context.Context, pool *pgxpool.Pool, _ *slog.Logger) error {
	o, err := skill.Load()
	if err != nil {
		return err
	}
	rep, err := skill.Seed(ctx, pool, o)
	if err != nil {
		return err
	}

	fmt.Printf("seeded ontology %s\n", skill.OntologyVersion)
	fmt.Printf("  %d canonical skills\n", rep.Skills)
	fmt.Printf("  %d aliases (normalized)\n", rep.Aliases)
	fmt.Printf("  %d edges\n\n", rep.Edges)
	fmt.Printf("database now holds %d skills, %d aliases, %d edges\n",
		rep.TotalSkills, rep.TotalAliases, rep.TotalEdges)

	if extra := rep.TotalSkills - int64(rep.Skills); extra > 0 {
		fmt.Printf("\n%d skills came from extraction and are NOT in the vocabulary.\n",
			extra)
		fmt.Println("They are kept, not discarded — we paid for that evidence — and")
		fmt.Println("`--role=skills --unresolved` ranks them by how many postings use")
		fmt.Println("them, which is how the vocabulary grows from evidence.")
	}
	return nil
}

func skillsUnresolved(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := skill.Unresolved(ctx, pool, 40)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("every extracted skill resolved to the committed vocabulary.")
		return nil
	}

	fmt.Printf("%d extracted skills the vocabulary does not know, "+
		"most-used first:\n\n", len(rows))
	fmt.Printf("  %-8s %s\n", "POSTINGS", "PHRASE")
	for _, r := range rows {
		fmt.Printf("  %-8d %s\n", r.Postings, r.DisplayName)
	}
	fmt.Println()
	fmt.Println("A phrase on many postings is worth adding to internal/skill/ontology.json.")
	fmt.Println("One on a single posting is usually noise. Adding one is a reviewed edit to")
	fmt.Println("a data file, then `--role=skills` again — not a migration.")
	return nil
}

func skillsDemand(ctx context.Context, pool *pgxpool.Pool) error {
	rep, err := skill.NewDemandWriter(pool, nil).Snapshot(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("skill demand snapshot for %s\n", rep.Day.Format("2006-01-02"))
	fmt.Printf("  %d rows written (skill x country x work mode)\n", rep.Rows)
	fmt.Printf("  %d days of history, latest %s\n\n", rep.DaysCollected, rep.Latest)

	if len(rep.Top) == 0 {
		fmt.Println("nothing to report: no live posting has an extracted skill yet.")
		fmt.Println("Run extraction first — the demand series is computed from")
		fmt.Println("opportunity_skill, so it is empty until postings are enriched.")
		return nil
	}

	fmt.Printf("  %-34s %9s %s\n", "SKILL", "POSTINGS", "COUNTRIES")
	for _, t := range rep.Top {
		fmt.Printf("  %-34s %9d %d\n", truncate(t.DisplayName, 34), t.Postings, t.Countries)
	}

	fmt.Println()
	fmt.Println("This series is the one thing in the system that cannot be rebuilt:")
	fmt.Println("you cannot retroactively observe what the market wanted last quarter.")
	fmt.Printf("It is a recomputed snapshot, so re-running today is harmless — but a\n")
	fmt.Println("missed day is a permanent gap. It belongs in cron:")
	fmt.Println()
	fmt.Println("  30 2 * * * devsignal --role=skills --demand")

	if rep.DaysCollected < 14 {
		fmt.Println()
		fmt.Printf("With %d day(s) collected, there is no trend to read yet. Growth\n",
			rep.DaysCollected)
		fmt.Println("rates and the skill-gap analysis need weeks, not rows.")
	}
	return nil
}
