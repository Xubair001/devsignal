---
name: pipeline-stage
description: Add, modify or debug a DevSignal pipeline stage or background worker — anything that claims work from the Postgres queue and advances an opportunity's pipeline_state. Use when the task mentions a worker, a queue, a job, a background task, a stage (parse, normalize, dedupe, enrich, embed), a sweeper, retries, backoff, leases, idempotency, stuck records, or a stranded backlog. Covers the claim pattern, in-transaction state advance, degrade-don't-block, and the concurrency rules.
---

# Working on a pipeline stage

The pipeline is a state machine in Postgres. Events, when they eventually exist, are a latency
optimization only. Every rule here exists to make a lost message cost seconds rather than data.

```
discovered -> fetched -> parsed -> normalized -> deduped -> enriched -> embedded -> ready
                                        |
                                        v
                                 failed_permanent   (visible in a queue, never lost)
```

## The claim pattern

Every stage claims the same way. Do not invent a second mechanism.

```go
// claim a bounded batch — backpressure comes from the batch size, never from memory
rows, err := q.ClaimBatch(ctx, ClaimBatchParams{
    State: "normalized",
    Limit: 100,
    Lease: 5 * time.Minute,
})
```

```sql
-- ClaimBatch
UPDATE opportunity SET lease_until = now() + $3, attempts = attempts + 1
 WHERE id IN (
   SELECT id FROM opportunity
    WHERE pipeline_state = $1
      AND next_attempt_at <= now()
      AND (lease_until IS NULL OR lease_until < now())
    ORDER BY next_attempt_at
    FOR UPDATE SKIP LOCKED
    LIMIT $2
 )
 RETURNING id, version;
```

`FOR UPDATE SKIP LOCKED` is what makes this safe with N workers and no coordinator. `SKIP LOCKED` is
also why you must never `ORDER BY random()` or add a second ordering key that reshuffles under
concurrency.

## The five rules

1. **Advance state in the same transaction as the work.** If the work writes rows, the state change
   is in that transaction. A committed side effect with an uncommitted state change is how records
   get processed twice; the reverse is how they get skipped.

2. **Be idempotent.** Key on `(opportunity_id, target_state)` or on `content_hash`. Assume every
   stage will run twice — a lease expiry, a redeploy mid-batch, or a duplicated event all cause it.
   Re-running must produce the same result, not a second row.

3. **Never block a later stage.** Enrichment failure must not prevent a posting reaching `ready`
   with a degraded quality flag. A posting with no extracted skills is still better than an
   invisible posting. This is the rule most often broken, because a strict chain feels tidier — but
   it makes the least reliable dependency (an external model) a hard prerequisite for visibility.

4. **Hold a lease, and release it.** A hard-killed worker must self-heal when the lease expires. On
   graceful shutdown, release claimed rows explicitly rather than waiting out the lease.

5. **Failures back off and then park.** `next_attempt_at = now() + backoff(attempts)`, and after the
   attempt ceiling move to `failed_permanent` with `last_error` populated. Parked is fine; invisible
   is not.

## Concurrent writes

The same posting is legitimately touched by three stages at once — a re-poll updating
`last_seen_at`, enrichment writing skills, dedup merging it. Row-level last-write-wins discards two
of the three.

```sql
UPDATE opportunity SET <disjoint columns>, version = version + 1
 WHERE id = $1 AND version = $2;
-- 0 rows affected -> reload and retry
```

Write **disjoint column sets** from different stages so the retry is cheap. `version` doubles as the
`opportunity_version` in the fit-score cache key, so bumping it correctly is what invalidates stale
scores.

## The sweeper

A stage is not complete until the sweeper knows about it. Add your state to the threshold table, or
records in it will strand silently and no dashboard will notice.

```sql
-- this query is the pipeline dashboard
SELECT pipeline_state, count(*), min(updated_at)
  FROM opportunity WHERE pipeline_state <> 'ready' GROUP BY 1;
-- an old min(updated_at) means something is stranded. alert on it.
```

## Concurrency and shutdown

```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(workers)          // always bounded. never one goroutine per item
```

Shutdown order, exactly:

1. Stop claiming new work.
2. `server.Shutdown(ctx)` if this process also serves HTTP.
3. Drain in-flight workers against a deadline.
4. Release any still-claimed rows (reset `lease_until`, restore prior state).
5. Exit.

Recover from panics at the worker-loop boundary. One malformed document must not kill the process and
take the other 99 records in the batch with it.

## Scheduling

There is no scheduler process. Schedules are rows, claimed with the same pattern:

```
source_schedule(source_id, interval, next_run_at, lease_until)
```

Self-balancing, no leader election, no double-firing. If you ever need a distributed lock, use a
Postgres advisory lock — Redis locking is not safe for correctness under partition.

## Observability

- One span per **batch**, not per record, with counts as span attributes. Per-record spans on an
  ingestion pipeline cost more than the compute they observe.
- Metric labels: `pipeline_stage`, `status`, `source_id` only. Never `opportunity_id` — one series
  per opportunity is unbounded cardinality.
- Log the `opportunity_id` (log fields are fine), never in a metric label, never any PII.

## Verify

```bash
go test ./internal/pipeline/...
make test-integration        # needs Postgres via Compose
make test -run TestIdempotent
```

Manual checks that catch the real bugs:

- Run the stage twice on the same record — assert no duplicate effect.
- `kill -9` a worker mid-batch — assert the sweeper recovers the records within the threshold.
- Rolling restart under load — assert zero lost jobs and zero 5xx.

## Done means

- [ ] Claims via `FOR UPDATE SKIP LOCKED` with a bounded batch and a lease
- [ ] State advances in the same transaction as the work
- [ ] Idempotent, verified by a test that runs it twice
- [ ] Does not block any later stage; degrades with a flag instead
- [ ] Backoff, attempt ceiling, `failed_permanent` with `last_error`
- [ ] Optimistic concurrency on `version`, disjoint columns
- [ ] Sweeper threshold registered for the new state
- [ ] Bounded concurrency; panic recovery at the loop boundary
- [ ] Shutdown releases claimed rows
- [ ] One span per batch; no high-cardinality metric labels
