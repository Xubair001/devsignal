#!/usr/bin/env bash
# Verifies the local toolchain matches what the blueprint requires (§ Prerequisites).
# Exit 0 = ready to develop. Exit 1 = something required is missing or too old.
set -uo pipefail

PASS=0; WARN=0; FAIL=0
ok()   { printf '  \033[32m✓\033[0m %-22s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
warn() { printf '  \033[33m!\033[0m %-22s %s\n' "$1" "${2:-}"; WARN=$((WARN+1)); }
bad()  { printf '  \033[31m✗\033[0m %-22s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

# Go tools install to GOBIN/GOPATH/bin, which is often not on a non-login PATH.
export PATH="$PATH:$(go env GOPATH 2>/dev/null)/bin"

echo
echo "Go"
if command -v go >/dev/null 2>&1; then
  GOV=$(go env GOVERSION | sed 's/^go//')
  MAJOR=${GOV%%.*}; REST=${GOV#*.}; MINOR=${REST%%.*}
  if [ "$MAJOR" -gt 1 ] || { [ "$MAJOR" -eq 1 ] && [ "$MINOR" -ge 25 ]; }; then
    ok "go $GOV" "container-aware GOMAXPROCS available"
  else
    bad "go $GOV" "need >= 1.25 (container-aware GOMAXPROCS)"
  fi
else
  bad "go" "not installed"
fi

# GOMAXPROCS must NOT be pinned — Go >=1.25 reads the cgroup limit itself, and a
# hardcoded value overrides it and reintroduces CPU throttling.
if [ -n "${GOMAXPROCS:-}" ]; then
  warn "GOMAXPROCS=$GOMAXPROCS" "unset it; the runtime derives this from the cgroup limit"
else
  ok "GOMAXPROCS unset" "correct — runtime derives it"
fi
if [ -n "${GOMEMLIMIT:-}" ]; then
  ok "GOMEMLIMIT=$GOMEMLIMIT" ""
else
  warn "GOMEMLIMIT unset" "set to ~90% of the container memory limit when deploying"
fi

echo
echo "Go tooling"
for t in golangci-lint staticcheck sqlc migrate; do
  if command -v "$t" >/dev/null 2>&1; then ok "$t" "$(command -v $t)"; else
    bad "$t" "go install — see docs/PREREQUISITES.md"; fi
done
command -v air >/dev/null 2>&1 && ok "air" "(optional, live reload)" \
  || warn "air" "(optional) live reload not installed"

echo
echo "Containers"
if command -v docker >/dev/null 2>&1; then
  ok "docker" "$(docker --version | cut -d, -f1)"
  if timeout 10 docker info >/dev/null 2>&1; then ok "docker daemon" "reachable"; else
    bad "docker daemon" "not reachable (start it, or add your user to the docker group)"; fi
  docker compose version >/dev/null 2>&1 \
    && ok "docker compose" "$(docker compose version --short 2>/dev/null)" \
    || bad "docker compose" "v2 plugin missing"
else
  bad "docker" "not installed"
fi

echo
echo "Images (v1 dev stack)"
# Plain postgres images do NOT bundle pgvector, and v1 keeps rows, FTS and kNN in
# one database — so the pgvector image is required, not interchangeable.
for img in "pgvector/pgvector:pg17" "redis:8-alpine" "minio/minio:latest"; do
  if docker image inspect "$img" >/dev/null 2>&1; then ok "$(echo $img | cut -d: -f1)" "$img"; else
    bad "$(echo $img | cut -d: -f1)" "docker pull $img"; fi
done

echo
echo "Postgres client"
command -v psql >/dev/null 2>&1 && ok "psql" "$(psql --version | awk '{print $3}')" \
  || warn "psql" "client not installed (only needed for manual inspection)"

echo
echo "Other"
for t in make git jq; do
  command -v "$t" >/dev/null 2>&1 && ok "$t" "" || bad "$t" "not installed"
done

echo
printf 'ready: %d passed, %d warnings, %d failed\n' "$PASS" "$WARN" "$FAIL"
[ "$FAIL" -eq 0 ] || { echo "fix the failures above before starting — see docs/PREREQUISITES.md"; exit 1; }
echo "toolchain is ready."
