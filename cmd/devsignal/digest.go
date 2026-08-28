package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/config"
	"github.com/Xubair001/devsignal/internal/digest"
	"github.com/Xubair001/devsignal/internal/matching"
	"github.com/Xubair001/devsignal/internal/opportunity"
	"github.com/Xubair001/devsignal/internal/store"
)

// digestRun is blueprint §35 step 18: build and send the daily digest.
//
// Prints a per-user report rather than only a count, because every outcome that
// is not "sent" has a reason and those reasons are the operational content. "47
// users, 3 sent" is not an answer to "why did I not get a digest".
func digestRun(
	ctx context.Context, cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, f flags,
) error {
	sender, err := digest.NewSender(cfg.MailSender, cfg.MailLogDir, nil)
	if err != nil {
		return err
	}
	svc := digest.NewService(pool, matching.New(pool, log),
		opportunity.NewService(pool, nil), sender, nil, log)

	if f.dryRun {
		return digestDryRun(ctx, pool, svc)
	}

	fmt.Printf("digest run  sender=%s\n", sender.Name())
	if cfg.MailSender == digest.SenderLog {
		fmt.Printf("            writing to %s (nothing is delivered)\n", cfg.MailLogDir)
	}
	fmt.Println()

	rep, err := svc.Run(ctx)
	if err != nil {
		return err
	}
	printDigestReport(rep)

	// Non-zero on a failure, so a cron entry is a working alert. An empty digest
	// is NOT a failure and must not exit non-zero: "nothing met the bar" is a
	// correct outcome, and treating it as an error would train whoever reads the
	// alert to ignore it.
	if n := rep.Counts()[digest.OutcomeFailed]; n > 0 {
		return fmt.Errorf("%d digests failed", n)
	}
	return nil
}

func printDigestReport(rep *digest.RunReport) {
	if len(rep.Results) == 0 {
		fmt.Println("no eligible recipients.")
		fmt.Println()
		fmt.Println("A recipient needs all of: a profile, a verified email address, an")
		fmt.Println("active account, digest_enabled, and a recorded consent that has not")
		fmt.Println("been withdrawn. Opt a user in with:")
		fmt.Println()
		fmt.Println("  devsignal --role=digest-optin --user=<id> --timezone=Europe/London")
		return
	}

	for _, r := range rep.Results {
		mark := "  "
		switch r.Outcome {
		case digest.OutcomeSent:
			mark = "->"
		case digest.OutcomeFailed:
			mark = "!!"
		}
		fmt.Printf("%s %-38s %-22s %d items\n",
			mark, r.UserID, string(r.Outcome), len(r.Items))
		if r.Reason != "" {
			for _, line := range wrapLines(r.Reason, 72) {
				fmt.Printf("     %s\n", line)
			}
		}
		for _, it := range r.Items {
			fmt.Printf("       %-14s %s — %s\n", string(it.Match.Fit.Band()),
				truncate(it.Posting.Title, 46), it.Posting.Company.Name)
		}
	}

	fmt.Println()
	counts := rep.Counts()
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[digest.Outcome(k)]))
	}
	fmt.Printf("%d recipients: %s\n", len(rep.Results), strings.Join(parts, ", "))

	// The caveat that matters right now, stated every run rather than left for
	// someone to work out from a row of zeros.
	if counts[digest.OutcomeEmpty] == len(rep.Results) && len(rep.Results) > 0 {
		fmt.Println()
		fmt.Println("Every digest was empty. Before treating that as a bug: with skill")
		fmt.Println("extraction unavailable, 45 of the fit model's 100 points cannot be")
		fmt.Println("scored, so coverage sits below 60% and every posting bands as \"Not")
		fmt.Println("enough information\" — which clears no bar by design. The digest is")
		fmt.Println("correctly declining to interrupt anyone on evidence we do not have.")
	}
}

// digestDryRun composes without claiming a day or sending anything.
//
// Separate from the real path rather than a flag threaded through it: a dry run
// that shares the write path is one `if` away from claiming a day it should not,
// and a consumed day cannot be given back.
func digestDryRun(ctx context.Context, pool *pgxpool.Pool, svc *digest.Service) error {
	q := store.New(pool)
	users, err := q.DigestCandidateUsers(ctx)
	if err != nil {
		return fmt.Errorf("loading candidates: %w", err)
	}
	fmt.Printf("dry run: %d eligible recipients, claiming nothing\n\n", len(users))
	for _, u := range users {
		msg, res, err := svc.Preview(ctx, u)
		if err != nil {
			fmt.Printf("!! %s: %v\n", u.UserID.String(), err)
			continue
		}
		fmt.Printf("== %s  %s  %s\n", u.UserID.String(), u.Timezone, string(res.Outcome))
		if res.Reason != "" {
			fmt.Printf("   %s\n", res.Reason)
		}
		fmt.Printf("   subject: %s\n", msg.Subject)
		fmt.Println()
		fmt.Println(indent(msg.Text, "   | "))
	}
	return nil
}

// digestOptIn records consent and settings for one user.
//
// A CLI action, not an API endpoint, and deliberately so for now: the consent
// wording a user agrees to is part of the record, and there is no signup screen
// yet to show them any. This writes a wording version that names itself as
// operator-recorded, so a real double opt-in can be told apart from this later.
func digestOptIn(
	ctx context.Context, _ *config.Config, _ *slog.Logger, pool *pgxpool.Pool, f flags,
) error {
	if f.user == "" {
		return fmt.Errorf("--user is required")
	}
	var uid pgtype.UUID
	if err := uid.Scan(f.user); err != nil {
		return fmt.Errorf("parsing --user: %w", err)
	}
	if _, err := time.LoadLocation(f.timezone); err != nil {
		return fmt.Errorf("--timezone %q is not an IANA zone name: %w", f.timezone, err)
	}
	if f.minBand != digest.BarStrong && f.minBand != digest.BarWorthALook {
		return fmt.Errorf("--min-band must be %q or %q",
			digest.BarStrong, digest.BarWorthALook)
	}

	q := store.New(pool)
	u, err := q.GetUserByID(ctx, uid)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}

	if _, err := q.UpsertNotificationSetting(ctx, store.UpsertNotificationSettingParams{
		UserID: uid, TenantID: u.TenantID, Timezone: f.timezone,
		QuietStart: 21, QuietEnd: 8, DigestEnabled: true,
		MaxPerWeek: 5, MinBand: f.minBand, SendWhenEmpty: false,
	}); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	// The wording is recorded, not just the fact. "Consent you cannot evidence is
	// consent you do not have", and evidencing it means knowing what was agreed.
	const wording = "operator-recorded-v1"
	if _, err := q.RecordDigestConsent(ctx, store.RecordDigestConsentParams{
		UserID: uid, ConsentedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		WordingVersion: strPtr(wording),
	}); err != nil {
		return fmt.Errorf("recording consent: %w", err)
	}

	fmt.Printf(
		"opted in %s\n  timezone     %s\n  quiet hours  21:00-08:00 local\n"+
			"  min band     %s\n  weekly cap   5\n  consent      %s\n",
		f.user, f.timezone, f.minBand, wording)
	fmt.Println()
	fmt.Println("This is an OPERATOR record, not a double opt-in. It is distinguishable")
	fmt.Println("from a real one by its wording version, which is the point: the consent")
	fmt.Println("basis in docs/OPEN-DECISIONS.md §3 is still a recommendation.")
	return nil
}

func strPtr(s string) *string { return &s }

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func wrapLines(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			out = append(out, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(out, line)
}
