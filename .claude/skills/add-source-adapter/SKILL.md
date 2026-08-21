---
name: add-source-adapter
description: Add or modify a DevSignal opportunity source (ATS board, official API, feed, sitemap, or permitted crawl). Use whenever the task mentions adding a source, a company board, a job feed, an ATS integration (Greenhouse, Lever, Ashby, Workable, SmartRecruiters), a crawler, a fetcher, or a parser for postings — and when debugging a source that stopped yielding complete records. Covers the legal tier gate, the required HTTP shape, provenance, identity keys, golden fixtures and the registry row.
---

# Adding a source adapter

A source is the only place untrusted input enters the system, and the only place with legal exposure.
Every step below exists because skipping it caused a specific, named failure.

## Step 0 — the tier gate (do this before writing code)

Classify the source. If you cannot place it in Tier A or B with a recorded reason, **stop and raise
it** — do not write the adapter.

| Tier | Qualifies | Action |
|------|-----------|--------|
| A | Public ATS job-board API, official API, RSS/Atom, sitemap, partner feed, employer-supplied | Proceed |
| B | Public page, robots-allowed, no login wall, no click-through terms, polite rate | Proceed after recording `legal_basis`, `robots_checked_at`, `terms_reviewed_at`, `reviewed_by` |
| C | Behind a login, terms forbid automated access, requires an account, actively enforced | **Do not ingest.** Not a risk-appetite call |

**The absolute rule:** never create an account, never authenticate, and never accept terms on a
source we ingest. Scraping public data survived the CFAA challenge in *hiQ v. LinkedIn*; hiQ still
lost on breach of contract, aggravated by having created profiles on the platform. Contract is the
exposure, not computer-crime law.

Prefer Tier A hard. It is not merely safer — it collapses the parsing surface to JSON, gives a
stable global identity (`ats_type`, `ats_job_id`) that makes dedup nearly free, sometimes supplies
compensation already structured, and makes conditional GETs cheap enough for the 5-minute freshness
SLO to be real.

## Step 1 — implement the interface, nothing more

```go
type SourceAdapter interface {
    ID()   SourceID
    Fetch(ctx context.Context, since Cursor) ([]RawDocument, Cursor, error)
    Parse(ctx context.Context, doc RawDocument) (ParsedPosting, error)
}
```

`Parse` must be **pure** — no network, no DB, no clock. Re-parsing a stored `RawDocument` has to be
deterministic, because that is what makes the golden fixtures in step 5 meaningful and what lets you
re-run a whole source after fixing a parser bug.

Everything shared lives in the framework, not in your adapter: retry, backoff, raw persistence,
politeness, conditional GET, body caps, provenance writes, dedup, liveness bookkeeping. If you find
yourself writing any of those, you are in the wrong file.

## Step 2 — the HTTP shape (non-negotiable)

Never `http.DefaultClient`. It has no timeout, so one hung source holds a goroutine and a connection
until you exhaust both.

```go
tr := &http.Transport{
    MaxConnsPerHost:       4,          // politeness, per source host
    MaxIdleConnsPerHost:   4,
    IdleConnTimeout:       90 * time.Second,
    TLSHandshakeTimeout:   10 * time.Second,
    ResponseHeaderTimeout: 15 * time.Second,
}
client := &http.Client{Transport: tr, Timeout: 45 * time.Second}

// hard cap on any body from a host we do not control
const maxBody = 5 << 20
body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
if len(body) > maxBody {
    return ErrBodyTooLarge      // quarantine the source; do not retry blindly
}
```

Required behaviours:

- **Conditional GET.** Send `If-None-Match` / `If-Modified-Since`, store the returned `ETag`. A 304
  is a successful poll that observed nothing changed — it still counts for liveness.
- **Per-host token bucket**, not a global one. Politeness is per host.
- **Bounded fan-out.** `errgroup.WithContext` + `SetLimit`. Never `go fetch(u)` in a loop over a
  sitemap; a large sitemap becomes a self-inflicted DoS.
- **Identified user agent** with a contact URL for Tier B.

## Step 3 — identity and provenance

Write one `opportunity_source` row per place the posting was seen. Never write the canonical row
directly from an adapter.

```
opportunity_source
  source_id, source_job_id          UNIQUE (source_id, source_job_id)
  ats_type, ats_job_id              set these for Tier A — they make dedup free
  apply_url, raw_object_key
  content_hash, simhash
  first_seen_at, last_seen_at
```

- `first_seen_at` is **ours** and is the only trustworthy age signal.
- `source_reported_posted_at` is **theirs** — store it, display it, never score on it. Boards and
  employers refresh it so listings look fresh.
- Raw payload goes to object storage; the row holds `raw_object_key`. You must be able to re-parse
  history without re-fetching.

## Step 4 — liveness bookkeeping

Your adapter reports what it observed. It does **not** decide closure.

```
successful poll, posting present  -> last_seen_at = now(), consecutive_misses = 0
successful poll, posting absent   -> consecutive_misses += 1
failed poll (any error, timeout, quarantine) -> touch nothing
```

Closure requires a *successful* poll in which the posting was absent. Inferring closure from a failed
fetch lets one source outage delete the corpus.

## Step 5 — golden fixtures (the highest-value test in the repo)

Commit a real recorded payload under `testdata/<source>/` and assert the **complete** normalized
output, field by field.

```
testdata/greenhouse/stripe-2026-08-21.json
testdata/greenhouse/stripe-2026-08-21.golden.json
```

Source payloads drift constantly, and the failure is almost never a clean error — it is a parser
that still returns a row with an empty skills list or a missing salary. Success counters stay green
while match quality degrades for weeks. The fixture is what catches it.

Refresh fixtures deliberately. **Treat a golden diff as a review item, never auto-rebaseline it.**

## Step 6 — the registry row

```
source
  id, name, tier, type
  legal_basis, robots_checked_at, terms_reviewed_at, reviewed_by
  rate_limit, poll_interval, etag_supported
  status                -- active | quarantined | retired
  last_success_at, last_failure_at
  items_discovered, items_processed, parse_yield_7d
```

Set `poll_interval` from the freshness SLO for the tier (Tier A: 5 min; Tier B: what politeness
allows), not from what feels safe.

## Step 7 — verify

```bash
make test-golden                      # your new fixture must pass
go test ./internal/source/<name>/...
make lint
```

Then check the source health view: `parse_yield` should be ≥ 98% and the field fill rates for title,
company, location and skills should look sane. A source that ingests rows but fills few fields is
broken, not working.

## Done means

- [ ] Tier recorded with a reason; nothing in Tier C
- [ ] No account, no auth, no accepted terms
- [ ] `Parse` is pure — no network, DB or clock
- [ ] Explicit HTTP client with layered timeouts and `io.LimitReader`
- [ ] Conditional GET with stored ETag
- [ ] Per-host rate limit, bounded fan-out
- [ ] `ats_type` / `ats_job_id` populated for Tier A
- [ ] Raw payload persisted; `first_seen_at` set by us
- [ ] Liveness reported, closure not decided in the adapter
- [ ] Golden fixture committed and asserting full output
- [ ] Registry row complete, `poll_interval` derived from the SLO
- [ ] Source-level purge still works for this source
