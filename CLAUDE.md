# CLAUDE.md

Contributor-facing guidance for working in this repo. The binding specification is
[docs/DevSignal-Product-and-Architecture-Blueprint.docx](docs/DevSignal-Product-and-Architecture-Blueprint.docx) —
read it before changing anything structural. Where this file and the blueprint disagree, the
blueprint wins and this file is stale.

## Status

**Blueprint §35 steps 2–17 and 19 are done.** Repo, CI, local stack, config/logging/tracing, canonical
schema, identity, the pipeline spine, the first source adapter, normalization + dedup, and the
read API with liveness and ghost-risk signals, source-health monitoring, and the developer
profile with resume ingestion and verified erasure, cached LLM extraction, and versioned
embeddings with vector search, two-channel retrieval, and the eligibility gate with the fit
score and its explanation, the evaluation harness that gates every scoring change, and the
feed with saves, applications and dismiss-with-reason, and the admin console with quarantine,
merge tools and the purge drill. It ingests real postings from multiple boards:
`make add-source name=greenhouse:gitlab && make ingest name=greenhouse:gitlab`, or in bulk with
`--role=add-sources --file=boards.txt --reviewed-by=you`. `--role=source-health` prints today
against each source's own baseline.

`--role=retrieve --user=<id>` prints the predicates, eligible count and per-channel coverage
for one user — the operational answer to "why am I not seeing X".

`--role=match --user=<id>` prints the band, the per-factor arithmetic and the exclusions with
their specific reasons.

`make eval` is live and wired into CI. Current: NDCG@10 0.877, Precision@7 0.873, coverage 88%,
eligibility false positives 0, over 286 real postings and 2,574 labelled pairs. The labels are
**rubric-derived, not human** — the gate detects regressions, it does not measure product
quality. Behavioural labels replace the rubric at step 17.

`engagement_event` is the append-only log behind three things at once: product state, the
behavioural evaluation set that will replace the rubric labels, and the ranking decision record
blueprint §32 requires. Nothing there updates or deletes — un-saving appends.

`/internal/admin` is the operations surface: source health with a quarantine toggle, full
provenance with a working un-merge, the merge-candidate and listing-flag review queues, re-run
controls, and a source purge that counts before it deletes. Admin is a role on `app_user`,
granted only from the binary (`--role=grant-admin --email=…`) so a compromised session cannot
mint more admins. Every action lands in the hash-chained audit log in the same transaction as
the change.

Next: step 18 is the daily digest, which needs the email decisions in
[docs/OPEN-DECISIONS.md](docs/OPEN-DECISIONS.md) settled first. Step 20 (SLOs) is unblocked, and
so is the frontend — see [docs/FRONTEND-PLAN.md](docs/FRONTEND-PLAN.md).

Extraction runs against a `Provider` interface, so the model is a config value
(`EXTRACTION_MODEL`, default `claude-opus-5`). No API key is set in this
environment, so extraction has only ever run against a fake provider — the cache,
validation and degrade paths are proven, but no live model call has been made.
`--role=spend` reports what it costs once one is. Scaling past the two boards currently
registered is blocked on the Tier-A source list, not on code — `add-sources` takes a file.

Erasure is real and `make check-erasure` proves it. The one part still open is BACKUPS: either
crypto-shredding with per-user keys, or a documented maximum retention window stated to users
(blueprint §33.3). The live-store guarantee does not depend on that choice; the privacy notice does.
Company entity resolution is deterministic-only so far: the ATS board token and a revealed domain.
Alias and fuzzy matching are still to come, and are never auto-merged. Do not skip ahead because a
later step looks easier — the order is dependency-ordered, not thematic.

Three source families are built and verified against live endpoints: `greenhouse`, `lever`,
`ashby`. All three are bulk JSON with descriptions inline, which is what makes a frequent poll
affordable. [docs/TIER-A-SOURCES.md](docs/TIER-A-SOURCES.md) is the registry, including four
platforms reviewed and reachable but needing a different fetch strategy (SmartRecruiters' list
carries no description at all), and why the Tier-C exclusions are permanent rather than deferred.

[docs/OPEN-DECISIONS.md](docs/OPEN-DECISIONS.md) carries the blueprint §33.3 items with a
recommendation and reasoning for each:

| Open item | Status | Blocks |
|-----------|--------|--------|
| Final Tier-A source list with per-source review | **settled** — three platforms built, rest reviewed | nothing |
| Backup erasure approach | **recommended** — stated 35-day window over crypto-shredding | privacy notice wording |
| Email consent basis and sending domain | **recommended** — SES + double opt-in, Resend in dev | first digest send (step 18) |
| EU AI Act classification for the recommender | **needs counsel** — not an engineering decision | EU launch, not development |

## Project (WHAT / WHY)

**Developer Opportunity Intelligence.** Not a job board and not an aggregator — an explainable
recommender over a corpus we keep *true*. The product answers four questions in order: what should I
apply to, why, what am I missing, and what should I do to become more competitive. The fourth is the
moat, and it needs longitudinal demand data that cannot be backfilled.

Two constraints shape every engineering decision:

- **The corpus is small and dirty.** ~200–500K live developer-relevant postings worldwide — the
  whole scoreable corpus fits in one process's memory. But an estimated 20–33% of online listings are
  ghosts, so *verified liveness is the product*, not data hygiene. Difficulty here is data quality
  and evaluation, never scale.
- **Trust is the only asset.** It is lost by one invented field, not by a missing one. See Hard rule 3.

## Repo map (HOW)

Planned structure per blueprint §37.1. One binary; the role is selected by flag.

| Path | What lives here |
|------|-----------------|
| `cmd/devsignal/` | The single binary. `--role=api\|worker\|digest\|admin` |
| `internal/source/` | `SourceAdapter` implementations + the registry. One package per source family |
| `internal/pipeline/` | Work queue, state machine, sweeper, leases. The spine every worker plugs into |
| `internal/opportunity/` | Canonical model, provenance, normalization, dedup, liveness |
| `internal/company/` | Entity resolution on registrable domain, alias table |
| `internal/skill/` | Ontology, aliases, edges, demand time-series writes |
| `internal/enrich/` | Extraction, embeddings, the content-hash cache, hot/cold lanes |
| `internal/matching/` | Eligibility gate, retrieval, fit scoring, explanation |
| `internal/engagement/` | Feed, saves, applications, dismissals |
| `internal/digest/` | Notification budget, quiet hours, the empty case |
| `internal/auth/`, `internal/profile/` | Identity, sessions, tenancy, resume ingestion |
| `internal/admin/` | Source health, merge/unmerge, quarantine, flag queue |
| `internal/enrich/` | Extraction. The cache key is the determinism guarantee, not just a cost saving |
| `internal/ghostrisk/` | Observable ghost-listing signals. Pure; bands + reasons, never a bare score |
| `internal/sourcehealth/` | Parser-rot detection. Pure; relative drops against a source's own baseline |
| `internal/profile/` | Profile, resume ingestion, and the erasure inventory. Add a location when you add a store |
| `pkg/blob/` | Object storage. Per-user key prefixes are what make erasure one call |
| `internal/eval/` | Evaluation harness + labelled fixtures. Gates scoring changes in CI |
| `pkg/` | logger, telemetry, middleware, clients. Nothing domain-specific |
| `migrations/` | golang-migrate. Never hand-write DDL outside a migration |
| `testdata/` | Recorded source payloads (golden files). The highest-value tests in the repo |
| `docs/` | The blueprint, ADRs, runbooks |

No `proto/` and no per-service repos until a blueprint §36 trigger fires.

## Hard rules (would-be reverts)

Non-negotiable. Don't break them without raising it first. Each one maps to a specific failure the
blueprint's audit found.

1. **Money is `int64` minor units + ISO-4217 currency + period.** Never `float`. A range needs
   `salary_min_minor` *and* `salary_max_minor`. Anything normalized across currencies stores its
   `fx_rate_date`.

2. **No score shown to a user may depend on the current time.** `fit_score` is a pure function of
   `(profile_version, opportunity_version, model_version)` — cacheable, reproducible, explainable.
   Recency, urgency and saturation live in `priority_score`, which orders the feed and is **never**
   displayed as a match. Putting freshness back inside fit reintroduces four bugs at once.

3. **Nothing renders that cannot be derived from observed data.** No competitiveness estimate (we
   have no applicant counts), no imputed salary presented as the employer's, no bare match
   percentage implying a probability. Bands and factor contributions until calibration exists, then
   percentiles. Blueprint §3 is binding on the UI.

4. **Never `http.DefaultClient`.** One explicit client per source tier, with dial / TLS /
   response-header / total timeouts, `MaxConnsPerHost`, a per-host token bucket, and every body read
   through `io.LimitReader` with a hard cap. Untrusted remote content is the most reliable way to
   kill a Go service.

5. **Never create an account, authenticate, or accept terms on a source we ingest.** This is the
   single rule that separates our legal posture from hiQ's. Tier A and B only; see blueprint §12.

6. **The DB row is the truth; events are a latency optimization.** Every stage claims work by
   `pipeline_state` with `FOR UPDATE SKIP LOCKED`, is idempotent, advances state in the *same*
   transaction as the work, and holds a lease. Losing an event must cost seconds, never data.

7. **No stage may block another.** Enrichment failure must not prevent a posting reaching `ready`
   with a degraded quality flag. A posting with no extracted skills is still better than an
   invisible posting.

8. **Never call the model when the extraction cache would hit.** Key is
   `(content_hash, prompt_version, model_id, schema_version)`. This is the determinism guarantee, not
   just a cost saving — re-extracting makes fit scores flap for postings that did not change.

9. **Closure requires a successful poll in which the posting was absent.** Never infer closure from a
   failed fetch, a timeout, or a quarantined source. That mistake lets one source outage delete the
   corpus.

10. **Everything a score depends on carries its version.** Ontology, model, prompt, schema, embedding
    and weights versions are written on the artifact. A model swap without a version column is an
    un-migratable rebuild.

11. **Merges are reversible.** Dedup writes a canonical `opportunity` and keeps every
    `opportunity_source` row with `merge_reason` and `merge_confidence`. Never delete a source row. A
    false merge hides a real job and is otherwise invisible.

12. **Metrics carry bounded labels only** — `source_id`, `pipeline_stage`, `status`,
    `http_status_class`, `model_id`. `user_id` and `opportunity_id` go in trace attributes and log
    fields, never metric labels. One series per opportunity is unbounded cardinality.

13. **No PII in logs, ever.** Not in errors, not in debug, not "temporarily".

14. **Domain logic takes time from an injected `Clock`.** Never `time.Now()` inside freshness,
    expiry, liveness or lease logic — it is untestable otherwise. All timestamps `timestamptz`, UTC.

15. **Migrations are expand/contract.** Nullable add + batched backfill, `CREATE INDEX
    CONCURRENTLY`, never a blocking `ALTER` on a hot table. Rolling deploys must survive both
    directions.

16. **Any change touching scoring, retrieval or extraction must pass `make eval`.** CI fails on
    NDCG@10 regression. Weight changes are not a matter of taste.

17. **Every new user-derived artifact is added to the erasure inventory in the same change.**
    Embeddings, index documents, caches, exports, warehouse copies. Blueprint §31.2 lists the
    locations; the completeness script must still pass.

18. **`pgx` directly, no ORM.** `sqlc` for static queries; a query builder only for the faceted
    search endpoint. Do not contort `sqlc` and do not adopt an ORM to solve one endpoint.

19. **Do not add NATS, OpenSearch or Kubernetes until its blueprint §36 trigger fires.** They are
    earned migrations with a measurement attached, not starting conditions. v1 is Postgres + Redis.

20. **A new embedding version ships with its own partial HNSW index, in the same migration.**
    Retrieval always filters by `embedding_version`, since distances across models are
    meaningless. An unconditional HNSW index cannot serve that filter: measured on 50k vectors
    with the queried version holding 1,000 of them — the shape of every rollout — the planner
    fell back to a sequential scan, 12.6 ms against 0.99 ms, growing linearly. It can also
    return fewer rows than asked for, because a filter outside the index is applied after the
    graph walk. `TestEveryLiveEmbeddingVersionHasAPartialIndex` fails if a version has vectors
    no index covers.

21. **Retrieval channels carry their predicates inside the query, and never force an index.**
    Filtering after a kNN walk throws away candidates the walk already spent its budget on:
    measured on 50k vectors with a 1%-selective predicate, asking for 100 returned 4. The fix
    is `hnsw.iterative_scan` (set per transaction with SET LOCAL) plus leaving the planner free
    to choose — with a small eligible set it uses an exact scan and returns everything, which
    is both correct and cheap. Forcing the index re-creates the bug.

22. **A retrieval channel that matches most of the corpus is broken, not generous.** Stage 1
    exists to bound downstream work. The keyword channel matches titles only: over
    title + description it returned all 199 real postings, because company boilerplate names
    the company's own platform in every description. Over titles it returned 34, all genuine
    matches. Description semantics belong to the vector channel, which embeds the full text.

23. **A factor with no observable data is excluded from the achievable maximum, never given a
    neutral value and never renormalized away.** `fit` reports earned points out of achievable
    points, and the band reads the ratio. Redistributing a missing factor's weight up to 1 was
    the first design, and it meant removing information RAISED the score: a posting nothing
    could be extracted from, whose one legible factor matched, read as a Strong fit; and a user
    with no skills listed outscored the same user after adding one skill that matched half the
    requirements. Any redistribution scheme has that property. Below 60% coverage the band is
    "Not enough information", which is a different statement from "Stretch" and only one of them
    is about the user.

24. **Reversing a merge must restore data, not just record an intention.** `opportunity_merge`
    stores the ids of the source rows it moved, not only a count: with two merges into one
    canonical there is nothing to infer from, and the original `UndoMerge` stamped `undone_at`
    while changing no data at all. An un-merge is three statements in one transaction — restore
    exactly those rows, clear `merged_into` and stamp `unmerged_at`, mark the merge reversed.
    Dedup skips anything with `unmerged_at` set: a human said these are different roles and a
    simhash does not overrule that, or the operator watches their un-merge undo itself.

25. **A cleanup delete is scoped to what the operation is about.** The source purge deletes
    orphaned postings only from the ids that source contributed to. A table-wide
    `WHERE NOT EXISTS` orphan sweep would remove unrelated postings as a side effect of purging
    one source — cleanup with unbounded blast radius. Destructive admin actions also count first
    and require the operator to echo the number back.

## The two numbers

The most commonly misunderstood part of the system. Keep them separate in code, not just in the UI.

```
fit_score        stable    f(profile_v, opportunity_v, model_v)
                           cached; invalidated only by a version change
                           displayed, with per-factor contributions

priority_score   volatile  g(fit, age, closing_soon, saturation)
                           computed at read time; orders today's feed
                           never displayed, never persisted as a match

internal/matching/fit.go        stage 1, pure, no clock
internal/matching/priority.go   the ONLY place the clock may touch what a user sees
internal/matching/eligibility.go stage 0 boolean; failures explained, never scored
```

Fit is a weighted sum over bounded factors whose weights total 1, so each term contributes exactly
`w_i * f_i` — the explanation *is* the model. Keep it linear and monotone; that is what makes the
displayed breakdown faithful rather than a post-hoc story.

## Pipeline

```
discovered -> fetched -> parsed -> normalized -> deduped -> enriched -> embedded -> ready
                                        |
                                        v
                                 failed_permanent  (visible in a queue, never lost)
```

A sweeper re-enqueues anything stuck past its stage threshold, forever. The state distribution *is*
the pipeline dashboard:

```sql
SELECT pipeline_state, count(*), min(updated_at)
  FROM opportunity WHERE pipeline_state <> 'ready' GROUP BY 1;
-- an old min(updated_at) means something is stranded. alert on it.
```

## Source policy

| Tier | What qualifies | Posture |
|------|----------------|---------|
| A — Sanctioned | Public ATS job-board APIs, official APIs, RSS/Atom, sitemaps, partner feeds | Build here first. Target ~80% of corpus |
| B — Permitted crawl | Public pages, robots-allowed, no login wall, no click-through terms, polite rate | Per-source review + recorded legal basis |
| C — Prohibited | Behind a login, terms forbid automated access, needs an account, actively enforced | Do not ingest |

Tier A is load-bearing, not merely safe: `(ats_type, ats_job_id)` is a stable global identifier, so
dedup is nearly free; the parsing surface collapses to JSON; and conditional GETs are cheap enough to
make the 5-minute freshness SLO real. Verified: a second Ashby poll returns 304 against a stored ETag.

Built: `greenhouse`, `lever`, `ashby` — see [docs/TIER-A-SOURCES.md](docs/TIER-A-SOURCES.md) for the
platform reviews, the per-platform quirks that cost something to learn, and what is deliberately out.
Two things worth knowing before adding a family: a bulk board is one document, so the body cap has to
fit the largest legitimate board (Ashby's `openai` board is 12.4 MB, which the original 8 MiB cap
rejected outright), and neither Lever nor Ashby returns a company name — leave the field empty and let
resolution fall back to the board token rather than deriving a name from a slug.

## Prerequisites

Verify with `./scripts/check-prereqs.sh`. Current machine state is recorded in
[docs/PREREQUISITES.md](docs/PREREQUISITES.md).

Local host ports are deliberately **off the defaults** — this machine already runs a native
redis on 6379 and other projects' databases on 5432/55432. Postgres 65432, Redis 65379,
MinIO 65000/65001; override with `POSTGRES_PORT` etc. in `.env`.

| Tool | Needed | Why this version |
|------|--------|------------------|
| Go | **≥ 1.25** | 1.25 made `GOMAXPROCS` container-aware from the cgroup CPU limit. Do **not** set `GOMAXPROCS` in a manifest — it overrides the correct value. Still set `GOMEMLIMIT` to ~90% of the memory limit |
| PostgreSQL | ≥ 16, with `pgvector` | Rows, FTS (`tsvector`) and kNN all live here in v1 |
| Docker + Compose | any current | Local Postgres, Redis, MinIO, OTel collector |
| golang-migrate | any current | Migrations |
| sqlc | any current | Static query codegen |
| golangci-lint, staticcheck | any current | CI gates |

## Adding things

- **A source** → use the `add-source-adapter` skill. Never hand-roll the HTTP client or skip the
  golden fixture.
- **A pipeline stage** → use the `pipeline-stage` skill. Claim by state, idempotent, lease, advance
  in-transaction.
- **A scoring factor or weight** → use the `scoring-change` skill. It will not merge without the
  eval harness.
- **A migration** → use the `db-migration` skill. Expand/contract only.
- **Anything storing user-derived data** → use the `privacy-surface` skill. The erasure inventory is
  part of the change.
- **An API endpoint, service or store method** → use the `backend-api-conventions` skill. Layering,
  DTO boundaries, error mapping, keyset pagination.
- **Any Go service code** → the `go-production-patterns` skill has the concrete shapes for HTTP
  clients, bounded concurrency, graceful drain and pool sizing.
- **Any UI** → use the `frontend-conventions` skill. It carries the binding display rules: never
  render a raw percentage, never invent a signal, always show verified-open state.

## Verifying changes

```bash
make fmt vet lint          # gofmt, go vet, golangci-lint, staticcheck
make test                  # unit, with -race
make test-golden           # source parser fixtures — the ones that catch real breakage
make eval                  # NDCG@10 / Precision@7 / coverage. Gates scoring changes
make test-integration      # provisions a disposable database, then runs the suite
make check-erasure         # asserts a deleted identifier appears nowhere
```

The integration suite is destructive — the queue tests claim and advance rows table-wide,
because that is what a worker does. It therefore runs only against a database whose name ends
in `_test`, provisioned and dropped per run by `make test-db`; `internal/dbtest` refuses
anything else. Do not hand it `DATABASE_URL` for a database you care about, and do not "fix"
that refusal by relaxing the check.

Before calling anything done, walk blueprint §38's production readiness gate. It is binary.

## Branches

Work on `dev`. Feature branches, when needed, come off `dev`.

`main` is updated by the repo owner via merge or PR — do not push to it, and do
not merge into it. An automated agent's job ends at pushing `dev`.

CI runs on both branches, so work on `dev` is verified before it is ever
proposed for `main`.
