#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if ! docker info >/dev/null 2>&1; then
  echo "Error: Docker is not running or unreachable" >&2
  exit 1
fi

SUFFIX="${$}_${RANDOM}"
PG_CONTAINER="pixelpace-it-${SUFFIX}-pg"
VALKEY_CONTAINER="pixelpace-it-${SUFFIX}-valkey"

SUCCESS=0

cleanup() {
  local rc=$?
  trap - EXIT INT TERM
  docker rm -f "$PG_CONTAINER" "$VALKEY_CONTAINER" >/dev/null 2>&1 || true
  if [ "$rc" -eq 0 ] && [ "$SUCCESS" -ne 1 ]; then
    rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

docker run -d \
  --name "$PG_CONTAINER" \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=pixelpace_test \
  -p 127.0.0.1::5432 \
  postgres:17 >/dev/null

docker run -d \
  --name "$VALKEY_CONTAINER" \
  -p 127.0.0.1::6379 \
  valkey/valkey >/dev/null

PG_PORT="$(docker port "$PG_CONTAINER" 5432 | head -n 1 | sed 's/.*://')"
VALKEY_PORT="$(docker port "$VALKEY_CONTAINER" 6379 | head -n 1 | sed 's/.*://')"

if [ -z "$PG_PORT" ] || [ -z "$VALKEY_PORT" ]; then
  echo "Error: Failed to obtain mapped host ports for containers" >&2
  exit 1
fi

READY_ATTEMPTS="${READY_ATTEMPTS:-60}"
READY_INTERVAL="${READY_INTERVAL:-1}"

pg_ready=0
for ((i = 1; i <= READY_ATTEMPTS; i++)); do
  # Probe over TCP: the image's init-phase temp server listens only on the unix
  # socket, so a socket probe can pass just before that server shuts down.
  if docker exec "$PG_CONTAINER" pg_isready -h 127.0.0.1 -p 5432 -U postgres -d pixelpace_test >/dev/null 2>&1; then
    pg_ready=1
    break
  fi
  sleep "$READY_INTERVAL"
done

if [ "$pg_ready" -ne 1 ]; then
  echo "Error: Postgres failed to become ready within timeout (${READY_ATTEMPTS} attempts)" >&2
  exit 1
fi

valkey_ready=0
for ((i = 1; i <= READY_ATTEMPTS; i++)); do
  if docker exec "$VALKEY_CONTAINER" valkey-cli ping >/dev/null 2>&1; then
    valkey_ready=1
    break
  fi
  sleep "$READY_INTERVAL"
done

if [ "$valkey_ready" -ne 1 ]; then
  echo "Error: Valkey failed to become ready within timeout (${READY_ATTEMPTS} attempts)" >&2
  exit 1
fi

for f in $(printf '%s\n' db/migrations/*.up.sql | sort); do
  echo "Applying migration: $f"
  if ! docker exec -i "$PG_CONTAINER" psql -U postgres -d pixelpace_test -v ON_ERROR_STOP=1 < "$f" >/dev/null; then
    echo "Error: Migration failed: $f" >&2
    exit 1
  fi
done

export TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:${PG_PORT}/pixelpace_test?sslmode=disable"
export VALKEY_ADDR="127.0.0.1:${VALKEY_PORT}"

go test -tags integration -p 1 ./... "$@"
SUCCESS=1
