# Prerequisites

Run `./scripts/check-prereqs.sh` — it is the authority. This file explains *why* each requirement
exists and how to install what's missing.

## Verified state (21 August 2026)

Checked on this machine: **16 passed, 2 warnings, 0 failed.**

| Tool | Required | Installed | Note |
|------|----------|-----------|------|
| Go | ≥ 1.25 | **1.26.5** | Container-aware `GOMAXPROCS` |
| Docker | any current | 29.1.3 | Daemon reachable |
| Docker Compose | v2 | 2.40.3 | |
| golangci-lint | any current | present | |
| staticcheck | any current | 2026.2 (0.8.0) | installed during setup |
| sqlc | any current | v1.31.1 | installed during setup |
| golang-migrate | any current | present | |
| psql client | any current | 18.4 | Manual inspection only |
| make, git, jq | — | present | |
| `pgvector/pgvector:pg17` | required | pulled (443 MB) | |
| `redis:8-alpine` | required | present | |
| `minio/minio:latest` | required | pulled (175 MB) | |

Two warnings, both expected:

- **`GOMEMLIMIT` unset** — correct locally. Set it to ~90% of the container memory limit when
  deploying. Without it a GC that runs slightly late becomes an OOMKill.
- **`air` not installed** — optional live-reload only.

## Why these specific versions

### Go ≥ 1.25 — not negotiable

Go 1.25 made `GOMAXPROCS` container-aware: the runtime reads the cgroup CPU bandwidth limit at
startup and rechecks periodically. Before that, a pod with a 2-CPU limit on a 64-core node would set
`GOMAXPROCS=64` and get aggressively throttled — the problem `uber-go/automaxprocs` existed to solve.

Two consequences for this repo:

- **Do not add `automaxprocs`.** It is superseded.
- **Do not set `GOMAXPROCS` in a deployment manifest.** It overrides the correct runtime value and
  reintroduces the throttling. This is the inverted form of the old bug and it is easy to inherit
  from a copied manifest.

### pgvector image, not plain postgres

The plain `postgres:*` images **do not bundle pgvector**, and v1 deliberately keeps rows, full-text
search (`tsvector`) and vector kNN in one database. `pgvector/pgvector:pg17` is Postgres plus the
extension.

This was a real gap on this machine — `postgres:15/16/17-alpine` were already present and would have
looked sufficient until the first `CREATE EXTENSION vector` failed.

### MinIO

S3-compatible local object storage for raw source documents, resumes and parsed artifacts. The
blueprint keeps large bodies (`description_html`) out of the hot table so the working set stays in
memory, which means object storage is needed from the first adapter, not later.

## Installing what's missing

```bash
# Go toolchain extras (install into $(go env GOPATH)/bin)
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
go install github.com/air-verse/air@latest          # optional, live reload

# Container images for the v1 dev stack
docker pull pgvector/pgvector:pg17
docker pull redis:8-alpine
docker pull minio/minio:latest
```

`$(go env GOPATH)/bin` is often absent from a non-login shell's `PATH`. Add it to your shell profile,
or `check-prereqs.sh` will report tools as missing that are actually installed:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

## Not yet required

These are deliberately absent until a blueprint §36 trigger fires. Installing them early is how the
architecture drifts back to the version the audit rejected:

| Tool | Trigger that earns it |
|------|----------------------|
| NATS / JetStream | A second independent consumer of the same event, or queue contention that pool tuning cannot fix |
| OpenSearch | Faceting cost, hybrid ranking tuning, or percolator-based alerting |
| Kubernetes / kind / helm | A replica count you cannot manage by hand |
| Kafka | Ordering or retention guarantees JetStream cannot give you |
| Terraform | First real cloud environment |
| atlas | Not used — migrations are golang-migrate |

## No local Postgres server needed

`pg_isready` reports no server on this machine, which is fine and expected. Development runs Postgres
through Compose so the version, extensions and seed data are reproducible. The `psql` **client** is
useful for manual inspection; the server is not.

Note the client here is 18.4 while the container is 17.11. That mismatch is harmless for
inspection — `psql` is backward compatible — but do not use client-side `pg_dump` 18 against the
17 server if you ever need a restorable dump.
