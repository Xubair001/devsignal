---
name: backend-api-conventions
description: How to build a backend feature in DevSignal — package layering, HTTP handlers, DTO boundaries, validation, mapping errors to status codes, transactions, pagination, and what to test at which layer. Use when adding or changing an API endpoint, a service, a store/repository method, request or response types, or when deciding where a piece of logic belongs. Complements go-production-patterns, which covers runtime hardening rather than code structure.
---

# Backend feature conventions

`go-production-patterns` covers how the process survives production. This covers where code goes and
what shape it takes.

## Layering — three layers, one direction

```
transport (internal/<domain>/http.go)   handler: decode, validate, call service, encode
    |                                   no SQL, no business rules
    v
service   (internal/<domain>/service.go) all business logic, no HTTP types
    |                                    takes/returns domain types
    v
store     (internal/store/)              sqlc-generated queries + thin wrappers
                                         no business rules
```

Dependencies point downward only. A store must never import a transport package; a service must
never see `http.Request` or a DTO. Test this by asking: could this service be called from a worker
with no HTTP involved? If not, business logic has leaked upward.

**Handlers stay thin.** Decode → validate → call one service method → encode. If a handler has a
branch on business state, that branch belongs in the service.

## DTOs live at the boundary, and only there

```go
// internal/opportunity/http.go
type opportunityResponse struct {
    ID        string          `json:"id"`
    Title     string          `json:"title"`
    Fit       *fitResponse    `json:"fit,omitempty"`
    Liveness  livenessResponse `json:"liveness"`
}
```

Never return a store struct or a domain struct directly from a handler. Two reasons that matter here:
a store struct leaks column changes into the public API, and — more importantly — it leaks fields
that must never reach a client. `ghost_risk_score`, `simhash`, `raw_object_key` and every internal
version field are examples. An explicit response type makes exposure a decision rather than an
accident.

DTO conversion goes in the transport file, not the service.

## Validation

Validate at the edge, then trust the type. Parse into a typed request struct, validate it, and
convert to domain types before calling the service — the service should not be re-checking that a
UUID is a UUID.

```go
type listRequest struct {
    Limit  int
    Cursor string
    Remote *bool
}

func decodeList(r *http.Request) (listRequest, error) { ... }   // 400 on failure
```

Reject unknown query parameters rather than ignoring them. Silently ignoring `?remote=true` when the
parameter is actually `work_mode` is a bug users report as "the filter doesn't work".

## Errors → status codes, in one place

Domain errors are sentinels; the transport layer owns the mapping. Never construct an HTTP status
inside a service.

```go
// domain
var (
    ErrNotFound     = errors.New("not found")
    ErrConflict     = errors.New("conflict")
    ErrInvalidInput = errors.New("invalid input")
    ErrForbidden    = errors.New("forbidden")
)

// transport — the ONLY place status codes are chosen
func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
    switch {
    case errors.Is(err, ErrNotFound):     status = 404
    case errors.Is(err, ErrForbidden):    status = 403   // never 404-as-403 by accident
    case errors.Is(err, ErrInvalidInput): status = 400
    case errors.Is(err, ErrConflict):     status = 409   // optimistic concurrency lost
    default:                              status = 500   // log the detail, return a generic body
    }
}
```

A 500 body never contains the error string — it can carry a DB structure, a query, or a value from
another tenant. Log the detail with the request ID; return the request ID to the client so support
can correlate.

## Authorization is not optional per-endpoint work

Every read and write is scoped to the authenticated user and tenant, enforced in one middleware plus
a scoped query — not by remembering to add `WHERE user_id = $1` in each handler. A missing scope
clause is a cross-tenant data leak, which is the most expensive class of bug this system can have.

When you add a table holding user data, the scoping is part of the same change (see the
`privacy-surface` skill).

## Transactions belong to the service

```go
func (s *Service) Save(ctx context.Context, userID, oppID uuid.UUID) error {
    return s.db.InTx(ctx, func(q *store.Queries) error {
        if err := q.InsertSaved(ctx, ...); err != nil { return err }
        return q.InsertEngagementEvent(ctx, ...)   // both, or neither
    })
}
```

The store exposes queries; the service decides the transaction boundary. A handler must never open a
transaction — it has no way to know what else belongs inside it.

## Pagination is always keyset

```
GET /api/v1/opportunities?limit=50&cursor=<opaque>
```

Never `OFFSET`. It degrades linearly as the corpus grows and it skips or duplicates rows when data
shifts between pages — which it constantly does here, because ingestion never stops. Encode
`(sort_key, id)` into an opaque cursor and always include `id` as the tiebreaker.

## Time and money cross the boundary explicitly

- Timestamps serialize as RFC 3339 UTC strings. Never a Unix int, never a local time.
- Money serializes as its parts, never a formatted string and never a float:

```json
"salary": { "min_minor": 12000000, "max_minor": 15000000,
            "currency": "USD", "period": "year", "is_estimated": false }
```

Formatting is the client's job — it depends on the viewer's locale, which the API does not know.
`"salary": null` and `"salary": {...}` are meaningfully different and both must be handled.

## What to test at which layer

| Layer | Test | Do not test |
|-------|------|-------------|
| store | Integration against real Postgres via Compose. Constraint violations, concurrency | Business rules |
| service | Table-driven unit tests with a fake store for logic; integration for transactions | HTTP codes |
| transport | `httptest` for decode/validate/status mapping | Business logic |

Use real Postgres for store tests, never sqlmock. Every interesting bug in this layer is a constraint,
an index, a `SKIP LOCKED` interaction or a serialization failure — none of which a mock reproduces.

## Done means

- [ ] Handler is thin: decode, validate, one service call, encode
- [ ] Explicit response DTO; no store or domain struct returned directly
- [ ] No internal field (`ghost_risk_score`, `simhash`, `*_version`, `raw_object_key`) exposed
- [ ] Unknown query parameters rejected, not ignored
- [ ] Errors are sentinels; status mapping only in transport; no error strings in 5xx bodies
- [ ] Query scoped to user and tenant
- [ ] Transaction boundary in the service, not the handler
- [ ] Keyset pagination with an `id` tiebreaker
- [ ] Money as parts; timestamps RFC 3339 UTC
- [ ] Store tested against real Postgres
