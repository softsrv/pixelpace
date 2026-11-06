#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Every docker call runs under a time limit so a stalled registry or a wedged
# daemon fails the run with a named step instead of hanging it.
if command -v timeout >/dev/null 2>&1; then
  TIMEOUT_BIN=timeout
elif command -v gtimeout >/dev/null 2>&1; then
  TIMEOUT_BIN=gtimeout
else
  echo "Error: 'timeout' (GNU coreutils) is required to bound docker calls; install coreutils (macOS: brew install coreutils for gtimeout)" >&2
  exit 1
fi

# run_bounded <secs> <step> <cmd...>: run cmd with a time limit; on timeout name the step.
run_bounded() {
  local secs="$1" step="$2" status=0
  shift 2
  "$TIMEOUT_BIN" -k 5 "$secs" "$@" || status=$?
  if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
    echo "Error: ${step} timed out after ${secs}s" >&2
  fi
  return "$status"
}

PULL_TIMEOUT="${PULL_TIMEOUT:-600}"
DOCKER_TIMEOUT="${DOCKER_TIMEOUT:-60}"
PROBE_TIMEOUT="${PROBE_TIMEOUT:-5}"
MIGRATION_TIMEOUT="${MIGRATION_TIMEOUT:-120}"

if ! run_bounded 15 "docker info" docker info >/dev/null 2>&1; then
  echo "Error: Docker is not running or unreachable (docker info failed or timed out)" >&2
  exit 1
fi

PG_IMAGE="postgres:17"
VALKEY_IMAGE="valkey/valkey"

SUFFIX="${$}_${RANDOM}"
PG_CONTAINER="pixelpace-it-${SUFFIX}-pg"
VALKEY_CONTAINER="pixelpace-it-${SUFFIX}-valkey"
HOST_LABEL="$(hostname 2>/dev/null || echo unknown)"
LABEL_ARGS=(
  --label "pixelpace-it=1"
  --label "pixelpace-it.run=${SUFFIX}"
  --label "pixelpace-it.pid=$$"
  --label "pixelpace-it.host=${HOST_LABEL}"
)
TEST_LOG=""

# Startup sweep: remove containers left by an earlier run on this host whose
# process is gone (SIGKILL, host crash). A live run's containers are never touched.
if stale="$(run_bounded 15 "docker ps (stale sweep)" docker ps -a \
  --filter "label=pixelpace-it=1" --filter "label=pixelpace-it.host=${HOST_LABEL}" \
  --format '{{.ID}} {{.Label "pixelpace-it.pid"}}')"; then
  while read -r cid cpid; do
    [ -n "$cid" ] || continue
    if [ -n "$cpid" ] && kill -0 "$cpid" 2>/dev/null; then
      continue
    fi
    echo "Removing stale integration container ${cid} (run pid ${cpid:-unknown} is gone)"
    run_bounded 30 "docker rm (stale ${cid})" docker rm -f -v "$cid" >/dev/null ||
      echo "Warning: could not remove stale container ${cid}; remove it with: docker rm -f -v ${cid}" >&2
  done <<<"$stale"
else
  echo "Warning: could not list stale integration containers; continuing" >&2
fi

SUCCESS=0
DOCKER_RUN_INCOMPLETE=0

# remove_run_containers: remove every container labelled with this run, plus
# both names (a name can exist before its labels are visible). Prints the
# containers it could not remove and returns non-zero if any remain.
remove_run_containers() {
  local ids="" target left="" rm_out
  if ! ids="$(run_bounded 15 "docker ps (cleanup)" docker ps -aq --filter "label=pixelpace-it.run=${SUFFIX}")"; then
    left="(label pixelpace-it.run=${SUFFIX}: listing failed)"
    ids=""
  fi
  for target in $ids "$PG_CONTAINER" "$VALKEY_CONTAINER"; do
    if rm_out="$(run_bounded 30 "docker rm ${target} (cleanup)" docker rm -f -v "$target" 2>&1)"; then
      continue
    fi
    case "$rm_out" in
      *"No such container"*) ;;
      *) left="${left} ${target}" ;;
    esac
  done
  if [ -n "$left" ]; then
    echo "Error: cleanup left integration containers behind:${left}" >&2
    echo "  remove them with: docker rm -f -v \$(docker ps -aq --filter label=pixelpace-it.run=${SUFFIX})" >&2
    return 1
  fi
  return 0
}

cleanup() {
  local rc=$? clean=0
  trap - EXIT INT TERM
  remove_run_containers || clean=1
  if [ "$DOCKER_RUN_INCOMPLETE" -eq 1 ]; then
    # A killed `docker run` client can leave the daemon creating the container
    # after the first removal; sweep by label again to catch it.
    for _ in 1 2 3; do
      sleep 3
      remove_run_containers || clean=1
    done
  fi
  if [ -n "$TEST_LOG" ]; then
    rm -f "$TEST_LOG"
  fi
  if [ "$rc" -eq 0 ] && { [ "$SUCCESS" -ne 1 ] || [ "$clean" -ne 0 ]; }; then
    rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

for image in "$PG_IMAGE" "$VALKEY_IMAGE"; do
  if ! run_bounded "$PULL_TIMEOUT" "docker pull ${image}" docker pull "$image" >/dev/null; then
    echo "Error: Failed to pull image: ${image}" >&2
    exit 1
  fi
done

if ! run_bounded "$DOCKER_TIMEOUT" "docker run ${PG_IMAGE}" docker run -d \
  --name "$PG_CONTAINER" \
  "${LABEL_ARGS[@]}" \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=pixelpace_test \
  -p 127.0.0.1::5432 \
  "$PG_IMAGE" >/dev/null; then
  DOCKER_RUN_INCOMPLETE=1
  echo "Error: Failed to start Postgres container" >&2
  exit 1
fi

if ! run_bounded "$DOCKER_TIMEOUT" "docker run ${VALKEY_IMAGE}" docker run -d \
  --name "$VALKEY_CONTAINER" \
  "${LABEL_ARGS[@]}" \
  -p 127.0.0.1::6379 \
  "$VALKEY_IMAGE" >/dev/null; then
  DOCKER_RUN_INCOMPLETE=1
  echo "Error: Failed to start Valkey container" >&2
  exit 1
fi

# No pipeline here: under errexit+pipefail a failing `docker port` inside a
# pipeline would exit before the named error below could be printed.
if ! PG_PORT_RAW="$(run_bounded 15 "docker port (Postgres)" docker port "$PG_CONTAINER" 5432)"; then
  echo "Error: Failed to obtain mapped host port for Postgres (docker port failed)" >&2
  exit 1
fi
if ! VALKEY_PORT_RAW="$(run_bounded 15 "docker port (Valkey)" docker port "$VALKEY_CONTAINER" 6379)"; then
  echo "Error: Failed to obtain mapped host port for Valkey (docker port failed)" >&2
  exit 1
fi
PG_PORT="${PG_PORT_RAW%%$'\n'*}"
PG_PORT="${PG_PORT##*:}"
VALKEY_PORT="${VALKEY_PORT_RAW%%$'\n'*}"
VALKEY_PORT="${VALKEY_PORT##*:}"

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
  if run_bounded "$PROBE_TIMEOUT" "pg_isready probe" docker exec "$PG_CONTAINER" pg_isready -h 127.0.0.1 -p 5432 -U postgres -d pixelpace_test >/dev/null 2>&1; then
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
  if run_bounded "$PROBE_TIMEOUT" "valkey-cli ping probe" docker exec "$VALKEY_CONTAINER" valkey-cli ping >/dev/null 2>&1; then
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
  if ! run_bounded "$MIGRATION_TIMEOUT" "migration $f" docker exec -i "$PG_CONTAINER" psql -U postgres -d pixelpace_test -v ON_ERROR_STOP=1 < "$f" >/dev/null; then
    echo "Error: Migration failed: $f" >&2
    exit 1
  fi
done

export TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:${PG_PORT}/pixelpace_test?sslmode=disable"
export VALKEY_ADDR="127.0.0.1:${VALKEY_PORT}"

# -v so every skip is visible: the integration tests skip when their service is
# unreachable, and a skip is not a failure to go test, so this run checks for it.
# Extra arguments (e.g. -run TestName) are forwarded to go test. ${@+"$@"} keeps
# bash 3.2 (macOS) from treating an empty "$@" as unbound under set -u.
TEST_LOG="$(mktemp)"
set +e
go test -tags integration -p 1 -v ./... ${@+"$@"} 2>&1 | tee "$TEST_LOG"
test_rc=${PIPESTATUS[0]}
set -e

if [ "$test_rc" -ne 0 ]; then
  echo "Error: integration tests failed (go test exit ${test_rc})" >&2
  exit "$test_rc"
fi

if grep -Eq '^[[:space:]]*--- SKIP:' "$TEST_LOG"; then
  echo "Error: integration tests skipped (this run must not skip any):" >&2
  grep -E '^[[:space:]]*--- SKIP:' "$TEST_LOG" | sed -E 's/^[[:space:]]*--- SKIP: /  /' >&2
  exit 1
fi

if ! grep -Eq '^[[:space:]]*--- PASS:' "$TEST_LOG"; then
  echo "Error: no integration test ran (check the -run filter or the integration build tag)" >&2
  exit 1
fi

SUCCESS=1
