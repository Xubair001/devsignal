# CLAUDE.md

Contributor-facing guidance for working in this repo. The binding specification is
[docs/DevSignal-Product-and-Architecture-Blueprint.docx](docs/DevSignal-Product-and-Architecture-Blueprint.docx) —
read it before changing anything structural. Where this file and the blueprint disagree, the
blueprint wins and this file is stale.

## Status

**Blueprint §35 steps 2–17 and 19–21 are done.** Repo, CI, local stack, config/logging/tracing, canonical
schema, identity, the pipeline spine, the first source adapter, normalization + dedup, and the
read API with liveness and ghost-risk signals, source-health monitoring, and the developer
profile with resume ingestion and verified erasure, cached LLM extraction, and versioned
embeddings with vector search, two-channel retrieval, and the eligibility gate with the fit
score and its explanation, the evaluation harness that gates every scoring change, and the
feed with saves, applications and dismiss-with-reason, and the admin console with quarantine,
merge tools and the purge drill, the SLOs with error budgets and burn-rate alerts, and the load
test that measures them. It ingests real postings from multiple boards:
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

The web front end is public at `/` (landing), `/login` and `/register`, with the console under
`/app`. Separated rather than switching on the session at the root, so a deep link means one thing
whoever follows it.

**RBAC is a role on the identity, not a lookup.** `Authenticate` already loads the user row, so
`auth.Identity` carries `Role` and revoking admin takes effect on the very next request.
`RequireAdmin` reads the context and answers **404, not 403** — a 403 confirms the surface exists.
`/api/v1/me` returns `role` and `is_admin` so the console can hide what the caller cannot use; that
is usability, and the server gate is the boundary. `navFor(isAdmin)` filters the sidebar, the mobile
drawer and the ⌘K palette from one source, because three lists that each decide what exists is how a
hidden destination stays reachable from one of them. Verified with a real member account: `role:
"user"`, 404 on all four admin endpoints, own surfaces 200.

`/internal/admin` is the operations surface: source health with a quarantine toggle, full
provenance with a working un-merge, the merge-candidate and listing-flag review queues, re-run
controls, and a source purge that counts before it deletes. Admin is a role on `app_user`,
granted only from the binary (`--role=grant-admin --email=…`) so a compromised session cannot
mint more admins. Every action lands in the hash-chained audit log in the same transaction as
the change.

`--role=slo` prints every objective against its target and exits non-zero on a breach, so a
cron entry is a working alert with no metrics pipeline. `GET /internal/admin/slo` serves the
same as JSON. Five of the twelve objectives report **unmeasurable with the reason attached**
rather than green — see [docs/SLO.md](docs/SLO.md) for the alert rules and why that matters.

`make loadtest` drives the real router in-process and exits non-zero on a breach. Measured: the
feed meets its objectives to ~16 concurrent requests, peaking near 140 req/s over 288 postings.
The bottleneck is per-request CPU proportional to the CANDIDATE count, not the connection pool —
quadrupling the pool changed nothing.

The console front end is built, in [`web/`](web/) — React + TypeScript + Vite, TanStack Query,
Tailwind v4, four routes (overview, feed, sources, flags). Its binding display rules and the
reasoning behind them are in [web/README.md](web/README.md); the settled framework decision is in
[docs/FRONTEND-PLAN.md](docs/FRONTEND-PLAN.md), which previously recommended SvelteKit and was
wrong — the checked-in `frontend-conventions` skill already specified React.

Building it found a real gap rather than confirming the API: the feed returned a ranking with no
posting attached, so a card had no company, salary, apply link or liveness — the product's central
claim. See hard rule 27. The feed DTO now embeds `opportunity.Summary`, shared with the browse list
so the two cannot drift.

**Email verification is built and is the transactional half of `internal/mail`.** Transactional
mail and the digest share a **transport** and never a consent gate — a user who withdraws digest
consent still needs to verify an address and reset a password, so the consent check lives in
`internal/digest` and deliberately not in the transport. `MAIL_SENDER=log` writes the message,
link included, to `MAIL_LOG_DIR`, which is what makes signup completable with no provider.

A verification link works **exactly once**: `ConsumeUserToken` claims it with
`consumed_at IS NULL` in the WHERE clause, so two concurrent requests cannot both win and a
replayed link finds nothing. Expiry is checked in SQL, not Go, so a clock difference between
process and database cannot widen the window. Only the hash is stored. A resend supersedes the
outstanding link rather than adding a second one. Expired, spent and never-issued all report
**identically** — distinguishing them tells a caller whether an address is registered. A send
failure never fails the signup: the account exists either way and a resend is one click.
`/api/v1/me` returns `email_verified`, because the digest refuses to mail an unverified address
and without that the console would be a silent dead end.

`--role=digest` is step 18. Everything blueprint §4.3 requires is built — a structural daily cap,
a weekly cap, quiet hours in the user's own timezone, a minimum **band** to interrupt on, and the
explicit empty case — with the transport behind a `Sender` interface. `DIGEST_SENDER=log` renders
each digest to disk and delivers nothing, the same shape extraction uses with a fake provider, so
only the last hop is unproven. Which provider actually sends is still open
([docs/OPEN-DECISIONS.md](docs/OPEN-DECISIONS.md) §3) and the default sender **fails** rather than
silently dropping mail. `--role=digest-optin` records evidenced consent.

Against the real corpus the digest correctly sends **nothing**: 188 roles are eligible and none
reach "Strong fit", because coverage sits under 60% without extraction. That is the feature working
— see hard rule 28.

Next: step 22 (calibration) needs outcome data the engagement log is now collecting. Step 26
(market intelligence) is blocked on a demand-series writer that **does not exist** — see
[docs/FRONTEND-PLAN.md](docs/FRONTEND-PLAN.md).

Extraction runs against a `Provider` interface with **two implementations**:
`anthropic` (SDK, prompt caching, schema-constrained output) and `openai` (raw HTTP per hard rule
4, strict Structured Outputs). `enrich.Resolve` picks one — explicitly via `EXTRACTION_PROVIDER`,
or inferred from whichever key is set. Two keys and no explicit choice is an **error**, not a
precedence rule: which vendor read a posting is part of its cache key.

**Extraction has now run live**, against OpenAI. The same `JSONSchema()` is accepted by OpenAI's
strict mode unmodified, so there is one schema, not two. Two things were measured rather than
assumed:

- `gpt-5-mini` at `reasoning_effort=minimal` returned **12 skills in 280 output tokens and 5.2 s**.
  At default effort the same posting cost **2,531 output tokens and 38 s** and returned **eleven**.
  Extraction is mechanical reading; a reasoning budget bought a worse answer. Minimal is the default.
- OpenAI caches prompt prefixes automatically only from 1024 tokens, which `Instructions` does not
  reach. So the content-hash cache in `internal/enrich` — not the vendor's — is what makes
  re-extraction free. Hard rule 8 is provider-neutral by design.

`ModelID()` is vendor-qualified (`openai:gpt-5-mini`), because two vendors can ship the same model
name and hard rule 8 makes that string the determinism guarantee. `--role=spend` reports cost per
model id per lane.

**The 45 skill points now score.** Extraction was only half the blocker; the other two halves were
an ontology and a way to write profile skills, and both are built:

- `internal/skill` normalizes the model's words onto a canonical vocabulary. Without it "Go",
  "Golang" and "Go (Golang)" were three rows and could never match a profile — measured: 10
  postings produced 91 distinct skills with almost no overlap. `Normalize` is where the difficulty
  is, because `+`, `#` and `.` carry meaning ("C++" is not "C", ".NET" is not "net") while every
  other punctuation mark does not. `Load` validates the hand-edited file and refuses a duplicate
  slug, an alias bound to two skills, or an edge to a slug that does not exist — it caught a real
  conflict on first run.
- `PUT /api/v1/profile` accepts skills, resolved through the **same** ontology. The profile
  deliberately **cannot mint** new skills: a typo would become a vocabulary entry that then matches
  no posting, so unrecognised names come back in `unresolved_skills` and are shown struck through.
  Extraction is the asymmetric case — an unrecognised phrase there is evidence from a posting and
  is kept.

**Resume skills extract too, and `--role=resume-skills` is where the privacy rule lives.** The
resume text is REDACTED before it leaves the process: the leading header block goes (located by the
first section heading, falling back to the opening 200 characters), then every email address, URL,
phone-shaped number and 7+ digit run. What was sent is recorded on the `resume` row —
`skills_field_set`, `skills_redaction_version`, and counts, never values.

The record lives on the resume row rather than in the shared `extraction` table on purpose.
`extraction` is keyed on a content hash and has no `user_id`, so a resume extraction cached there
would be user-derived data erasure could not scope a delete to. On the resume row it cascades with a
store already in the inventory, so this added no new erasure location.

Two things the redactor got wrong first, both found by running it rather than reading it:
`profile.cleanText` collapses every newline to a space before the text is stored, so an earlier
line-counting header drop was a **complete no-op in production** while cheerfully recording "6
header lines dropped" — the candidate's name was reaching the model. And the URL pattern required a
scheme, so `linkedin.com/in/firstname-lastname` in the body would have leaked. Both have regression
tests over the flattened one-line form that production actually stores.

Seniority and years from a resume are **recorded, never written onto the profile**. Those are the
user's own stated preferences, and overwriting what a person typed with what a model read off their
document is the same category error as showing an imputed salary as the employer's.

Verified end to end: "Golang" → Go, "K8s" → Kubernetes, "Postgres" → PostgreSQL, "CI/CD" → cicd,
and `--role=match` now reports `+12 of 35 from required skills (1 of 3 required skills)` with
coverage at 90 of 100 points instead of 45. Bands read "Stretch" rather than "Not enough
information".

`--role=skills` seeds the vocabulary, `--unresolved` ranks the phrases it does not know by how many
postings use them (the evidence-driven growth path — a phrase on forty postings is worth adding, one
on a single posting is noise), and `--demand` snapshots `skill_demand_daily`, which had **no writer
at all** before this. It is a recomputed snapshot rather than an incrementing counter: a counter
would double-count every re-extraction and drift with nothing to audit against, and this is the one
series in the system that cannot be rebuilt. Scaling past the two boards currently
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
| Email consent basis and sending domain | **recommended** — SES + double opt-in | real *delivery*; step 18's logic is built and verifiable without it |
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
| `internal/skill/` | Ontology (264 canonical skills, 623 normalized aliases, 81 edges), alias resolution, the demand time-series writer |
| `internal/enrich/` | Extraction, embeddings, the content-hash cache, hot/cold lanes |
| `internal/matching/` | Eligibility gate, retrieval, fit scoring, explanation |
| `internal/engagement/` | Feed, saves, applications, dismissals |
| `internal/digest/` | Notification budget, quiet hours, the minimum band, the empty case. Transport behind a `Sender` interface |
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
| `internal/apicontract/` | Reflection test over the json paths the console reads. Catches a renamed tag, which neither compiler can |
| `web/` | The console: React + TS + Vite, TanStack Query, Tailwind v4. Display rules in `web/README.md` |
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

26. **An objective, metric or score we cannot measure reports as unmeasurable with the reason
    attached — never as green.** Five of the twelve SLOs cannot be measured yet: liveness
    accuracy needs the employer's answer, dedup precision needs labelled pairs, the digest does
    not exist. A dashboard showing green for something nobody measured is worse than one with a
    visible gap, because the gap prompts a question and the false green ends the conversation.
    `TestLivenessAccuracyStaysUnmeasurableUntilGroundTruthExists` fails if anyone flips the
    product's central claim to measurable, since that means finding ground truth or starting to
    guess. This is hard rule 3 applied to ourselves.

27. **A feed item carries the posting, and the posting is not optional.** The daily feed may not
    show a role whose open state is unknown, so `FeedItem.Posting` is a value, never a pointer and
    never `omitempty` — the handler drops an item it cannot describe rather than sending a partial
    one. This was wrong for the whole of step 17: the matcher returned a ranking and nothing
    carried the posting, so a card had no company, no salary, no apply link and no way to say
    "verified open". Because the client's DTOs are hand-written, neither compiler noticed.
    `internal/apicontract` is the guard: a reflection test over every json path the console reads,
    which fails on a rename. Fetch the postings for the page, not for the candidate set —
    loading 188 rows to render 7 cost 40 ms of the cold p95. The same applies to the saved list,
    which was ids and timestamps only — and a save is revisited days later, which is exactly when
    liveness matters most.

29. **A posting body is third-party HTML and is sanitized at the serve boundary.**
    `opportunity.description_text` holds the board's bytes verbatim — every row in the corpus
    starts with a `<div>` — and it was served as `description_html` with no filtering at all. A
    client rendering that is stored XSS: a script in an employer's own posting would run with an
    operator's session. `opportunity.SanitizeDescription` runs an allow-list (bluemonday) over it,
    and the tests cover ten vectors a tag blocklist misses — `img onerror`, `javascript:` and
    `data:` URLs, inline handlers, `<style>`, `<form>`, `<svg><animate onbegin>`, `<object>`.
    Serve-time and not ingest-time on purpose: sanitizing on the way in would change the content
    hash, invalidate every cached extraction, and destroy the answer to "what did the board
    actually publish". `class`, `id` and `style` are stripped along with the rest, so third-party
    markup cannot reshape the page around itself.

28. **The bar for interrupting someone is a BAND, and "Not enough information" clears none of
    them.** The digest's minimum is `strong` or `worth_a_look`, never a numeric threshold: hard
    rule 3 forbids treating an uncalibrated score as a probability, and that applies to our own
    send decision as much as to anything rendered. `BandInsufficient` is not the bottom of a
    ladder — it says we could observe less than 60% of the model — so treating it as a low score
    would mean emailing people on the strength of data we admit we do not have, and doing it most
    often exactly when extraction is broken. An unrecognized bar sends nothing rather than
    everything: failing closed is the only safe direction for a rule about interrupting people.
    A related pair: quiet hours **defer and write no row**, so the day stays claimable, while an
    `empty` outcome writes a row that is **provisional** — ingestion runs all day and a Strong fit
    appearing at 10:00 must still reach the user, so a later run upgrades that row in place. Only
    `sent` is terminal, and the `outcome <> 'sent'` guard in the UPDATE is the daily cap.

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
./bin/devsignal --role=slo  # every objective against its target; exits non-zero on a breach
make loadtest              # drives the real API and checks the latency objectives
./bin/devsignal --role=digest --dry-run  # compose and print; claim no day, send nothing
```

The Go targets name `./cmd/... ./internal/... ./pkg/...` rather than `./...` on purpose: an npm
dependency under `web/node_modules` ships its own Go package, and `./...` compiles and tests it.

For the console, `npm run build` runs `tsc -b` first, so a type error fails the build:

```bash
cd web && npm install && npm run dev   # :5173, proxies /api and /internal to :8080
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
