# DevSignal. Targets that do not exist yet fail loudly with the blueprint step
# that creates them — a gate that silently passes is worse than no gate.
SHELL := /bin/bash
export PATH := $(PATH):$(shell go env GOPATH)/bin

DB_URL ?= postgres://devsignal:devsignal@localhost:65432/devsignal?sslmode=disable
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
test-golden: ## [step 7] source parser fixture tests
	@echo "not built yet — arrives with the first source adapter (blueprint §35 step 7)"; exit 1

.PHONY: test-integration
test-integration: ## integration tests against the real stack (needs make up)
	DATABASE_URL="$(DB_URL)" go test -tags integration -count=1 -timeout 300s ./...

.PHONY: eval
eval: ## [step 16] ranking evaluation harness — gates scoring changes
	@echo "not built yet — arrives with the eval harness (blueprint §35 step 16)"; exit 1

.PHONY: check-erasure
check-erasure: ## [step 11] proves a deleted identifier appears nowhere
	@echo "not built yet — arrives with profile/resume ingestion (blueprint §35 step 11)"; exit 1
