package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/config"
	"github.com/Xubair001/devsignal/internal/enrich"
	"github.com/Xubair001/devsignal/internal/profile"
	"github.com/Xubair001/devsignal/pkg/blob"
)

// resumeSkillsRun extracts skills from resumes whose text is parsed.
//
// A role rather than a pipeline stage, deliberately. The pipeline is for
// opportunities — its state machine, sweeper and leases are all keyed on
// `opportunity` — and bolting a user-document path onto it would mean either a
// second state machine or a shared one that means different things per row. A
// batch that finds its own work by version comparison is the smaller thing.
func resumeSkillsRun(
	ctx context.Context, cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, f flags,
) error {
	provider, err := enrich.Resolve(enrich.ResolveConfig{
		Provider:        cfg.ExtractionProvider,
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		Model:           cfg.ExtractionModel,
		ReasoningEffort: cfg.ExtractionReasoningEffort,
	})
	if err != nil {
		return fmt.Errorf("resume skills need a model: %w", err)
	}

	store, err := blob.New(ctx, blob.Config{
		Endpoint: cfg.S3Endpoint, Bucket: cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		PathStyle: cfg.S3PathStyle,
	})
	if err != nil {
		return fmt.Errorf("object storage: %w", err)
	}

	svc := profile.NewService(pool, store, log)
	ex, err := profile.NewSkillExtractor(svc, provider, log)
	if err != nil {
		return err
	}

	limit := int32(f.users)
	if limit <= 0 {
		limit = 25
	}

	fmt.Printf("resume skill extraction  model=%s  redaction=%s\n",
		provider.ModelID(), profile.RedactionVersion)
	fmt.Println()
	fmt.Println("What leaves this process, per resume:")
	fmt.Println("  the resume's extracted text, with the leading header block removed")
	fmt.Println("  (located by the first section heading, else the opening 200 chars) and")
	fmt.Println("  every email address, URL, phone-shaped number and 7+ digit run stripped.")
	fmt.Println("  Employer names, job titles and dates MAY REMAIN — see profile.Redact.")
	fmt.Println()

	results, err := ex.ExtractPending(ctx, limit)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("nothing to do: every parsed resume already has skills under the")
		fmt.Println("current prompt, model and redaction version.")
		return nil
	}

	unresolvedTotal := 0
	for _, r := range results {
		fmt.Printf("  %s\n", r.ResumeID)
		fmt.Printf("     sent %d of %d chars (%d removed)\n",
			r.Redaction.OutChars, r.Redaction.InChars,
			r.Redaction.InChars-r.Redaction.OutChars)
		fmt.Printf("     removed: %d emails, %d URLs, %d phone-shaped, %d long digit runs\n",
			r.Redaction.Emails, r.Redaction.URLs, r.Redaction.Phones,
			r.Redaction.LongDigits)
		fmt.Printf("     header block: %d chars, located by %s\n",
			r.Redaction.HeaderChars, r.Redaction.HeaderBy)
		fmt.Printf("     %d skills found, %d resolved to the vocabulary\n", r.Found, r.Resolved)
		if r.SeniorityClaimed != "" || r.YearsClaimed != nil {
			years := "unstated"
			if r.YearsClaimed != nil {
				years = fmt.Sprintf("%d", *r.YearsClaimed)
			}
			fmt.Printf("     the resume evidences: seniority %q, years %s "+
				"(RECORDED, not written to the profile)\n",
				r.SeniorityClaimed, years)
		}
		if len(r.Unresolved) > 0 {
			unresolvedTotal += len(r.Unresolved)
			fmt.Printf("     not in the vocabulary: %v\n", truncateList(r.Unresolved, 6))
		}
		fmt.Println()
	}

	fmt.Printf("%d resume(s) processed\n", len(results))
	if unresolvedTotal > 0 {
		fmt.Printf("\n%d skill phrases did not resolve. The profile deliberately cannot mint\n",
			unresolvedTotal)
		fmt.Println("new skills — a model's paraphrase is not evidence a vocabulary entry should")
		fmt.Println("exist, and one that matches no posting is worse than none. Review with")
		fmt.Println("`--role=skills --unresolved` and edit internal/skill/ontology.json.")
	}
	fmt.Println()
	fmt.Println("Seniority and years are recorded on the resume row and NOT written onto the")
	fmt.Println("profile. Those are the user's own stated preferences; overwriting what a")
	fmt.Println("person typed with what a model read off their document is the same category")
	fmt.Println("error as showing an imputed salary as the employer's.")
	return nil
}

func truncateList(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return append(v[:n:n], "…")
}
