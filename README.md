# DevSignal

Developer Opportunity Intelligence — an explainable recommender over a corpus we keep *true*.

Not a job board and not an aggregator. It answers four questions in order: what should I apply to,
why, what am I missing, and what should I do to become more competitive.

- **Specification:** [docs/DevSignal-Product-and-Architecture-Blueprint.docx](docs/DevSignal-Product-and-Architecture-Blueprint.docx) — the source of truth
- **Contributor guide:** [CLAUDE.md](CLAUDE.md) — repo map, hard rules, conventions
- **Prerequisites:** [docs/PREREQUISITES.md](docs/PREREQUISITES.md)

## Status

Blueprint §35 steps 2–4 are done: repo, CI, local stack, config/logging/tracing skeleton, and the
canonical schema. Nothing ingests yet — the first source adapter is step 7.

## Quick start

```bash
make check-prereqs        # verify the toolchain (Go >= 1.25, pgvector image, ...)
cp .env.example .env
make up                   # postgres (pgvector) + redis + minio
make migrate-up           # create the schema
make psql -c '\dt'        # see the tables
make run                  # api role on :8080
                          # postgres 65432, redis 65379, minio 65000
curl localhost:8080/readyz
```

## Layout

| Path | What lives here |
|------|-----------------|
| [cmd/devsignal/](cmd/devsignal/) | The single binary; role chosen by `--role` |
| [internal/config/](internal/config/) | Typed config. The only place that reads the environment |
| [migrations/](migrations/) | golang-migrate. Never hand-write DDL outside a migration |
| [pkg/logger/](pkg/logger/) | `log/slog` setup |
| [pkg/telemetry/](pkg/telemetry/) | OpenTelemetry tracing |
| [scripts/](scripts/) | `check-prereqs.sh` |
| [docs/](docs/) | Blueprint, prerequisites |

## Commands

`make help` lists everything. Targets for tests that do not exist yet (`eval`, `test-golden`,
`test-integration`, `check-erasure`) exit non-zero and name the step that creates them.
