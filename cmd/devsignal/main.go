// Command devsignal is the single binary; the role is chosen by flag.
// Roles beyond `api` arrive at their step in the blueprint's §35 order and
// exit with a clear message rather than pretending to work.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"

	"github.com/Xubair001/devsignal/internal/auth"
	"github.com/Xubair001/devsignal/internal/config"
	"github.com/Xubair001/devsignal/internal/embed"
	"github.com/Xubair001/devsignal/internal/enrich"
	"github.com/Xubair001/devsignal/internal/ingest"
	"github.com/Xubair001/devsignal/internal/opportunity"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/profile"
	"github.com/Xubair001/devsignal/internal/source"

	// Importing an adapter family is what enables it.
	_ "github.com/Xubair001/devsignal/internal/source/greenhouse"
	"github.com/Xubair001/devsignal/internal/stages"
	"github.com/Xubair001/devsignal/internal/store"
	"github.com/Xubair001/devsignal/pkg/blob"
	"github.com/Xubair001/devsignal/pkg/logger"
	"github.com/Xubair001/devsignal/pkg/telemetry"
)

func main() {
	role := flag.String("role", "api",
		"api | worker | ingest-once | add-source | add-sources | source-health | spend | digest | admin")
	srcName := flag.String("source", "", "source name, e.g. greenhouse:gitlab")
	srcFile := flag.String("file", "", "file of source names, one per line (add-sources)")
	reviewer := flag.String("reviewed-by", "", "who reviewed the platform (add-sources)")
	flag.Parse()

	if err := run(*role, *srcName, *srcFile, *reviewer); err != nil {
		// stderr, not the logger: the logger may be the thing that failed.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(role, srcName, srcFile, reviewer string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.LogLevel, cfg.LogFmt)
	log.Info("starting", "role", role, "env", cfg.Env)

	// Signal context first: everything below cancels off it, so ^C works even
	// while the pool is still dialling.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Init(ctx, telemetry.Config{
		Enabled:     cfg.OTelEnabled,
		Exporter:    cfg.OTelExporter,
		ServiceName: cfg.OTelServiceName,
		Env:         cfg.Env,
		SampleRatio: cfg.OTelSampleRatio,
	})
	if err != nil {
		return err
	}
	defer func() {
		// Fresh context: ctx is already cancelled by the time we get here.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(flushCtx); err != nil {
			log.Warn("tracing shutdown", "err", err)
		}
	}()

	pool, err := openDB(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch role {
	case "api":
		return serveAPI(ctx, cfg, log, pool)
	case "worker":
		return runWorkers(ctx, cfg, log, pool)
	case "add-source":
		return addSource(ctx, log, pool, srcName)
	case "ingest-once":
		return ingestOnce(ctx, log, pool, srcName)
	case "add-sources":
		return addSources(ctx, log, pool, srcFile, reviewer)
	case "source-health":
		return sourceHealthReport(ctx, pool)
	case "spend":
		return spendReport(ctx, pool)
	case "digest", "admin":
		return fmt.Errorf("role %q is not implemented yet", role)
	default:
		return fmt.Errorf("unknown role %q (api | worker | digest | admin)", role)
	}
}

func openDB(ctx context.Context, cfg *config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	// pods x MaxConns must stay under the server's max_connections. This is a
	// capacity decision, not a default (go-production-patterns skill).
	pc.MaxConns = cfg.DatabaseMaxConns
	pc.MinConns = cfg.DatabaseMinConns
	pc.MaxConnLifetime = time.Hour
	pc.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("database pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database ping: %w", err)
	}
	log.Info("database connected", "max_conns", pc.MaxConns)
	return pool, nil
}

func serveAPI(ctx context.Context, cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool) error {
	r := chi.NewRouter()
	// No RealIP: it is vulnerable to spoofing (it trusts X-Forwarded-For /
	// X-Real-IP whether or not your infrastructure sets them). When rate
	// limiting needs a client IP, derive it from a trusted-proxy config.
	r.Use(middleware.RequestID, middleware.Recoverer)

	// Liveness: is the process up. Never touches a dependency — a DB blip must
	// not get the pod killed.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Readiness: can we serve traffic. This one does check the DB.
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		c, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(c); err != nil {
			log.Warn("readiness failed", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	authSvc := auth.NewService(pool, log, auth.DefaultPolicy(), nil)
	authH := auth.NewHandler(authSvc, log)

	oppH := opportunity.NewHandler(opportunity.NewService(pool, nil), log)

	// Object storage is required for resumes. Failing at startup is correct: an
	// API that accepts an upload it cannot store would lose user data silently.
	store2, err := blob.New(ctx, blob.Config{
		Endpoint: cfg.S3Endpoint, Bucket: cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		PathStyle: cfg.S3PathStyle,
	})
	if err != nil {
		return fmt.Errorf("object storage: %w", err)
	}
	profileH := profile.NewHandler(profile.NewService(pool, store2, log), log)

	r.Route("/api/v1", func(api chi.Router) {
		api.Mount("/auth", authH.Routes())
		// Public read surface: the corpus is not user-specific until matching
		// exists (step 15). Personalized routes live under the authenticated
		// group below.
		api.Mount("/opportunities", oppH.Routes())
		// Everything below requires a live session. Scoping is enforced here and
		// in the query, never per handler.
		api.Group(func(priv chi.Router) {
			priv.Use(authH.Authenticator)
			// Profile, resumes and erasure are all the caller's own data, so they
			// live behind authentication and read the identity from the context.
			priv.Mount("/profile", profileH.Routes())
			priv.Get("/me", func(w http.ResponseWriter, req *http.Request) {
				id, ok := auth.FromContext(req.Context())
				if !ok {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"user_id":%q,"tenant_id":%q}`,
					id.UserID.String(), id.TenantID.String())
			})
		})
	})

	// Trace real requests, not health probes — kubelet polling would otherwise
	// dominate the trace volume and tell you nothing.
	handler := otelhttp.NewHandler(r, "http",
		otelhttp.WithFilter(func(req *http.Request) bool {
			return req.URL.Path != "/healthz" && req.URL.Path != "/readyz"
		}),
	)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      handler,
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}

	// Ordered shutdown. In Kubernetes a preStop delay is still required — the
	// endpoint removal is asynchronous, so without it the LB sends traffic to a
	// draining pod and every deploy produces 502s.
	drainCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	// Deliberately not derived from ctx: ctx is already cancelled by the signal,
	// and inheriting it would abort the drain instantly instead of draining.
	//nolint:contextcheck // fresh context is required for the drain deadline
	if err := srv.Shutdown(drainCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	log.Info("drained cleanly")
	return nil
}

// runWorkers starts one worker per pipeline stage plus the sweeper.
//
// Handlers are placeholders until their step: each stage currently just passes
// the record through so the spine is exercisable end to end. Replacing a
// placeholder is the whole of that step's work.
func runWorkers(ctx context.Context, appCfg *config.Config, log *slog.Logger, pool *pgxpool.Pool) error {
	// pipeCfg, not cfg: the app config is also in scope here and confusing the
	// two is how the wrong value gets read.
	pipeCfg := pipeline.DefaultConfig()
	queue := pipeline.NewQueue(pool, pipeCfg, log)

	// Pass-through where the real work arrives at a later step. Replacing one of
	// these IS that step's work; they are deliberately visible rather than hidden
	// behind a fake implementation.
	passthrough := func(context.Context, pipeline.Item) error { return nil }

	normalizer := stages.NewNormalizer(pool, log)
	deduper := stages.NewDeduper(pool, log)

	// The model is a configuration value, not an architectural commitment: the
	// provider is an interface so tiers can be compared against the regression
	// set without touching the pipeline.
	provider, perr := enrich.NewClaudeProvider(enrich.ClaudeConfig{
		APIKey: appCfg.AnthropicAPIKey, Model: appCfg.ExtractionModel,
	})
	if perr != nil {
		return fmt.Errorf("extraction provider: %w", perr)
	}
	enricher := stages.NewEnricher(pool, enrich.NewService(pool, provider, log), log)

	// Local, deterministic and free by default. A hosted model drops in behind
	// the interface once the eval harness shows retrieval quality justifies the
	// cost and the data egress — measured before buying, not after.
	embedder := stages.NewEmbedder(pool, embed.NewLocal(), log)

	// Concurrency per stage is set independently — that is the whole point of
	// separating them (blueprint §25). AI work will be the bottleneck, not fetch.
	pipelineStages := []pipeline.Stage{
		// Tier-A bulk APIs create rows directly at 'parsed', so these two states
		// are only used by sources where discovery and detail fetch are separate.
		{State: pipeline.StateDiscovered, Concurrency: 4, Handle: passthrough},
		{State: pipeline.StateFetched, Concurrency: 8, Handle: passthrough},

		{State: pipeline.StateParsed, Concurrency: 8, Handle: normalizer.Handle},
		{State: pipeline.StateNormalized, Concurrency: 4, Handle: deduper.Handle},

		// Lower concurrency than the cheap stages: this is the only per-token
		// component, and it is the bottleneck the blueprint expects to scale
		// independently (§25).
		{State: pipeline.StateDeduped, Concurrency: 2, Handle: enricher.Handle},
		// Local embedding is CPU-bound rather than rate-limited, so it can run
		// wider than extraction.
		{State: pipeline.StateEnriched, Concurrency: 4, Handle: embedder.Handle},
		{State: pipeline.StateEmbedded, Concurrency: 2, Handle: passthrough},
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, st := range pipelineStages {
		w := pipeline.NewWorker(queue, st, log)
		g.Go(func() error { return w.Run(gctx) })
	}
	sweeper := pipeline.NewSweeper(queue, log)
	g.Go(func() error { return sweeper.Run(gctx) })

	runner := ingest.NewRunner(pool, sourceClient(), log)
	g.Go(func() error { return runner.Run(gctx) })

	// Per-item dedup is order-dependent; this makes it eventually consistent.
	dedupeSweeper := stages.NewDedupeSweeper(deduper)
	g.Go(func() error { return dedupeSweeper.Run(gctx) })

	// Periodic stats: the state distribution IS the pipeline dashboard.
	g.Go(func() error {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
				rows, err := queue.Stats(gctx)
				if err != nil {
					continue
				}
				for _, r := range rows {
					log.Info("pipeline", "state", r.PipelineState, "count", r.Total)
				}
			}
		}
	})

	log.Info("workers running", "stages", len(pipelineStages))
	if err := g.Wait(); err != nil {
		return fmt.Errorf("workers: %w", err)
	}
	log.Info("workers drained cleanly")
	return nil
}

// sourceClient is the single polite HTTP client for all outbound source
// requests: layered timeouts, per-host rate limiting and a hard body cap.
func sourceClient() *source.Client {
	return source.NewClient(source.DefaultClientConfig())
}

// addSource registers a source and its schedule. Tier and legal basis are
// required by the schema, so an unvetted source cannot be added by accident.
func addSource(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, name string) error {
	if name == "" {
		return fmt.Errorf("--source is required, e.g. --source=greenhouse:gitlab")
	}
	family, cfg, ok := strings.Cut(name, ":")
	if !ok || cfg == "" {
		return fmt.Errorf("source name must be \"<family>:<config>\", got %q", name)
	}
	if _, err := source.Build(family, source.Options{
		Config: map[string]string{"board_token": cfg}, Client: sourceClient(),
	}); err != nil {
		return fmt.Errorf("no usable adapter for %q: %w", name, err)
	}

	q := store.New(pool)
	src, err := q.UpsertSource(ctx, store.UpsertSourceParams{
		Name: name, Tier: "a", Type: family + "_public_board_api",
		LegalBasis:    "public, documented, unauthenticated board API intended for third-party consumption",
		PollInterval:  pgtype.Interval{Microseconds: (5 * time.Minute).Microseconds(), Valid: true},
		EtagSupported: true,
	})
	if err != nil {
		return fmt.Errorf("registering source: %w", err)
	}
	if err := q.UpsertSourceSchedule(ctx, store.UpsertSourceScheduleParams{
		SourceID: src.ID, Cursor: []byte("{}"),
	}); err != nil {
		return fmt.Errorf("scheduling source: %w", err)
	}
	log.Info("source registered", "name", src.Name, "tier", src.Tier, "id", src.ID.String())
	return nil
}

func ingestOnce(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, name string) error {
	if name == "" {
		return fmt.Errorf("--source is required")
	}
	runner := ingest.NewRunner(pool, sourceClient(), log)
	res, err := runner.RunOnce(ctx, name)
	if err != nil {
		return err
	}
	log.Info("ingest complete",
		"fetched", res.Fetched, "created", res.Created, "updated", res.Updated,
		"unchanged", res.Unchanged, "skipped", res.Skipped,
		"closed", res.Closed, "not_modified", res.NotModified,
		"yield", res.ParseYield())
	return nil
}

// addSources registers many boards at once.
//
// The reviewable unit for Tier A is the ATS PLATFORM, not each company board:
// every Greenhouse board is the same documented public endpoint pattern, so
// reviewing one company's board tells you nothing the platform review did not.
// --reviewed-by is therefore required and recorded on every row, so a bulk
// import is attributable rather than anonymous.
func addSources(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, path, reviewer string) error {
	if path == "" {
		return fmt.Errorf("--file is required, one \"<family>:<config>\" per line")
	}
	if reviewer == "" {
		return fmt.Errorf("--reviewed-by is required: a bulk import must be attributable")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	q := store.New(pool)
	client := sourceClient()
	var added, skipped int
	seen := map[string]bool{}

	for i, line := range strings.Split(string(raw), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		if seen[name] {
			continue // a duplicated line is not an error, just nothing to do
		}
		seen[name] = true

		family, cfg, ok := strings.Cut(name, ":")
		if !ok || cfg == "" {
			log.Warn("skipping malformed line", "line", i+1, "value", name)
			skipped++
			continue
		}
		// Refuse to register anything we cannot actually poll. An unbuildable
		// source would sit in the registry looking active and fetch nothing.
		if _, err := source.Build(family, source.Options{
			Config: map[string]string{"board_token": cfg}, Client: client,
		}); err != nil {
			log.Warn("skipping: no usable adapter", "source", name, "err", err)
			skipped++
			continue
		}

		platformRef := family + " public board API, reviewed by " + reviewer
		src, err := q.UpsertSource(ctx, store.UpsertSourceParams{
			Name: name, Tier: "a", Type: family + "_public_board_api",
			LegalBasis:    "public, documented, unauthenticated board API intended for third-party consumption",
			PollInterval:  pgtype.Interval{Microseconds: (5 * time.Minute).Microseconds(), Valid: true},
			EtagSupported: true,
		})
		if err != nil {
			return fmt.Errorf("registering %s: %w", name, err)
		}
		if err := q.SetSourceReview(ctx, store.SetSourceReviewParams{
			ID: src.ID, ReviewedBy: &reviewer, PlatformReviewRef: &platformRef,
		}); err != nil {
			return fmt.Errorf("recording review for %s: %w", name, err)
		}
		if err := q.UpsertSourceSchedule(ctx, store.UpsertSourceScheduleParams{
			SourceID: src.ID, Cursor: []byte("{}"),
		}); err != nil {
			return fmt.Errorf("scheduling %s: %w", name, err)
		}
		added++
	}

	log.Info("bulk source registration complete",
		"added", added, "skipped", skipped, "reviewed_by", reviewer)
	if added == 0 {
		return fmt.Errorf("no sources registered from %s", path)
	}
	return nil
}

// sourceHealthReport prints today against each source's own baseline. Absolute
// numbers are shown alongside the rates because a rate over a tiny sample is
// what a fixed threshold would have alerted on.
func sourceHealthReport(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := store.New(pool).SourceHealthReport(ctx)
	if err != nil {
		return fmt.Errorf("health report: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("no sources registered")
		return nil
	}

	fmt.Printf("%-26s %-6s %-12s %5s %8s %8s %8s  %s\n",
		"SOURCE", "TIER", "STATUS", "DEGR", "SEEN", "YIELD", "LOC%", "NOTE")
	for _, r := range rows {
		yield, loc := "-", "-"
		if r.TodaySeen > 0 {
			yield = fmt.Sprintf("%.0f%%", 100*float64(r.TodayUsable)/float64(r.TodaySeen))
			loc = fmt.Sprintf("%.0f%%", 100*float64(r.TodayWithLocation)/float64(r.TodaySeen))
		}
		// Baseline shown next to today's figure: the drop is the signal, not the
		// level. 71% looks fine until you see it used to be 98%.
		base := ""
		if r.BaselineSeen > 0 {
			base = fmt.Sprintf(" (was %.0f%% / %.0f%%)",
				100*float64(r.BaselineUsable)/float64(r.BaselineSeen),
				100*float64(r.BaselineWithLocation)/float64(r.BaselineSeen))
		}
		note := ""
		if r.LastHealthNote != nil {
			note = *r.LastHealthNote
		}
		fmt.Printf("%-26s %-6s %-12s %5d %8d %8s %8s  %s%s\n",
			trunc(r.Name, 26), r.Tier, r.Status, r.ConsecutiveDegraded,
			r.TodaySeen, yield, loc, note, base)
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// spendReport prints what enrichment has cost. Enrichment is the only component
// billed per token, so its spend has to be observable rather than inferred from
// an invoice weeks later.
func spendReport(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := store.New(pool).ExtractionSpendReport(ctx)
	if err != nil {
		return fmt.Errorf("spend report: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("no extractions recorded yet")
		return nil
	}
	fmt.Printf("%-12s %-24s %-6s %7s %12s %12s %12s  %s\n",
		"DAY", "MODEL", "LANE", "CALLS", "INPUT", "OUTPUT", "CACHED", "CACHE HIT")
	for _, r := range rows {
		// The cached share of input tokens is the lever that decides the bill, so
		// it is shown rather than left to be worked out.
		share := "-"
		if r.InputTokens > 0 {
			share = fmt.Sprintf("%.0f%%", 100*float64(r.CacheReadTokens)/float64(r.InputTokens))
		}
		fmt.Printf("%-12s %-24s %-6s %7d %12d %12d %12d  %s\n",
			r.Day.Time.Format("2006-01-02"), trunc(r.ModelID, 24), r.Lane,
			r.Calls, r.InputTokens, r.OutputTokens, r.CacheReadTokens, share)
	}
	return nil
}
