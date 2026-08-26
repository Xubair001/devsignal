# Service level objectives, dashboards and alerts

Blueprint §28's table, as running code. `internal/slo` holds the objectives and the
error budget arithmetic; `--role=slo` prints the report and exits non-zero on a
breach; `GET /internal/admin/slo` serves the same thing as JSON.

## The rule this is arranged around

**An objective we cannot measure reports as `unmeasurable` with the reason
attached — never as green.**

Four of the twelve currently cannot be measured — five until step 18 landed;
see "What changed at step 18" below. A dashboard that shows green for
something nobody measured is worse than one with a visible gap: the gap prompts a
question, the false green ends the conversation. It is the same rule the product
applies to users (blueprint §3, hard rule 3), applied to ourselves.

## The objectives

| Objective | Target | Window | Measured from |
|---|---|---|---|
| Feed latency, cached | p95 < 300 ms | 1 day | metrics |
| Feed latency, cold | p95 < 800 ms | 1 day | metrics |
| Search latency | p95 < 500 ms | 1 day | **unmeasurable** — no search endpoint |
| Freshness, Tier A | p95 < 15 min | 1 day | database |
| Freshness, Tier B | p50 < 2 h | 1 day | **unmeasurable** — no Tier B source registered |
| Liveness accuracy | > 97% genuinely open | 30 days | **unmeasurable** — needs ground truth |
| Parse yield per source | ≥ 98% | 1 day | database, **per source** |
| Dedup precision | > 99.5% | 30 days | **unmeasurable** — needs labelled pairs |
| Extraction validity | > 99% | 1 day | database |
| Digest generation | inside 30 min | 1 day | database, since step 18 |
| Pipeline backlog | 0 records stranded > 1 h | 1 hour | database |
| API availability | 99.5% | 30 days | metrics |

Three notes that change how the numbers should be read.

**Freshness measures OUR pipeline, not publish-to-visible.** It runs from
`first_seen_at` — the moment we first saw the posting — to the moment it reached
`ready`. The source's claimed publish date is deliberately not used: boards and
employers refresh it so listings look fresh, which would make this number a
fiction we control. The report states the caveat inline every time.

**Parse yield is per source, never aggregated.** Parser rot affects one board at a
time, and an aggregate stays green while one source silently returns empty fields.
That failure is what blueprint §29 calls the largest ongoing operational cost in
the product.

**Digest generation measures the SPREAD of a run, not one user's duration.**
The objective is that everybody's digest is ready in time, so the number is
first-user-start to last-user-finish on the most recent day that produced
anything. It reports `no_data` rather than `met` when no run has happened: a
0-second spread over zero users would clear a 30-minute target trivially, and an
objective that goes green because the job never ran is the false green this
document exists to prevent.

**Liveness accuracy is not the same as verification recency.** Whether a role is
genuinely open needs the employer's answer, which we do not have. What we can
measure is how recently we checked, and that is reported *next to* the objectives
under an explicit heading rather than inside the accuracy row. A test
(`TestLivenessAccuracyStaysUnmeasurableUntilGroundTruthExists`) fails if anyone
flips it to measurable, because doing so means either finding a source of truth or
starting to guess.

## Error budgets and burn rate

For a ratio objective, the error budget is the permitted failure fraction:
99.5% availability allows 0.5% of requests to fail over the month.

The number that matters operationally is not the budget but the **burn rate** —
how fast it is being consumed relative to the rate that would exhaust it exactly
as the window closes.

```
burn rate 1.0   on track to spend the whole budget precisely at the window's end
burn rate 2.0   spending twice as fast; the budget is gone at the halfway point
burn rate 0     nothing failing
```

Burn rate is scaled by how much of the window the measurement covers. Comparing a
raw one-hour failure fraction against a month's budget would call every brief blip
a breach, which is how alerts get muted.

An objective inside its target but burning above 1x reports as **`at_risk`**. That
is the state worth acting on: by the time an objective is breached, the users
already had the bad month.

## Alert rules

Two windows, following the multi-window approach in Google's SRE workbook. Two
rather than one because they catch different failures, and single-window alerting
forces a choice between missing slow burns and paging on every deploy.

| Severity | Burn rate | Sustained over | Consumes | Means |
|---|---|---|---|---|
| **page** | ≥ 14.4x | 1 hour | 2% of a 30-day budget | an outage happening now |
| **ticket** | ≥ 6x | 6 hours | 5% of a 30-day budget | a degradation nobody noticed |

`slo.Alert(burnRate, window)` returns the severity. The thresholds are constants in
`internal/slo`, so changing them is a reviewed diff rather than a dashboard edit.

### Prometheus form

The metric names below are what the service emits. These rules are written out
because the collector is a blueprint §36 earned migration and does not exist yet —
when it lands, this is what goes in it, not something to be re-derived.

```yaml
# Fast burn: page.
- alert: DevSignalAPIAvailabilityFastBurn
  expr: |
    (
      sum(rate(http_server_request_duration_count{http_response_status_class="5xx"}[1h]))
      /
      sum(rate(http_server_request_duration_count[1h]))
    ) > (14.4 * 0.005)
  for: 1h
  labels: { severity: page }
  annotations:
    summary: "API availability burning error budget at >14.4x"

# Slow burn: ticket.
- alert: DevSignalAPIAvailabilitySlowBurn
  expr: |
    (
      sum(rate(http_server_request_duration_count{http_response_status_class="5xx"}[6h]))
      /
      sum(rate(http_server_request_duration_count[6h]))
    ) > (6 * 0.005)
  for: 6h
  labels: { severity: ticket }

# Latency objectives are a bound, not a budget: a p95 either clears it or does not.
- alert: DevSignalFeedLatencyCached
  expr: |
    histogram_quantile(0.95,
      sum by (le) (rate(http_server_request_duration_bucket{http_route="/api/v1/feed"}[1h]))
    ) > 300
  for: 15m
  labels: { severity: ticket }
```

The pipeline backlog and the corpus-derived objectives do not need Prometheus:
`--role=slo` exits non-zero on a breach, so a cron entry is sufficient and needs no
metrics pipeline at all.

```cron
*/10 * * * * devsignal --role=slo >/var/log/devsignal-slo.log 2>&1 || notify-oncall
```

## Metric cardinality

Hard rule 12 is the shape of `pkg/telemetry/metrics.go`. Every label has a small
fixed set of values:

| Emitted | Never |
|---|---|
| `http.route` — the chi route **template** | the raw path (one series per opportunity id) |
| `http.response.status_class` — 2xx/4xx/5xx | the exact status code |
| `pipeline.stage`, `status` | `opportunity_id` |
| `source_id` — hundreds, and per-source health is the point | `user_id` |

`user_id` and `opportunity_id` belong in trace attributes and log fields, which
are sampled and indexed for exactly that purpose. One time series per opportunity
takes down the metrics backend long before anyone reads the dashboard.

Histogram buckets are explicit rather than the default exponential set, with
boundaries at and just below each target: a histogram whose nearest edges are 250
and 1000 cannot tell you whether a 300 ms p95 was met.

## Dashboard

There is no Grafana in the stack, and adding one is a §36 earned migration rather
than a starting condition. What exists instead:

- `--role=slo` — the full report, human-readable, non-zero exit on breach.
- `GET /internal/admin/slo` — the same report as JSON, on the operations surface.
  Admin-only: a breached objective is operational detail, and publishing it invites
  being read as a promise to users.
- The pipeline state distribution, which CLAUDE.md calls the pipeline dashboard. A
  large count that is moving is healthy; a small one that is not is an incident, so
  the oldest entry per state travels with the count.

## Step 21

Built — see the load test section below. One correction to an earlier assumption
recorded here: it does **not** need a metrics backend. A load test measures latency
client-side from its own requests, which is where a user-facing latency objective
belongs anyway.

---

# Load test (step 21)

`make loadtest`, or `--role=loadtest --users=N --concurrency=N --duration=Ns`.

Blueprint §35 calls step 21 "a test that can fail", so it **exits non-zero when an
objective is breached**. A load test that only prints numbers is a benchmark, and a
benchmark never tells you to stop.

## How it measures

Latency is taken **client-side**, from just before the request to just after the
body is fully read. That is where a user-facing latency objective belongs:
server-side handler time excludes connection setup, request parsing, response
serialization and the body write, all of which the user waits for. It also means
this needs no metrics backend to produce a percentile.

It drives the **real router** in-process (`buildRouter`, shared with `--role=api`),
with real session tokens hashed by `auth.HashToken`. A load test against a
hand-built handler measures the hand-built handler.

Two phases, because the blueprint has two latency objectives:

- **cold** — one request per user, nothing cached: retrieval and scoring run in full.
- **warm** — repeated requests, fit scores served from `fit_score`.

Percentiles are over **successful requests only**. A 500 returning in 2ms would
otherwise pull the p95 down, so a service failing fast would look fast. 4xx counts
as *available*: a 400 is the API correctly rejecting a bad request and a 404 is a
posting that does not exist, so counting them as failures would let a buggy client
spend our error budget.

## Measured capacity

288 real postings, ~188 eligible per profile, 5 distinct personas, 16 CPUs:

| concurrency | cold p95 | warm p95 | req/s | verdict |
|---|---|---|---|---|
| 8 | 115 ms | 70 ms | 131 | met |
| 16 | 176 ms | 148 ms | 141 | met |
| 32 | 571 ms | 413 ms | 98 | cached objective breached |
| 64 | 687 ms | 699 ms | 108 | cached objective breached |

**The feed meets its objectives up to roughly 16 concurrent requests, peaking near
140 req/s.** Past that the 300 ms cached target breaks *and throughput falls* —
141 to 98 req/s — which is the signature of queuing past the knee: latency grows
without more work getting done.

### What the bottleneck is not

The obvious suspect was the connection pool at `max_conns=10`. It is not:
quadrupling it to 40 moved warm p95 from 359 ms to 376 ms and throughput from 106
to 113 req/s at concurrency 32. Within noise.

### What it is

Per-request CPU proportional to the **candidate** count, not the returned count.
The feed scores every eligible candidate and returns seven. At ~188 candidates and
140 req/s that is roughly 26,000 candidate scorings a second, plus response
serialization.

This is the stage-1 cap doing its job — `retrieve.DefaultMaxCandidates` bounds the
worst case at 500 — but it means feed cost tracks the size of the eligible set
rather than the page size. The lever, when it is needed, is to shrink the eligible
set (tighter predicates, or scoring only the top-N by retrieval rank) rather than
to add connections.

## The bug this found

The first run measured **842 ms for a single feed request**. One request was
issuing **376 individual INSERTs** — 188 `eligibility_result` rows and 188
`fit_score` rows, one network round trip each — an N+1 write introduced in step 15
and never exercised until now.

Batching them through `sqlc`'s `:batchexec` (a pgx pipeline, one round trip) took
the same request to **82 ms**, and feed throughput from 6 to 78 req/s at
concurrency 8. The statements are unchanged; only the number of times the request
waits for the network is.

A second bug was in the harness itself. It spawned a goroutine per user bounded by
`errgroup.SetLimit`, so during the timed phase only the first `concurrency` users
ever ran, and the rest fired a single request each as the deadline passed — that
user's *first* request, cold, counted into the warm percentile. It reported warm
p95 968 ms where the truth was 134 ms, which looked like the service degrading
rather than the harness misreporting. Workers now pull users from a shared counter.

## What these numbers are not

**288 postings is far below the blueprint's 200–500K target corpus**, and the run
prints that caveat every time. These latencies are a floor, not a prediction:
retrieval cost grows with the eligible set, and at this size the planner does not
even choose the vector index (measured in step 13 — it prefers an exact scan under
a few thousand rows). A run against a realistic corpus is the test that matters,
and it needs the corpus first.
