package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/config"
	"github.com/Xubair001/devsignal/internal/matching"
)

// gapsReport answers "what am I missing" for one user.
func gapsReport(
	ctx context.Context, _ *config.Config, log *slog.Logger, pool *pgxpool.Pool, f flags,
) error {
	if f.user == "" {
		return fmt.Errorf("--user is required")
	}
	var uid pgtype.UUID
	if err := uid.Scan(f.user); err != nil {
		return fmt.Errorf("parsing --user: %w", err)
	}

	rep, err := matching.New(pool, log).SkillGaps(ctx, uid)
	if err != nil {
		return err
	}

	fmt.Printf("skill gaps for %s\n\n", f.user)
	fmt.Printf("  %d roles passed your gate; %d of them have extracted skills (%.0f%%)\n",
		rep.Eligible, rep.WithSkills, rep.Coverage()*100)

	switch rep.State() {
	case matching.StateStale:
		fmt.Println()
		fmt.Println("  NO ELIGIBILITY for the current profile version. The gate has not run")
		fmt.Println("  since this profile last changed, so there is no eligible set to")
		fmt.Println("  analyse — results computed against an older version describe a gate")
		fmt.Println("  the user no longer has, and using them would answer for who they were.")
		fmt.Println()
		fmt.Println("  Fix: request the feed once. That evaluates the gate and writes fresh")
		fmt.Println("  rows.  devsignal --role=match --user=" + f.user)
		return nil
	case matching.StateThin:
		fmt.Println()
		fmt.Println("  NOT ENOUGH INFORMATION. Below 60% coverage this list would say more")
		fmt.Println("  about which postings we failed to read than about what the market")
		fmt.Println("  wants — it would name whichever skills happened to be in the fraction")
		fmt.Println("  we could parse. Run extraction over more of the corpus first.")
		return nil
	}

	if len(rep.Strengths) > 0 {
		fmt.Println()
		fmt.Println("  what you have that these roles ask for:")
		for _, s := range rep.Strengths {
			fmt.Printf("    %-28s required by %d\n", s.DisplayName, s.RequiredBy)
		}
	}

	fmt.Println()
	if len(rep.Gaps) == 0 {
		fmt.Println("  no gaps: every skill your eligible roles require is one you have.")
	} else {
		fmt.Println("  what they ask for that you have not listed:")
		fmt.Printf("    %-28s %8s %9s\n", "SKILL", "REQUIRED", "PREFERRED")
		for _, g := range rep.Gaps {
			fmt.Printf("    %-28s %8d %9d\n", g.DisplayName, g.RequiredBy, g.PreferredBy)
		}
	}

	if rep.Excluded > 0 {
		fmt.Printf("\n  %d further phrases were excluded for not being in the vocabulary.\n",
			rep.Excluded)
		fmt.Println("  A large number there means the ontology is behind, not that you have")
		fmt.Println("  few gaps — review with `--role=skills --unresolved`.")
	}

	fmt.Println()
	fmt.Println("These are COUNTS OF POSTINGS, not an estimate of your chances. We have no")
	fmt.Println("applicant counts, so there is no competitiveness figure to give and one")
	fmt.Println("invented here would discredit every honest number beside it.")
	fmt.Println()
	fmt.Println("Nor is a gap a penalty: the required-skills factor already scores what you")
	fmt.Println("match, and counting it twice is exactly the double-count this surface avoids.")
	return nil
}
