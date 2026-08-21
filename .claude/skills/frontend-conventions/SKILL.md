---
name: frontend-conventions
description: Frontend conventions for the DevSignal web app — React + TypeScript + Vite + TanStack Query, data fetching and cache keys, component and folder structure, forms, accessibility, and the product display rules that are binding on the UI (never render a raw match percentage, never invent a signal, always show verified-open state). Use when building or reviewing any UI, page, component, hook, or API client code.
---

# Frontend conventions

**Stack.** React + TypeScript + Vite, TanStack Query for server state, React Router. The blueprint
does not specify a frontend stack — this matches the React + TypeScript admin dashboard in
`tenders.scraping`, so conventions transfer. If you want Nuxt/Vue instead, raise it before building
out; do not mix.

## The display rules are binding, not stylistic

Blueprint §3 is a product contract, and the frontend is where it is kept or broken. Trust is lost by
one invented field, not by a missing one.

| Render this | Never render this |
|-------------|-------------------|
| Band: `Strong fit` / `Worth a look` / `Stretch` | A raw match percentage implying a probability |
| Per-factor contributions: `+29 of 35` | A competitiveness estimate — we have no applicant counts |
| `Verified open · checked 2 hours ago` | A posting whose liveness is unknown, in the daily feed |
| `Salary not disclosed`, or a clearly-labelled market range | An estimated salary styled like a disclosed one |
| Percentile, once the API returns one | Any value the API did not send |

Two concrete consequences for code:

1. **Never compute a score, band, or percentage in the client.** The API sends `fit.band` and
   `fit.factors[]`. If the UI derives a number, two clients will disagree and the explanation stops
   matching the backend's decision log.
2. **`salary: null` is a first-class state**, not a falsy value to `||` away. `{salary?.min_minor ||
   'Competitive'}` is exactly the invented field this rule exists to prevent.

```tsx
// wrong — invents a claim and computes a score client-side
<span>{Math.round(fit.score)}% match</span>
<span>{opp.salary ? fmt(opp.salary) : "Competitive salary"}</span>

// right — renders only what the API asserted
<FitBadge band={fit.band} />
{opp.salary
  ? <Salary value={opp.salary} estimated={opp.salary.is_estimated} />
  : <MutedText>Salary not disclosed</MutedText>}
```

## Folder structure — feature folders

```
src/
  features/
    feed/            components, hooks, types for one product surface
    opportunity/
    profile/
    admin/
  components/ui/     generic, product-unaware primitives
  lib/
    api/             typed client, one module per resource
    queryKeys.ts     every cache key, in one place
  routes/
```

A file only lives in `components/ui/` if it has no idea what an opportunity is. Anything that knows
about fit bands or liveness belongs in a feature folder.

## Server state is TanStack Query's job; nothing else is

- Server data: TanStack Query. Never mirrored into `useState`, never into a global store.
- URL state (filters, page, sort): the URL. It must survive a refresh and be shareable.
- Ephemeral UI state (open menu, focus): local `useState`.

There is no Redux/Zustand store for server data. Duplicating it is how stale rows get rendered next to
fresh ones.

### Cache keys are centralized and hierarchical

```ts
// lib/queryKeys.ts — the ONLY place keys are constructed
export const qk = {
  feed:        (params: FeedParams) => ['feed', params] as const,
  opportunity: (id: string)         => ['opportunity', id] as const,
  explanation: (id: string)         => ['opportunity', id, 'explanation'] as const,
  profile:     ()                   => ['profile'] as const,
} as const;
```

Inline array literals scattered through components make invalidation guesswork — you end up
invalidating `['feed']` and missing `['feed', {...}]`. Hierarchy matters: invalidating
`['opportunity', id]` should also drop that opportunity's explanation.

### Mutations invalidate; they do not hand-patch

```ts
const dismiss = useMutation({
  mutationFn: (v: { id: string; reason: DismissReason }) => api.dismiss(v),
  onSuccess: (_d, v) => {
    qc.invalidateQueries({ queryKey: qk.feed({}) });
    qc.invalidateQueries({ queryKey: qk.opportunity(v.id) });
  },
});
```

Optimistic updates are allowed for the dismiss/save buttons — the feed must feel instant — but the
rollback path must exist, and the reason code must reach the server. **A dismissal whose reason is
dropped is a lost training label**, and those are not recoverable later.

## The API client is typed and hand-written at the boundary

One module per resource under `lib/api/`. Types mirror the API's response DTOs — including the
awkward parts:

```ts
export type Money = {
  min_minor: number; max_minor: number | null;
  currency: string; period: 'year' | 'month' | 'week' | 'day' | 'hour';
  is_estimated: boolean;
};

export type Opportunity = {
  id: string;
  title: string;
  salary: Money | null;               // null is a real state, not "unknown yet"
  liveness: { verified_open: boolean; checked_at: string };
  fit?: { band: 'strong' | 'possible' | 'stretch'; factors: Factor[] };
};
```

Money arrives as minor units. **Format at the render edge, never store a formatted string**, and
never do arithmetic on money in the client beyond choosing a display range.

## Loading, empty and error states are designed, not defaults

Every list needs four: loading, empty, error, and populated. The empty state carries product meaning
here:

> "Nothing met your bar today. The market was quiet — 3 new postings matched your stack but none
> cleared your seniority filter."

That is a feature, not a failure. Do not pad the feed to hit a count, and do not render a spinner
where a skeleton belongs — a skeleton preserves layout and avoids the shift.

## Accessibility — the non-negotiable minimum

- Semantic elements first. A clickable `<div>` is a bug; use `<button>`.
- Every interactive control is keyboard reachable with a visible focus ring.
- The fit breakdown must be readable by a screen reader: it is the product's core claim, so the
  contributions belong in real text, not only in a chart. Charts get `aria-label` plus a table
  fallback.
- Never encode meaning in colour alone — band, liveness and gaps all need text or an icon too.
- Colour contrast ≥ 4.5:1 for body text.

## Performance, in the order that actually matters here

1. Do not over-fetch. The feed is ~7 items; there is no virtualization problem to solve.
2. Paginate with the API's opaque cursor. Never `OFFSET`, never fetch-all-then-slice.
3. Memoize only after measuring. `useMemo` around a 7-item map is noise.
4. Code-split by route, not by component.
5. Keep the explanation payload out of the list request — fetch it when the card expands.

## Forms

React Hook Form + Zod, with the Zod schema as the single source of truth for the shape. Validate on
blur, submit disabled while pending, and server-side field errors mapped back onto fields rather than
dumped in a banner. The profile form is long: persist a draft locally so a refresh does not lose
twenty minutes of typing.

## Never in the client

- Secrets, API keys, or model credentials.
- Any score, band or ranking computation.
- PII in `localStorage` beyond an in-progress form draft the user is actively editing.
- Business rules — eligibility and scoring are server decisions, and the client must not
  re-implement or "helpfully" pre-filter them.

## Done means

- [ ] No percentage, competitiveness figure, or client-computed score rendered
- [ ] `salary: null` handled as its own state, not `||`-defaulted
- [ ] Liveness shown; unverified postings not presented as live
- [ ] Server state in TanStack Query only; keys from `queryKeys.ts`
- [ ] Mutations invalidate the right hierarchy; dismiss reason reaches the server
- [ ] Loading, empty, error and populated states all present and intentional
- [ ] Keyboard reachable, focus visible, meaning not colour-only, contrast ≥ 4.5:1
- [ ] Money formatted at the render edge from minor units
- [ ] No secrets, no business rules, no PII in storage
