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
	"github.com/Xubair001/devsignal/internal/ingest"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/source"

	// Importing an adapter family is what enables it.
	_ "github.com/Xubair001/devsignal/internal/source/greenhouse"
	"github.com/Xubair001/devsignal/internal/stages"
	"github.com/Xubair001/devsignal/internal/store"
	"github.com/Xubair001/devsignal/pkg/logger"
	"github.com/Xubair001/devsignal/pkg/telemetry"
)

func main() {
	role := flag.String("role", "api", "api | worker | ingest-once | add-source | digest | admin")
	srcName := flag.String("source", "", "source name, e.g. greenhouse:gitlab")
	flag.Parse()

	if err := run(*role, *srcName); err != nil {
		// stderr, not the logger: the logger may be the thing that failed.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(role, srcName string) error {
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
		return runWorkers(ctx, log, pool)
	case "add-source":
		return addSource(ctx, log, pool, srcName)
	case "ingest-once":
		return ingestOnce(ctx, log, pool, srcName)
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

	r.Route("/api/v1", func(api chi.Router) {
		api.Mount("/auth", authH.Routes())
		// Everything below requires a live session. Scoping is enforced here and
		// in the query, never per handler.
		api.Group(func(priv chi.Router) {
			priv.Use(authH.Authenticator)
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
func runWorkers(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error {
	cfg := pipeline.DefaultConfig()
	queue := pipeline.NewQueue(pool, cfg, log)

	// Pass-through where the real work arrives at a later step. Replacing one of
	// these IS that step's work; they are deliberately visible rather than hidden
	// behind a fake implementation.
	passthrough := func(context.Context, pipeline.Item) error { return nil }

	normalizer := stages.NewNormalizer(pool, log)
	deduper := stages.NewDeduper(pool, log)

	// Concurrency per stage is set independently — that is the whole point of
	// separating them (blueprint §25). AI work will be the bottleneck, not fetch.
	pipelineStages := []pipeline.Stage{
		// Tier-A bulk APIs create rows directly at 'parsed', so these two states
		// are only used by sources where discovery and detail fetch are separate.
		{State: pipeline.StateDiscovered, Concurrency: 4, Handle: passthrough},
		{State: pipeline.StateFetched, Concurrency: 8, Handle: passthrough},

		{State: pipeline.StateParsed, Concurrency: 8, Handle: normalizer.Handle},
		{State: pipeline.StateNormalized, Concurrency: 4, Handle: deduper.Handle},

		{State: pipeline.StateDeduped, Concurrency: 4, Handle: passthrough},  // enrichment: step 12
		{State: pipeline.StateEnriched, Concurrency: 2, Handle: passthrough}, // embeddings: step 13
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
