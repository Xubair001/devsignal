---
name: db-migration
description: Write or review a DevSignal Postgres migration — new tables, columns, indexes, enums, constraints, backfills, or pgvector index changes. Use whenever the task involves schema changes, DDL, golang-migrate, sqlc regeneration, adding a field to the opportunity or profile model, or an index for a slow query. Enforces expand/contract, non-blocking DDL, and the version columns the scoring cache depends on.
---

# Writing a migration

All DDL ships as a `golang-migrate` pair under `migrations/`. Never hand-write DDL outside a
migration, and never edit a migration that has run anywhere other than your own laptop.

```
migrations/000123_add_ghost_risk_score.up.sql
migrations/000123_add_ghost_risk_score.down.sql
```

## Expand / contract — always

Rolling deploys mean old and new code run **simultaneously** against one schema. A migration that
only works after every pod has restarted will break the deploy it ships in.

| Phase | What you do | Safe because |
|-------|-------------|--------------|
| Expand | Add nullable column / new table / new index | Old code ignores it |
| Backfill | Batched updates, throttled | No long transaction, no table lock |
| Migrate reads | New code reads the new column, tolerates NULL | Both versions work |
| Migrate writes | New code writes both old and new | Rollback still possible |
| Contract | Drop the old column — **a separate, later migration** | Nothing references it |

Never combine expand and contract in one migration. The rollback path is the whole point.

## Non-blocking DDL

```sql
-- SAFE
ALTER TABLE opportunity ADD COLUMN ghost_risk_score real;      -- nullable, no default: instant
CREATE INDEX CONCURRENTLY idx_opp_state_next
    ON opportunity (pipeline_state, next_attempt_at);
ALTER TABLE opportunity ADD CONSTRAINT chk_salary
    CHECK (salary_max_minor >= salary_min_minor) NOT VALID;    -- then VALIDATE separately
```

```sql
-- DANGEROUS on a hot table
ALTER TABLE opportunity ADD COLUMN x text NOT NULL DEFAULT 'y';  -- rewrites on older PG
CREATE INDEX idx_foo ON opportunity (foo);                       -- blocks writes
ALTER TABLE opportunity ALTER COLUMN foo TYPE bigint;            -- full rewrite + lock
ALTER TABLE opportunity VALIDATE CONSTRAINT chk_x;               -- fine, but do it separately
```

`CREATE INDEX CONCURRENTLY` **cannot run inside a transaction**, so that migration must be marked
non-transactional. It can also leave an INVALID index if it fails — check for one before retrying:

```sql
SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid;
```

## Backfills

Batched, throttled, resumable. Never one statement over the whole table.

```sql
-- run in a loop from application code or a one-off command, not inside the migration
UPDATE opportunity SET ghost_risk_score = 0
 WHERE id IN (
   SELECT id FROM opportunity WHERE ghost_risk_score IS NULL LIMIT 5000
 );
```

A single `UPDATE` over 500K rows holds one transaction open long enough to bloat the table and block
autovacuum. Keep batches small and commit between them.

## Columns this system requires

If you are adding a table that participates in scoring, provenance or user data, it needs the
matching bookkeeping — these are not optional decoration:

| Concern | Columns |
|---------|---------|
| Concurrency | `version int NOT NULL DEFAULT 0` — the scoring cache key depends on it |
| Pipeline | `pipeline_state`, `attempts`, `last_error`, `next_attempt_at`, `lease_until` |
| Liveness | `first_seen_at`, `last_seen_at`, `closed_at`, `close_reason`, `consecutive_misses` |
| Provenance | `source_id` FK — required so source-level purge stays a single operation |
| Reproducibility | `ontology_version`, `model_id`, `prompt_version`, `embedding_version` |
| Tenancy | `tenant_id` — add it now even though nothing uses it yet |
| Money | `*_minor bigint`, `currency char(3)`, `period`, `fx_rate_date` — never `numeric` for a rate you didn't fix in time, never `float` |

Timestamps are `timestamptz`, always. Never `timestamp`.

## pgvector

```sql
ALTER TABLE opportunity_embedding ADD COLUMN embedding vector(768);
CREATE INDEX CONCURRENTLY idx_opp_emb_hnsw
    ON opportunity_embedding USING hnsw (embedding vector_cosine_ops);
```

Two rules. The index's operator class must match the distance function your query uses, or the index
is silently ignored and you get a sequential scan. And a dimension change is a **new column plus a
dual-write migration**, never an `ALTER TYPE` — vectors from two models are not comparable, and
thresholds are model-specific constants that must be re-tuned.

## After the migration

```bash
make migrate-up
make sqlc                 # regenerate — a schema change without this leaves stale Go types
make migrate-down         # prove the down path actually works
make migrate-up
make test-integration
```

`sqlc generate` is not optional. Forgetting it produces Go structs that no longer match the table,
and the failure surfaces far from the cause.

## Done means

- [ ] Up **and** down migration, both tested
- [ ] Expand and contract are separate migrations
- [ ] No blocking `ALTER` or plain `CREATE INDEX` on a hot table
- [ ] `CONCURRENTLY` migrations marked non-transactional
- [ ] Backfill is batched and resumable, outside the migration
- [ ] `version`, `tenant_id`, provenance and version columns present where required
- [ ] `timestamptz`; money in minor units
- [ ] `make sqlc` run and the generated diff reviewed
- [ ] Erasure inventory updated if the table holds user-derived data
