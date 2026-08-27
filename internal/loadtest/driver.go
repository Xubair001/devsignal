package loadtest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Xubair001/devsignal/internal/auth"
	"github.com/Xubair001/devsignal/internal/profileindex"
	"github.com/Xubair001/devsignal/internal/slo"
)

// Config is one run.
type Config struct {
	// Users is how many distinct profiles drive traffic. More than one matters:
	// the fit score is cached per (user, posting, versions), so a single user would
	// measure the cache and call it the feed.
	Users int
	// Concurrency is the number of in-flight requests. Bounded by an errgroup
	// limit, never by spawning a goroutine per request — backpressure from a
	// bounded worker set, not from memory.
	Concurrency int
	// Duration is how long each phase runs.
	Duration time.Duration
	// FeedSize is the limit passed to the feed, defaulting to the product's seven.
	FeedSize int
}

// WithDefaults fills the gaps.
func (c Config) WithDefaults() Config {
	if c.Users <= 0 {
		c.Users = 20
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
	if c.Duration <= 0 {
		c.Duration = 15 * time.Second
	}
	if c.FeedSize <= 0 {
		c.FeedSize = 7
	}
	return c
}

// Result is the whole run.
type Result struct {
	Config Config
	// Cold is the first request per user: nothing cached, retrieval and scoring run
	// in full. This is what the cold feed objective is about.
	Cold Stats
	// Warm is repeated requests: the fit scores are cached, so this measures the
	// path a returning user takes.
	Warm Stats
	// SLO verdicts, so the run answers "did we meet the objective" rather than
	// leaving the reader to compare numbers to a table.
	Verdicts []slo.Result
	// CorpusSize is what the feed had to work with. A latency number without it is
	// unfalsifiable: fast against 30 postings says nothing about 500,000.
	CorpusSize int64
}

// Driver runs the test.
type Driver struct {
	pool    *pgxpool.Pool
	handler http.Handler
	log     *slog.Logger
	// fixturePrefix marks everything the run creates so cleanup can remove it
	// without touching anything else.
	fixturePrefix string
}

// NewDriver builds one over the REAL router.
func NewDriver(pool *pgxpool.Pool, handler http.Handler, log *slog.Logger) *Driver {
	return &Driver{
		pool: pool, handler: handler, log: log,
		fixturePrefix: "loadtest-" + uuid.NewString()[:8],
	}
}

// Run seeds users, drives the feed cold then warm, and evaluates the objectives.
func (d *Driver) Run(ctx context.Context, cfg Config) (*Result, error) {
	cfg = cfg.WithDefaults()
	res := &Result{Config: cfg}

	if err := d.pool.QueryRow(ctx,
		`SELECT count(*) FROM opportunity WHERE pipeline_state='ready' AND merged_into IS NULL`).
		Scan(&res.CorpusSize); err != nil {
		return nil, fmt.Errorf("loadtest: counting corpus: %w", err)
	}
	if res.CorpusSize == 0 {
		// Refuse rather than report a fast feed over nothing. A load test that
		// passes against an empty corpus is the most misleading possible result.
		return nil, fmt.Errorf("loadtest: the corpus is empty; " +
			"ingest something first or the latency numbers mean nothing")
	}

	users, err := d.seedUsers(ctx, cfg.Users)
	if err != nil {
		return nil, err
	}
	defer d.cleanup(context.WithoutCancel(ctx))

	// Cold: one request per user, nothing cached. Sequential per user but
	// concurrent across users, so each user's first request really is their first.
	d.log.Info("load test: cold phase", "users", len(users))
	cold := NewRecorder()
	if err := d.drive(ctx, users, cfg, cold, true); err != nil {
		return nil, err
	}
	res.Cold = cold.Summarize()

	// Warm: repeated requests for the run's duration, hitting the score cache.
	d.log.Info("load test: warm phase", "duration", cfg.Duration)
	warm := NewRecorder()
	if err := d.drive(ctx, users, cfg, warm, false); err != nil {
		return nil, err
	}
	res.Warm = warm.Summarize()

	res.Verdicts = d.evaluate(res)
	return res, nil
}

// user is one seeded caller.
type user struct {
	id    pgtype.UUID
	token string
}

// drive issues requests. coldOnce sends exactly one request per user; otherwise it
// loops until the duration elapses.
func (d *Driver) drive(
	ctx context.Context, users []user, cfg Config, rec *Recorder, coldOnce bool,
) error {
	deadline := time.Now().Add(cfg.Duration)

	g, gctx := errgroup.WithContext(ctx)

	// One goroutine per WORKER, not per user, with workers pulling users from a
	// shared counter.
	//
	// The first version spawned a goroutine per user and bounded them with
	// SetLimit, which quietly broke the measurement: only the first `concurrency`
	// users ever ran during the timed phase, and the remaining ones each fired a
	// single request as the deadline passed. Those were that user's FIRST request,
	// so they were cold — cold requests counted into the warm percentile. At 20
	// users and 4 concurrent it pushed the warm p95 from 134ms to 968ms, which
	// looked like the service degrading rather than the harness misreporting.
	workers := cfg.Concurrency
	if coldOnce {
		// The cold phase is one request per user, so it needs no more workers than
		// there are users.
		workers = min(workers, len(users))
	}

	// A mutex-free fan-in: each worker records into its own recorder and they are
	// merged at the end. Sharing one recorder under a lock would serialize the
	// workers and make the concurrency setting a fiction.
	locals := make([]*Recorder, workers)
	var next atomic.Int64

	for w := range workers {
		locals[w] = NewRecorder()
		rec := locals[w]
		g.Go(func() error {
			for {
				i := next.Add(1) - 1
				if coldOnce && i >= int64(len(users)) {
					// Every user has had their one request.
					return nil
				}
				u := users[int(i)%len(users)]

				start := time.Now()
				status, err := d.requestFeed(gctx, u, cfg.FeedSize)
				rec.Add(Sample{Duration: time.Since(start), Status: status, Err: err})

				// A cancelled context stops the phase cleanly rather than failing
				// it: the run was interrupted, which is not a load-test result.
				// errgroup already carries whatever caused the cancellation.
				select {
				case <-gctx.Done():
					return nil
				default:
				}
				if !coldOnce && time.Now().After(deadline) {
					return nil
				}
			}
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("loadtest: driving requests: %w", err)
	}

	for _, l := range locals {
		for _, s := range l.samples {
			rec.Add(s)
		}
		rec.overflow += l.overflow
	}
	return nil
}

// requestFeed issues one authenticated feed request against the real handler.
func (d *Driver) requestFeed(ctx context.Context, u user, size int) (int, error) {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/feed?limit=%d", size), nil)
	req.Header.Set("Authorization", "Bearer "+u.token)

	rec := httptest.NewRecorder()
	d.handler.ServeHTTP(rec, req)

	// The body is read to completion, because a user waits for it and a handler
	// that streams slowly would otherwise look fast.
	if _, err := io.Copy(io.Discard, rec.Body); err != nil {
		return rec.Code, err
	}
	return rec.Code, nil
}

// evaluate compares the run against the objectives.
func (d *Driver) evaluate(res *Result) []slo.Result {
	var out []slo.Result

	if o, ok := slo.ByID(slo.FeedLatencyCold); ok {
		out = append(out, slo.EvaluateLatency(o, res.Cold.P95, int64(res.Cold.Successes)))
	}
	if o, ok := slo.ByID(slo.FeedLatencyCached); ok {
		out = append(out, slo.EvaluateLatency(o, res.Warm.P95, int64(res.Warm.Successes)))
	}
	if o, ok := slo.ByID(slo.APIAvailability); ok {
		// Both phases pooled: availability is a property of the service, not of a
		// phase, and splitting it would let a bad cold phase hide behind a good
		// warm one.
		total := int64(res.Cold.Total + res.Warm.Total)
		ok2 := int64(res.Cold.Successes + res.Warm.Successes)
		out = append(out, slo.EvaluateRatio(o, ok2, total, res.Config.Duration*2))
	}
	return out
}

// seedUsers registers users through the real service and builds their vectors.
//
// Through the real path rather than by inserting rows: a load test that
// hand-crafts its fixtures can pass while registration is broken, and the feed
// depends on a profile vector that only the indexer produces.
func (d *Driver) seedUsers(ctx context.Context, n int) ([]user, error) {
	var tenantID pgtype.UUID
	if err := d.pool.QueryRow(ctx,
		`INSERT INTO tenant (display_name) VALUES ($1) RETURNING id`,
		"Load test "+d.fixturePrefix).Scan(&tenantID); err != nil {
		return nil, fmt.Errorf("loadtest: tenant: %w", err)
	}

	idx := profileindex.New(d.pool, profileindex.Local(), d.log)
	out := make([]user, 0, n)

	for i := range n {
		email := fmt.Sprintf("%s-%d@loadtest.invalid", d.fixturePrefix, i)

		var userID pgtype.UUID
		if err := d.pool.QueryRow(ctx, `
			INSERT INTO app_user (tenant_id, email, password_hash, email_verified_at)
			VALUES ($1,$2,'loadtest',now()) RETURNING id`,
			tenantID, email).Scan(&userID); err != nil {
			return nil, fmt.Errorf("loadtest: user %d: %w", i, err)
		}

		// Profiles vary across users so the feed is not answering the same query
		// every time. Identical profiles would let one cached candidate set serve
		// every request, and the measurement would be of Postgres's buffer cache.
		persona := personas[i%len(personas)]
		if _, err := d.pool.Exec(ctx, `
			INSERT INTO profile (user_id, tenant_id, headline, seniority_ordinal,
			  target_role_families, target_countries, work_mode_preference,
			  languages, target_employment_types)
			VALUES ($1,$2,$3,$4,$5,ARRAY[]::char(2)[],$6,ARRAY['en']::char(2)[],
			        ARRAY[]::text[])`,
			userID, tenantID, persona.headline, persona.seniority,
			persona.families, persona.workMode); err != nil {
			return nil, fmt.Errorf("loadtest: profile %d: %w", i, err)
		}
		if err := idx.Refresh(ctx, userID); err != nil {
			return nil, fmt.Errorf("loadtest: profile vector %d: %w", i, err)
		}

		token, err := d.session(ctx, userID)
		if err != nil {
			return nil, err
		}
		out = append(out, user{id: userID, token: token})
	}
	d.log.Info("load test fixtures seeded", "users", len(out), "prefix", d.fixturePrefix)
	return out, nil
}

// personas give the seeded users different queries. Drawn from what the corpus
// actually contains rather than an idealised spread.
// workModeRemote is what every persona prefers, because the corpus is
// overwhelmingly remote — a persona asking for onsite would measure the corpus
// rather than the feed.
const workModeRemote = "remote"

var personas = []struct {
	headline  string
	seniority int16
	families  []string
	workMode  string
}{
	{"Senior backend engineer, Go and PostgreSQL", 4, []string{"backend", "platform"}, workModeRemote},
	{"Staff infrastructure engineer, Kubernetes", 5, []string{"platform"}, workModeRemote},
	{"Mid-level fullstack engineer, TypeScript", 3, []string{"fullstack", "frontend"}, workModeRemote},
	{"Senior security engineer, application security", 4, []string{"security"}, workModeRemote},
	{"Senior product manager, developer tooling", 4, []string{"product"}, workModeRemote},
}

// session issues a real session token through the auth tables.
//
// The token is a random opaque value hashed at rest, which is what the
// authenticator expects: sessions are server-side records so revocation is real
// (blueprint §31.1), and a load test that forged a JWT would bypass the lookup the
// feed actually performs on every request.
func (d *Driver) session(ctx context.Context, userID pgtype.UUID) (string, error) {
	raw := uuid.NewString() + uuid.NewString()

	// auth.HashToken, not a local sha256: the authenticator looks the session up by
	// that exact hash, and a second implementation of it would drift and produce a
	// load test that authenticates against nothing.
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO user_session (user_id, token_hash, expires_at, user_agent)
		VALUES ($1,$2, now() + interval '1 hour', 'loadtest')`,
		userID, auth.HashToken(raw)); err != nil {
		return "", fmt.Errorf("loadtest: session: %w", err)
	}
	return raw, nil
}

// cleanup removes everything the run created.
//
// Runs with a detached context so a cancelled or timed-out run still cleans up —
// otherwise an interrupted load test leaves its users behind and the next run's
// numbers include them.
func (d *Driver) cleanup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if _, err := d.pool.Exec(ctx,
		`DELETE FROM app_user WHERE email LIKE $1`, d.fixturePrefix+"-%@loadtest.invalid"); err != nil {
		d.log.Error("load test cleanup: users", "err", err)
	}
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM tenant WHERE display_name = $1`, "Load test "+d.fixturePrefix); err != nil {
		d.log.Error("load test cleanup: tenant", "err", err)
	}
}
