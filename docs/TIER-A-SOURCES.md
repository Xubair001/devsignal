# Tier-A source registry

Closes the blueprint §33.3 open item "final Tier-A source list with per-source review".

The reviewable unit for Tier A is the **ATS platform**, not the company board. Every Greenhouse
board is the same documented public endpoint pattern, so reviewing one company's board tells you
nothing the platform review did not. That is why `--role=add-sources --file=boards.txt` takes a
list: adding a company is registration, not review.

Every row below was **probed live** (dates in the last column). Documentation for these endpoints
goes stale faster than the endpoints do, so the evidence is a real response, not a doc page.

## Built

| Platform | Endpoint pattern | Auth | Descriptions in bulk? | Verified |
|---|---|---|---|---|
| Greenhouse | `boards-api.greenhouse.io/v1/boards/{token}/jobs?content=true` | none | yes | `gitlab`, 203 postings, 143 KB |
| Lever | `api.lever.co/v0/postings/{site}?mode=json` | none | yes | `unlimit`, 51 postings |
| Ashby | `api.ashbyhq.com/posting-api/job-board/{name}?includeCompensation=true` | none | yes | `linear` 32, `ramp` 136, `ashby` 63 |

All three share the property that makes Tier A load-bearing rather than merely safe: one request
returns the whole board with descriptions inline, `(ats_type, ats_job_id)` is a stable global
identifier so dedup is nearly free, and conditional GETs are cheap enough to make a frequent poll
real. A second Ashby poll returned `304 not modified` against a stored ETag.

### Per-platform notes that cost something to learn

**Greenhouse** double-escapes the description; it is unescaped once in the adapter so the stored
HTML is real HTML. Work mode has to be read out of the location string, because Greenhouse does not
state it.

**Lever** returns a top-level JSON **array**, unlike every other family here. `createdAt` is epoch
**milliseconds**. It states `workplaceType` outright, so work mode is read rather than guessed —
the one place it is better than Greenhouse. It returns **no company name** anywhere in the payload.

**Ashby** returns the largest bodies of any family measured: `linear` is 1.2 MB, `ramp` 2.3 MB, and
**`openai` is 12.4 MB**. The original 8 MiB body cap rejected that board outright, which is why
`source.MaxBodyBytes` is now 32 MiB and per-client overridable. A cap that silently excludes the
largest employers is worse than a generous one, because the failure looks like a small market
rather than a configuration error. Ashby also exposes `isListed`; false means the employer withdrew
the posting from their own public board, and the adapter skips those rather than republishing
something taken down.

**Neither Lever nor Ashby returns a company name.** Both adapters leave `CompanyName` empty on
purpose. Company resolution falls back to the board token, and deriving a display name from a slug
would be a guess rendered as fact (hard rule 3).

## Reviewed, verified reachable, not built yet

These are Tier A on the same grounds — public, documented, unauthenticated — but each needs a fetch
strategy the current adapters do not have. They are listed so the inventory shows they were
considered rather than missed.

| Platform | Endpoint | Verified | Why not built yet |
|---|---|---|---|
| SmartRecruiters | `api.smartrecruiters.com/v1/companies/{id}/postings` | `BoschGroup`, `totalFound=4776` | **List carries no description.** Needs a per-job detail fetch via each posting's `ref`, plus offset pagination at 100/page. 4,776 postings is 48 list requests and 4,776 detail requests — an N+1 fetch strategy with its own incremental cursor, not a variation on the bulk-JSON adapter. |
| Breezy | `{company}.breezy.hr/json` | reachable, 200 JSON | Small boards; worth adding once the N+1 or per-page strategy above exists. |
| Personio | `{company}.jobs.personio.de/xml` | 200 XML | XML feed rather than JSON; adapter shape differs. Slug discovery unresolved. |
| Workable / Recruitee / Workday / BambooHR | various | endpoint patterns not confirmed with a live slug | Probed; the patterns commonly cited did not return a board for the slugs tried. Not ruled out — unverified, and an unverified source is not a source. |

**Workday** deserves a specific note: its public endpoint is a `POST` with a JSON body per tenant
and site (`{tenant}.wd{n}.myworkdayjobs.com/wday/cxs/...`), and it is the single largest ATS by
enterprise seat count. It is worth building, and it is a different adapter shape again.

## What is explicitly out

Tier C, per blueprint §12 and hard rule 5 — **never** create an account, authenticate, or accept
terms on a source we ingest. That rule is the whole difference between our posture and hiQ's, so
these are not "later", they are never:

- LinkedIn, Indeed, Glassdoor, ZipRecruiter: login walls, click-through terms forbidding automated
  access, and active enforcement.
- Any endpoint requiring an API key issued to us under terms of service.
- Any aggregator whose own terms forbid redistribution.

Paid aggregator APIs (there are several selling exactly this data) are also out for v1, and not on
cost grounds alone: a bought corpus cannot be verified live, and **verified liveness is the
product**. Buying the corpus would mean buying the ghost listings too.

## Adding a company board

```bash
make add-source name=greenhouse:gitlab      # one
make add-source name=lever:unlimit
make add-source name=ashby:linear

# or in bulk, with the reviewer recorded
./bin/devsignal --role=add-sources --file=boards.txt --reviewed-by=you
```

`boards.txt` is one `family:token` per line. The family must be a registered adapter; an unknown
family fails loudly rather than registering a source nothing can poll.

## Scaling to the blueprint's ~80% of corpus target

The corpus target is 300–500 company boards (blueprint §35 step 10). Discovery of *which* boards
is the remaining manual work and is deliberately not automated: each board is a company we are
asserting is real, and the cheap ways to enumerate them are exactly the Tier-C sources above.

The practical route, in order of cost:

1. **Company lists you already trust** — portfolio pages of large VCs, Y Combinator's company
   directory, and any public "who's hiring" list. Each company's careers page reveals its ATS in
   the URL (`boards.greenhouse.io/x`, `jobs.lever.co/x`, `jobs.ashbyhq.com/x`), which is the board
   token.
2. **Probe the pattern.** For a candidate slug, one unauthenticated GET says definitively whether a
   board exists. `add-sources` already skips slugs with no usable adapter.
3. **Let source health do the pruning.** `--role=source-health` reports each source against its own
   baseline, so a board that goes quiet or breaks shows up without anyone watching it.

There is no step here that requires paying anyone.
