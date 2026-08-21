---
name: go-production-patterns
description: Concrete Go shapes for DevSignal service code — HTTP clients, bounded concurrency, context and timeouts, pgx pool sizing, graceful shutdown and drain, panic recovery, slog, money and time types, GOMEMLIMIT and container CPU behaviour. Use when writing or reviewing any Go in this repo, when a service leaks goroutines or memory, when deploys drop requests or jobs, when Postgres connections exhaust, or when reaching for time.Now, float for money, or http.DefaultClient.
---

# Go production patterns for this repo

These are the shapes that survive contact with production in this specific system: a long-running
Go process fetching untrusted remote content and writing to one Postgres.

## Runtime (Go ≥ 1.25)

Go 1.25 made `GOMAXPROCS` container-aware — the runtime reads the cgroup CPU limit at startup and
rechecks periodically.

- **Do not set `GOMAXPROCS` in a deployment manifest.** It overrides the correct runtime value and
  reintroduces exactly the throttling it used to fix. This is the inverted version of the old bug.
- **Do not add `uber-go/automaxprocs`.** It is superseded on this Go version.
- **Do set `GOMEMLIMIT`** to roughly 90% of the container memory limit. The runtime still does not
  know your memory ceiling, and without it a GC that runs slightly late becomes an OOMKill.

## HTTP out — never the default client

`http.DefaultClient` has no timeout. One hung source holds a goroutine and a connection until you
exhaust both.

```go
tr := &http.Transport{
    MaxConnsPerHost:       4,
    MaxIdleConnsPerHost:   4,
    IdleConnTimeout:       90 * time.Second,
    TLSHandshakeTimeout:   10 * time.Second,
    ResponseHeaderTimeout: 15 * time.Second,
}
client := &http.Client{Transport: tr, Timeout: 45 * time.Second}
```

Four separate timeouts matter because they fail differently: a dial timeout is a dead host, a TLS
timeout is a broken middlebox, a response-header timeout is a hung server, and the total timeout is
your only protection against a slow-drip body.

```go
const maxBody = 5 << 20
body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
if len(body) > maxBody {
    return ErrBodyTooLarge
}
```

An unbounded `io.ReadAll` on a hostile or merely broken page is an OOM waiting to happen.

## Bounded concurrency, always

```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(8)
for _, u := range urls {
    u := u
    g.Go(func() error { return fetch(ctx, u) })
}
err := g.Wait()
```

Never `go work(item)` in a loop over input you did not size. Backpressure comes from claiming a
bounded batch off the queue — never from memory pressure, which is not backpressure, it is a crash.

## Context and timeouts

Every DB and HTTP call takes a context. But client-side cancellation does **not** stop a query
already executing on the server, so also set a server-side ceiling:

```sql
ALTER ROLE devsignal SET statement_timeout = '30s';
```

Cancelling a context mid-query also forces pgx to discard the connection, so a storm of cancellations
churns the pool. Prefer a correctly sized timeout over aggressive cancellation.

## pgx pool sizing

```
pods × pool_max ≤ max_connections − headroom
```

20 pods at 25 connections is 500 connections, which will exceed a managed Postgres limit and fail as
a cascade rather than as backpressure. Treat `pool_max` as a capacity decision, not a default.

When PgBouncer arrives in transaction pooling mode: **disable pgx statement caching.** Transaction
pooling hands you a different server connection per transaction, so a cached prepared-statement
handle does not exist on it, and you get intermittent `prepared statement does not exist` under load
only. Decide this before load testing, not during it.

## Graceful shutdown and drain

Two distinct bugs appear on the first rolling deploy.

**API side.** Kubernetes removes the pod from the Service endpoints asynchronously, so the load
balancer keeps sending traffic for a second or two after SIGTERM. Without a `preStop` delay those
requests become 502s on *every* deploy.

```yaml
lifecycle:
  preStop:
    exec: { command: ["sh", "-c", "sleep 5"] }
terminationGracePeriodSeconds: 60      # longer than your drain deadline
```

**Worker side.** Ordered shutdown, exactly:

```go
<-sigCtx.Done()
claimer.Stop()                          // 1. stop claiming new work
srv.Shutdown(drainCtx)                  // 2. finish in-flight HTTP
workers.Drain(drainCtx)                 // 3. finish in-flight jobs
queue.ReleaseClaims(ctx, claimed)       // 4. release rows, don't wait out the lease
```

Every claim carries a lease so a `kill -9` self-heals; the explicit release just makes a clean deploy
fast instead of slow.

## Panic recovery at the loop boundary

```go
func (w *Worker) runOne(ctx context.Context, job Job) (err error) {
    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("panic: %v", r)
        }
    }()
    return w.handle(ctx, job)
}
```

One malformed document must not kill the process and take the rest of the batch with it. Recover per
job, not per process.

## Types that cause silent corruption

**Money.** `int64` minor units + ISO-4217 currency + period. Never `float`.

```go
type Money struct {
    AmountMinor int64   // 12345 == 123.45
    Currency    string  // "USD"
    Period      Period  // year | month | hour
}
```

A float will eventually show a user `$119,999.99`, and a single `salary` field cannot hold a range.

**Time.** Inject a clock; never call `time.Now()` inside domain logic.

```go
type Clock interface{ Now() time.Time }
```

Freshness, expiry, liveness and lease logic are untestable otherwise — you cannot write a test for
"closed after N missed polls" against the real wall clock. All timestamps `timestamptz`, UTC.

## Logging and metrics

```go
slog.InfoContext(ctx, "stage completed",
    "stage", "enrich", "source_id", srcID, "count", n)
```

- `log/slog` only. No third-party logger, no `fmt.Println`.
- **No PII in logs, ever** — not in errors, not in debug, not "temporarily".
- Metric labels are bounded: `source_id`, `pipeline_stage`, `status`, `http_status_class`,
  `model_id`. `user_id` and `opportunity_id` belong in trace attributes and log fields. One metric
  series per opportunity is unbounded cardinality and becomes the biggest line on the bill.

## Errors

```go
var ErrBodyTooLarge = errors.New("source: body exceeds cap")

if err != nil {
    return fmt.Errorf("parse posting %s: %w", id, err)
}
```

Wrap with `%w`, use sentinel errors for domain conditions the caller must branch on, and keep the
retryable/permanent distinction explicit — the pipeline decides backoff versus `failed_permanent`
from it.

## SQL

`pgx` directly, no ORM. `sqlc` for the static queries that are most of the system. A query builder
for the faceted-search endpoint only — the "`($1 IS NULL OR col = $1)`" workaround defeats the
planner and degrades as the corpus grows. Do not contort `sqlc`, and do not adopt an ORM to solve one
endpoint.

## Profiling

`net/http/pprof` on a separate admin port, never publicly routable.

## Quick review checklist

- [ ] No `http.DefaultClient`; layered timeouts; `io.LimitReader` on every foreign body
- [ ] Every fan-out bounded (`SetLimit` / semaphore)
- [ ] Context on every DB and HTTP call; `statement_timeout` set server-side
- [ ] `pool_max × pods` budgeted; statement cache off behind PgBouncer
- [ ] Shutdown: stop claiming → drain → release claims; `preStop` present
- [ ] Panic recovered per job
- [ ] Money is `int64` minor units; no float
- [ ] Time from an injected `Clock`
- [ ] `slog`; no PII; metric labels bounded
- [ ] `GOMEMLIMIT` set; `GOMAXPROCS` *not* set in the manifest
