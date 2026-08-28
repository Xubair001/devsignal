package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/config"
	"github.com/Xubair001/devsignal/internal/readiness"
)

// readinessRun prints blueprint §38 and exits non-zero if any line is not true.
//
// The blueprint calls the gate binary — "nothing ships to real users until every
// line is true" — so this exits non-zero on an UNPROVEN line as well as a failed
// one. A launch checklist that passes when four items were never measured is a
// formality, and the whole point of the gate is that it is not one.
func readinessRun(
	ctx context.Context, _ *config.Config, _ *slog.Logger, pool *pgxpool.Pool, _ flags,
) error {
	// "." because this runs from the repository root in development. A
	// deployed binary has no source tree, and the test-backed lines then
	// report unproven rather than claiming a pass nobody checked.
	rep, err := readiness.NewEvaluator(pool, ".").Evaluate(ctx)
	if err != nil {
		return err
	}

	fmt.Println("blueprint §38 — production readiness gate")
	fmt.Println("binary: nothing ships to real users until every line is true")
	fmt.Println()

	for _, r := range rep.Results {
		mark := "[ ?? ]"
		switch r.Status {
		case readiness.StatusPass:
			mark = "[ ok ]"
		case readiness.StatusFail:
			mark = "[FAIL]"
		}
		fmt.Printf("%s %s\n", mark, r.Line.Text)
		for _, line := range wrapLines(r.Detail, 68) {
			fmt.Printf("         %s\n", line)
		}
		fmt.Println()
	}

	pass, fail, unproven := rep.Counts()
	fmt.Printf("%d pass, %d fail, %d unproven of %d\n",
		pass, fail, unproven, len(rep.Results))

	if unproven > 0 {
		fmt.Println()
		fmt.Println("UNPROVEN is not passing. A line nobody measured reports as unproven")
		fmt.Println("with what is missing attached, never as green — an all-green board")
		fmt.Println("with unproven lines is not an all-green board. Most of these are")
		fmt.Println("settled by `make test && make test-integration`, which CI runs; the")
		fmt.Println("drills need a human to perform the operation once.")
	}

	if !rep.Ready() {
		return fmt.Errorf("%d of %d readiness lines are not true", fail+unproven, len(rep.Results))
	}
	fmt.Println("\nready.")
	return nil
}
