# Frontend plan

There is **no frontend yet**. Nothing has been built, and no framework has been chosen — that
choice is still open and is yours. What exists is the API it will consume, and a set of display
rules that are binding rather than stylistic.

Read [.claude/skills/frontend-conventions/SKILL.md](../.claude/skills/frontend-conventions/SKILL.md)
before writing any UI. This document is the plan; that skill is the contract.

---

## The rules that are not negotiable

These come from blueprint §3 and they are the difference between this product and a job board with
a match percentage on it. Trust is lost by one invented field, not by a missing one.

| Show | Never show |
|---|---|
| A band: **Strong fit / Worth a look / Stretch / Not enough information** | A bare percentage. It implies a probability, and no calibration has been measured |
| Per-factor points: **"+29 of 35 from required skills"** | A raw score out of 100 as the headline |
| **"72 of a possible 90 — pay was not disclosed"** | A score presented as if the whole model was evaluated |
| **"Not scored: this posting's required skills have not been extracted yet"** | The same factor rendered as a zero |
| **"Verified open, checked 2 hours ago"** | A posting with no liveness state |
| **"Not disclosed"** for salary | An imputed or estimated salary shown as the employer's figure |
| **"You told us you applied on 3 March"** | "Applied" as though we verified it with the employer |

Two of these have already been enforced in the API and are covered by tests, so a frontend cannot
accidentally break them: there is **no percentage field** and **no priority field** in any feed
response. Priority orders the feed and is volatile by design — exposing it would let a client
render a "match" that changes overnight because a posting aged.

The one that needs the most care in the UI is the third row. Most postings today score against a
partial model, because extraction has not run against a live provider yet. A design that assumes
every card shows "84/100" will look broken; a design built around *band + what could and could not
be scored* will look honest and will not need reworking later.

---

## The API, as it exists today

All of this is live and testable now. `GET` unless noted; everything except `/opportunities`
requires a session.

### Reading

```
GET  /api/v1/opportunities              corpus browse, keyset-paginated
GET  /api/v1/opportunities/:id          one posting, with liveness and ghost-risk signals

GET  /api/v1/feed                       today's feed, priority-ordered, default 7
GET  /api/v1/feed/excluded              why a posting is NOT in the feed, with the specific check
GET  /api/v1/feed/:id/explanation       the factor breakdown for one posting

GET  /api/v1/profile                    profile + skills
GET  /api/v1/engagement/saved           saved postings, keyset-paginated on saved_at
GET  /api/v1/engagement/dismiss-reasons the closed set of reasons, with labels
GET  /api/v1/me                         identity
```

### Writing

```
POST   /api/v1/auth/register  /login  /refresh  /logout
PUT    /api/v1/profile
POST   /api/v1/profile/resume           multipart upload; parsed server-side

POST   /api/v1/engagement/opened/:id
POST   /api/v1/engagement/saved/:id
DELETE /api/v1/engagement/saved/:id
POST   /api/v1/engagement/applied/:id
POST   /api/v1/engagement/dismissed/:id  body: {"reason": "wrong_level"}

DELETE /api/v1/me                        erasure, inventoried
```

### The feed response shape

```jsonc
{
  "items": [{
    "opportunity_id": "…",
    "title": "Senior Backend Engineer",
    "fit": {
      "band": "Worth a look",
      "points": 30, "max_points": 45,
      "summary": "30 of a possible 45 points (some factors could not be scored)",
      "factors": [
        { "factor": "seniority", "points": 15, "max_points": 15, "scored": true },
        { "factor": "domain", "points": 10, "max_points": 10, "scored": true,
          "reason": "backend matches what you are targeting" },
        { "factor": "required_skills", "points": 0, "max_points": 0, "scored": false,
          "reason": "this posting's required skills have not been extracted yet" }
      ],
      "versions": { "weights": "w2", "embedding": "v1", "profile": 3 }
    },
    "state": { "saved": false, "applied": false, "applied_at": null, "dismissed": false },
    "channels": ["vector", "keyword"]
  }],
  "diagnostics": {
    "eligible_after_predicates": 175, "retrieved": 175,
    "passed_eligibility_gate": 173, "excluded_by_gate": 2,
    "retrieval_truncated": false
  }
}
```

`diagnostics` exists because a thin feed and a broken pipeline look identical without it. Surface it
somewhere — even a quiet line under an empty feed saying "175 roles matched your filters, 2 were
excluded" turns a dead end into an explanation.

### Things the API does not have yet

Be aware of these before designing around them:

- **No `/skills` endpoints.** `/skills`, `/skills/trends`, `/skills/gaps` are in the blueprint's API
  surface but belong to the intelligence surfaces (step 26). The "what should I do to become more
  competitive" question — the product's stated moat — has no backend yet.
- **No admin API.** `/internal/admin/*` is step 19.
- **No digest.** Step 18, and blocked on the email decisions.
- **No search or faceted filtering.** `/opportunities` is a keyset list, not a search endpoint.
  Faceted search is the one endpoint the blueprint allows a query builder for, and it is unbuilt.
- **No pagination on the feed.** It returns up to `limit` (default 7, max 50). A feed is not a
  search result and the blueprint treats them as different surfaces.

---

## Screens worth building, in the order that de-risks most

Each one exercises something already built, so none of this is speculative.

**1. Today's feed.** The product. Seven cards, each with band, the factor breakdown, liveness state,
and save / apply / dismiss. This is the screen the whole backend exists to serve, and building it
first will expose whatever the API got wrong.

**2. The explanation panel.** Expanding a card shows the arithmetic: every factor, what it earned,
what it could have earned, and plainly stated reasons for the ones that could not be scored. This is
the "why" in the product's four questions, and it is the screen that either earns trust or does not.

**3. Profile and preferences.** The right-hand side of every match. Worth building early for a
reason beyond completeness: **the hard predicates are unforgiving**, and a user who sets a country
filter without understanding it will see an empty feed. The UI should show what each preference will
exclude, before it excludes it.

**4. "Why am I not seeing X".** `/feed/excluded` with the specific failed check per posting. Rare as
a destination, disproportionate in trust. It is also the fastest way to find out that a predicate is
wrong.

**5. Saved and applied.** The lightest screen, and it closes the loop the engagement log opens.

Deliberately **not** first: a corpus browse/search screen. It is the easiest thing to build and it
turns the product into a job board with extra steps. The feed is the differentiator.

---

## Framework choice — settled

**React 19 + TypeScript + Vite, TanStack Query for server state, React Router, Tailwind v4.
Built, in [`web/`](../web/).**

This corrects an earlier version of this document, which recommended SvelteKit or Next.js with
server-side rendering. That recommendation was written without checking the repo: the
`frontend-conventions` skill in `.claude/skills/` already specified React + TypeScript + Vite +
TanStack Query, on the grounds that it matches the `tenders.scraping` admin dashboard so
conventions transfer. A checked-in convention beats a fresh opinion, and the skill also says
explicitly: *"If you want Nuxt/Vue instead, raise it before building out; do not mix."*

Two things from the old recommendation were right and are kept:

- **No component library for the feed card.** The card is the product, its honesty rules are
  unusual, and a design-system card will fight every one of them — starting with "put the score in
  a circular progress ring", which is exactly the bare percentage blueprint §3 forbids.
  `FitLedger` renders `+15 of 15` rows instead, and there is no ring anywhere in `web/`.
- **TypeScript at the boundary.** The DTOs in `web/src/lib/api/types.ts` mirror the Go response
  types by hand.

### Two places this consciously departs from the old plan

**Hand-written DTOs, not generated.** The old text wanted types generated from the API "so a field
the backend removes becomes a compile error". The goal is right; generation is the wrong instrument
here, because a generated client flattens exactly the parts the display rules depend on —
`salary: null` has to stay distinguishable from absent, and `scored: false` on a factor has to stay
distinguishable from `points: 0`. What replaced it is `internal/apicontract`: a reflection test
listing every json path the console reads, which fails if one is renamed or removed. It needs no
database and runs on every `make test`. Verified by renaming `verified_open` and watching it fail.

**A client-rendered SPA, which the old text warned against.** The warning was that it "puts the
slowest call after the slowest paint and shows an empty state to every first-time visitor". Worth
answering rather than ignoring:

- The feed is not slow at present. Measured p95 is 75 ms warm and 129 ms cold (`make loadtest`,
  288 postings). SSR would be hiding a fifth of a second.
- Nothing renders an empty state while loading. Every list has four designed states, and the
  loading one is a skeleton that holds the card's layout, so there is no shift when data lands.
- The first audience is the operations console, which is authenticated and internal. The
  latency-to-first-paint argument is a public-marketing-page argument.

The warning becomes correct again in two specific circumstances, and it is worth writing down now
rather than rediscovering it: when the feed is public and unauthenticated, and when retrieval cost
grows with a realistic corpus. `SLO.md` already records that feed cost tracks the size of the
eligible set rather than the page size, so this is a *when*, not an *if*. The migration path is
narrow on purpose — the app is route-split and all server state goes through TanStack Query, so
adding a server-rendered shell later does not mean a rewrite.

### What is built

| Route | What it is |
|---|---|
| `/` | Overview: SLO objectives ordered by attention, pipeline state, liveness recency |
| `/feed` | The feed with the fit ledger, save/apply, and dismiss-with-reason |
| `/sources` | Source table with yield, quarantine and purge, URL-backed sort and page |
| `/flags` | The flag queue with uphold/reject |

The design system, the theme handling and the honesty rules are documented in
[`web/README.md`](../web/README.md).

---

## Remaining backend work

Steps 2–21 of blueprint §35 are done, with step 18's transport still open. What is left, and what each actually depends on:

| # | Step | Status | Blocked on |
|---|------|--------|-----------|
| 18 | Daily digest, budget, quiet hours | **done, except the transport** | a sending domain, for real delivery ([OPEN-DECISIONS](OPEN-DECISIONS.md)) |
| 19 | Admin console, quarantine, merge tools, purge drill | **done** | — |
| 20 | SLOs, dashboards, error-budget alerts | **done** — see [SLO.md](SLO.md) | — |
| 21 | Load test against the SLOs | **done** — `make loadtest` | — |
| 22 | Calibration + percentile display | not started | **outcome data**, which step 17 has just started collecting |
| 23 | Postgres queue → NATS JetStream | earned migration | a §36 trigger, measured |
| 24 | Postgres FTS → OpenSearch | earned migration | a §36 trigger, measured |
| 25 | Compose → Kubernetes → AWS | earned migration | a §36 trigger, measured |
| 26 | Market intelligence surfaces | not started | a demand-series writer, which **does not exist** — see below |

Also outstanding, and none of it is a numbered step:

- **Extraction has never run against a live model.** No `ANTHROPIC_API_KEY` is set. The cache,
  validation and degrade paths are proven against a fake provider, but 45% of the fit model
  (required + preferred skills) is unavailable on every real posting until it runs. This is the
  single highest-leverage thing outstanding, and it is one paste into `.env`.
- **The corpus is three boards, 286 postings.** The target is 300–500 boards. The code takes a
  list; choosing the companies is manual by design.
- **The eval labels are rubric-derived.** Step 17 has just started collecting real ones. Until there
  are enough, NDCG@10 0.877 measures agreement with our own rubric.
- **Company entity resolution is deterministic-only.** Alias and fuzzy matching are unbuilt, and are
  never auto-merged.
- **Backup erasure** is a stated recommendation, not a decision.
- **The skill demand time-series has no writer.** `skill_demand_daily` was created by
  migration 000004 and has never been written to; `skill`, `skill_alias` and `skill_edge` are
  empty too. An earlier version of this table said the series had been "collecting since step 8",
  which was wrong twice: step 8 was normalization and dedup, and nothing writes the table at all.
  Step 26 is therefore blocked on two things, not one — a writer, and the extracted skills that
  would populate it.

## What a frontend will immediately expose

Worth knowing before you start, because these will look like frontend bugs and are not:

1. **Almost every card will read "Not enough information"** until extraction runs. The band is
   correct — 45% of the model genuinely cannot be evaluated. Do not design it away.
2. **A country or work-mode preference can empty the feed.** The predicates are strict by design and
   the gate explains each exclusion; the UI has to surface that rather than showing a blank page.
3. **The corpus skews to one employer.** 203 of 286 postings are GitLab, all remote. A feed will
   look repetitive, and that is the corpus rather than the ranking.
