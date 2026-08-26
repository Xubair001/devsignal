# Service level objectives, dashboards and alerts

Blueprint §28's table, as running code. `internal/slo` holds the objectives and the
error budget arithmetic; `--role=slo` prints the report and exits non-zero on a
breach; `GET /internal/admin/slo` serves the same thing as JSON.

## The rule this is arranged around

**An objective we cannot measure reports as `unmeasurable` with the reason
attached — never as green.**

Five of the twelve currently cannot be measured. A dashboard that shows green for
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
| Digest generation | inside 30 min | 1 day | **unmeasurable** — digest is step 18 |
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

## What step 21 needs from this

Step 21 is a load test against these objectives — "a test that can fail". It needs
two things that do not exist yet: a metrics backend to read latency percentiles
from under load, and enough corpus for the feed to be doing real work. The
objectives and the arithmetic are ready for it.
