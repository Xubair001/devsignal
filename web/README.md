# DevSignal console

React 19 + TypeScript + Vite, TanStack Query for server state, React Router, Tailwind v4.
Conventions come from `.claude/skills/frontend-conventions`; the display rules come from
blueprint §3 and are not stylistic.

```bash
npm install
npm run dev        # :5173, proxies /api and /internal to :8080
npm run build      # tsc -b && vite build
npx tsc -b --noEmit
```

Both ports are off the defaults, for the same reason the Postgres and Redis ports are: this
machine already runs other projects on 5173 and 8080. Override with `DEVSIGNAL_WEB_PORT` and
`DEVSIGNAL_API_URL`.

### Authentication

The API accepts `Authorization: Bearer <session token>` and nothing else. The console sends the
token from `localStorage['ds-token']`, falling back to `VITE_DEV_TOKEN` when storage is empty.

A header rather than a cookie, deliberately. Matching the client to the API by adding cookie auth
would bring a CSRF surface with it: browsers attach cookies to cross-site requests automatically,
and this console issues state-changing POSTs — save, apply, dismiss, quarantine, purge. A token
read from storage is never sent by anyone but us, so there is nothing to forge and no CSRF token
to get wrong.

`VITE_DEV_TOKEN` goes in `web/.env.local` (gitignored) and must never be set in a deployed build:
Vite inlines env values into the bundle. Mint one with a session row whose `token_hash` is the
raw SHA-256 of the token — `bytea`, so `decode(…, 'hex')`, not the hex string itself.

## The five rules this UI exists to keep

Trust is lost by one invented field, not by a missing one. Everything below is a consequence.

**1. No percentage and no progress ring for fit.** `FitLedger` renders the arithmetic —
`+15 of 15`, `+4.1 of 20` — and the band arrives from the server as a string. There is deliberately
no ring or gauge on a card: a ring *is* a percentage, and a percentage implies a probability we have
not calibrated.

Percentages do appear on the operational screens — parse yield, error-budget burn rate, liveness
check recency — and that is not an exception to the rule. Those measure our own system against a
target we set, and they are shown to operators. The forbidden thing is a number that implies
something about a *user's* chances, which is a claim we have no data to make. If calibration ever
produces one, it will be a percentile and it will come from the API.

**2. An unscored factor is not a zero.** `scored: false` means we could not read it; `points: 0`
means you match none of it. Those are opposite statements and the ledger renders them differently —
unscored rows are recessed, italicised, and carry the server's reason ("this posting's required
skills have not been extracted yet"). Collapsing the two is the single most misleading thing this
screen could do, which is why the band `Not enough information` gets a neutral colour rather than a
warning one: it is a statement about our data, not about the user.

**3. `salary: null` is a state, not a falsy value.** It renders "Salary not disclosed". An
estimated range renders with "Our estimate, not the employer's" attached. `salary || 'Competitive'`
is the exact bug the rule exists to prevent.

**4. Liveness is always shown.** The daily feed may not contain a posting whose open state is
unknown, so `posting` is non-optional in the API response and the server drops an item it cannot
describe. The card shows the beacon, the check recency, and days-open measured from *our* first
sighting — never the employer's claimed post date, which boards refresh.

**5. Nothing is computed here.** No score, no band, no eligibility. Two clients that derive a
number will disagree with each other and with the backend's decision log.

**6. A skill the ontology cannot place is shown, not dropped.** The profile deliberately cannot
mint new skills — a typo would become a vocabulary entry that then matches no posting — so
`unresolved_skills` comes back from the server and those names render struck through with an
explanation. Silently dropping one would leave the user believing a skill counts when it counts
toward nothing.

**7. The posting body is rendered as HTML, and that is only safe because the server filters it.**
`description_html` is third-party content from a board anyone can post to. It is sanitized
server-side through an allow-list before it is served (see hard rule 29); rendering it here without
that would be stored XSS.

Meaning is never carried by colour alone: every `Pill` tone ships a glyph, and the fit ledger has an
`sr-only` table fallback because the breakdown is the product's core claim.

## Routes

Public at the root, the console under `/app`. Separated rather than switching on the session at
`/`, because a deep link has to mean one thing: `/app/browse/:id` is always the console and `/` is
always the public page, whoever is looking.

| Route | Who | What it is |
|---|---|---|
| `/` | public | Landing page |
| `/login`, `/register` | public | Two routes over one form — a browser password manager keys on the URL |
| `/app/feed` | member | Today's feed with the fit ledger, save/apply, dismiss-with-reason |
| `/app/saved` | member | Saved roles, each with its current liveness state |
| `/app/browse`, `/app/browse/:id` | member | The corpus, filtered and keyset-paginated, plus posting detail |
| `/app/profile` | member | Preferences, skills, resume upload, account erasure |
| `/app/settings` | member | Digest consent, quiet hours, caps, minimum band, send history |
| `/app/overview` | **operator** | SLOs, pipeline state, liveness recency |
| `/app/sources` | **operator** | Source table with yield, quarantine and purge |
| `/app/merges` | **operator** | Merge candidates dedup withheld for a human |
| `/app/flags` | **operator** | The listing-flag queue |

`/app` redirects to the feed for every role: the overview reads the admin-gated SLO report, so
making it the landing screen would greet a member with a surface they cannot load.

## Access control

`RequireAuth` redirects to `/login` and carries the attempted path, so signing in returns you where
you were going. `RequireAdmin` gates the operator routes.

**Neither is a security boundary.** The server gates `/internal/admin` independently and answers
404 to a non-admin. These exist so a member never sees a link to a page that would only show them
an error, and so a bookmarked operator URL degrades into a clear message rather than four failed
queries. The client-side refusal deliberately uses the same "does not exist" language the API does:
telling someone a page exists but is forbidden is information they did not have.

`navFor(isAdmin)` filters the sidebar, the mobile drawer and the ⌘K palette from **one** source.
Three lists that each decide what exists is how a hidden destination stays reachable from one of
them.

## Structure

```
src/
  components/ui/   product-unaware primitives. A file lives here only if it has
                   no idea what an opportunity is.
  features/
    shell/         app frame, ⌘K palette, theme
    feed/          the feed, the fit ledger, dismiss-with-reason
    overview/      SLO objectives, pipeline state
    sources/       source table, quarantine, purge
    flags/         the flag queue
  lib/
    api/           typed client + DTOs mirroring the Go types
    queryKeys.ts   every cache key, in one place
```

Server state lives in TanStack Query and nowhere else — never mirrored into `useState`, never into
a store. URL state (sort, page) lives in the URL so a refresh and a shared link both work.
Ephemeral UI state (open menu) is local.

### DTOs are hand-written, and there is a test for that

`lib/api/types.ts` mirrors the Go response types by hand rather than being generated, because a
generated client flattens the distinctions rules 2 and 3 depend on. The cost is that a renamed json
tag is invisible to both compilers — Go builds, `tsc` passes, the field arrives `undefined`.

`internal/apicontract` covers that: a reflection test listing every json path this console reads,
which fails if one is renamed or removed. It needs no database and runs on every `make test`. If
you add a field to a card, add its path there too.

## Typography and spacing

Nine named type steps, in `index.css`, each with its own line height. The values were set **from**
an audit rather than chosen first: the interface had drifted to 21 distinct hardcoded `text-[Npx]`
values, and these are where they actually clustered. Nothing may use an arbitrary pixel size again —
`grep -rhoE "text-\[[0-9.]+px\]" src/` should return nothing.

`Card` takes an enumerated `pad` (`none` / `tight` / `normal`) rather than a padding className. The
same audit found seven paddings in use for the same job, which is what makes a set of cards read as
unrelated rectangles instead of one system. Padding tightens on small screens in `Card` alone, so no
page has to remember to.

Wide content scrolls inside its own container — never by making the page body pan sideways, which is
the most common responsive failure and is always a table.

## Theme

Three states, not two. An explicit choice stamps `data-theme` on `<html>`; the default "system"
setting stamps nothing and only `prefers-color-scheme` separates light from dark. So tokens are
defined three times in `index.css`: light on bare `:root`, dark under
`@media (prefers-color-scheme: dark) { :root:not([data-theme='light']) }`, and dark again under
`:root[data-theme='dark']`. A colour defined only inside a media block never applies in the
unstamped state, which renders one theme's text on the other theme's background.

`index.html` carries a small pre-paint script that applies the stored choice before first paint, so
there is no flash of the wrong theme.

## Accessibility floor

Semantic elements first — a clickable `<div>` is a bug. Every control is keyboard reachable with a
visible focus ring. The fit breakdown is real text, not only a chart. Contrast ≥ 4.5:1 for body
text. `prefers-reduced-motion` is respected.
