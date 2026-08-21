# DevSignal. Targets that do not exist yet fail loudly with the blueprint step
# that creates them — a gate that silently passes is worse than no gate.
SHELL := /bin/bash
export PATH := $(PATH):$(shell go env GOPATH)/bin

DB_URL ?= postgres://devsignal:devsignal@localhost:65432/devsignal?sslmode=disable
S3_ENDPOINT ?= http://localhost:65000
S3_ACCESS_KEY ?= devsignal
S3_SECRET_KEY ?= devsignal123
BIN    := bin/devsignal

.PHONY: help
help: ## show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- environment
.PHONY: check-prereqs
check-prereqs: ## verify the local toolchain
	@./scripts/check-prereqs.sh

.PHONY: up
up: ## start postgres (pgvector), redis, minio
	docker compose up -d --wait

.PHONY: down
down: ## stop the stack, keep volumes
	docker compose down

.PHONY: nuke
nuke: ## stop the stack and DELETE all local data
	docker compose down -v

.PHONY: psql
psql: ## open a psql shell against the dev database
	@psql "$(DB_URL)"

# ---------------------------------------------------------------- migrations
.PHONY: migrate-up
migrate-up: ## apply all migrations
	migrate -path migrations -database "$(DB_URL)" up

.PHONY: migrate-down
migrate-down: ## roll back one migration
	migrate -path migrations -database "$(DB_URL)" down 1

.PHONY: migrate-reset
migrate-reset: ## roll everything back (local only)
	migrate -path migrations -database "$(DB_URL)" down -all

.PHONY: migrate-new
migrate-new: ## create a migration pair: make migrate-new name=add_foo
	@test -n "$(name)" || { echo "usage: make migrate-new name=add_foo"; exit 1; }
	migrate create -ext sql -dir migrations -seq $(name)

.PHONY: migrate-version
migrate-version: ## show the current schema version
	migrate -path migrations -database "$(DB_URL)" version

.PHONY: sqlc
sqlc: ## regenerate Go from SQL — required after any schema change
	sqlc generate

# ---------------------------------------------------------------- build / run
.PHONY: build
build: ## build the binary
	go build -o $(BIN) ./cmd/devsignal

.PHONY: run
run: ## run the api role
	go run ./cmd/devsignal --role=api

.PHONY: run-worker
run-worker: ## run pipeline workers + source polling
	go run ./cmd/devsignal --role=worker

.PHONY: add-source
add-source: ## register a source: make add-source name=greenhouse:gitlab
	@test -n "$(name)" || { echo "usage: make add-source name=greenhouse:gitlab"; exit 1; }
	go run ./cmd/devsignal --role=add-source --source=$(name)

.PHONY: ingest
ingest: ## poll one source once: make ingest name=greenhouse:gitlab
	@test -n "$(name)" || { echo "usage: make ingest name=greenhouse:gitlab"; exit 1; }
	go run ./cmd/devsignal --role=ingest-once --source=$(name)

# ---------------------------------------------------------------- quality
.PHONY: fmt
fmt: ## gofmt
	gofmt -l -w .

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: lint
lint: ## golangci-lint + staticcheck
	golangci-lint run ./...
	staticcheck ./...

.PHONY: test
test: ## unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: check
check: fmt vet lint test ## everything that must pass before a commit

# ---------------------------------------------------------------- not yet built
# These are documented in CLAUDE.md and must become real gates at their step.
# They exit non-zero on purpose: nobody should believe a gate passed when the
# gate does not exist.
.PHONY: test-golden
test-golden: ## source parser fixture tests — refuse to auto-rebaseline
	go test -count=1 ./internal/source/...

.PHONY: golden-update
golden-update: ## rewrite golden files (deliberate act; review the diff)
	go test -count=1 ./internal/source/... -update

.PHONY: test-integration
test-integration: ## integration tests against the real stack (needs make up)
	DATABASE_URL="$(DB_URL)" S3_ENDPOINT="$(S3_ENDPOINT)" \
	S3_ACCESS_KEY="$(S3_ACCESS_KEY)" S3_SECRET_KEY="$(S3_SECRET_KEY)" \
	go test -tags integration -count=1 -timeout 300s ./...

.PHONY: eval
eval: ## [step 16] ranking evaluation harness — gates scoring changes
	@echo "not built yet — arrives with the eval harness (blueprint §35 step 16)"; exit 1

.PHONY: check-erasure
check-erasure: ## proves an erased user leaves no trace in any store
	DATABASE_URL="$(DB_URL)" S3_ENDPOINT="$(S3_ENDPOINT)" \
	S3_ACCESS_KEY="$(S3_ACCESS_KEY)" S3_SECRET_KEY="$(S3_SECRET_KEY)" \
	go test -tags integration -count=1 -v -run 'TestErasure|TestExtractedTextIsNot' \
	  ./internal/profile/ ./pkg/blob/
