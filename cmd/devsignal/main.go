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
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/Xubair001/devsignal/internal/config"
	"github.com/Xubair001/devsignal/pkg/logger"
	"github.com/Xubair001/devsignal/pkg/telemetry"
)

func main() {
	role := flag.String("role", "api", "api | worker | digest | admin")
	flag.Parse()

	if err := run(*role); err != nil {
		// stderr, not the logger: the logger may be the thing that failed.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(role string) error {
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
	case "worker", "digest", "admin":
		return fmt.Errorf("role %q is not implemented yet: the pipeline arrives at step 6 of the blueprint's §35 order", role)
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
