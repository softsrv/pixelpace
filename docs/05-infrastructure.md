# Infrastructure, Observability & Testing

## Docker

### Multi-Stage Build

The Dockerfile uses two stages to produce a minimal, secure runtime image.

```
Stage 1: builder
  Base:    golang:1.23-alpine
  Purpose: compile the Go binary with all dependencies
  Steps:
    - Copy go.mod / go.sum; download modules (layer cache)
    - Copy source; run `go build -ldflags="-s -w" -o /app/bin/app ./cmd/app`
    - Strip debug symbols (-s -w) to reduce binary size

Stage 2: runtime
  Base:    gcr.io/distroless/static:latest  (or alpine:latest)
  Purpose: minimal attack surface — no shell, no package manager
  Steps:
    - Create non-root user (UID 1000, GID 1000)
    - Copy compiled binary from builder stage
    - Copy web/templates and web/static (if not embedded in binary)
    - Set USER 1000
    - EXPOSE 8080
    - ENTRYPOINT ["/app/bin/app"]
```

**Security properties:**
- No secrets baked into layers — all config comes from env vars at runtime
- Non-root execution (UID 1000)
- Distroless base has no shell → no exec attacks if container is compromised
- Build artifacts stay in the builder stage; runtime image contains only the binary

### `.dockerignore`

```
.env*
.git/
.gitignore
*.md
bin/
tmp/
node_modules/
web/static/css/dist/    # Tailwind output (rebuilt in Docker build)
```

---

## Makefile

All development and deployment workflows are driven by `make`. No `bash` scripts; no undocumented commands.

| Target | Command / Behavior |
|---|---|
| `make dev` | `make -j2 air tailwind-watch` — hot-reload + Tailwind watch in parallel |
| `make run` | `go run ./cmd/app` — run without live-reload |
| `make build` | `go build -o bin/app ./cmd/app` |
| `make test` | `go test ./...` |
| `make fmt` | `gofmt -w .` and `goimports -w .` |
| `make lint` | `golangci-lint run` |
| `make tailwind` | One-shot Tailwind CSS build (minified) |
| `make tailwind-watch` | Tailwind in watch mode (dev only) |
| `make migrate-up` | `migrate -path db/migrations -database $DATABASE_URL up` |
| `make migrate-down` | `migrate -path db/migrations -database $DATABASE_URL down 1` |
| `make migrate-create NAME=x` | `migrate create -ext sql -dir db/migrations -seq x` |
| `make migrate-status` | `migrate -path db/migrations -database $DATABASE_URL version` |
| `make sqlc-generate` | `sqlc generate` |
| `make docker-build` | `docker build -t app:dev .` |
| `make prod` | Build production image with `APP_ENV=production` tag |
| `make clean` | `rm -rf bin/ tmp/` |

`.air.toml` is configured to watch `**/*.go`, `web/templates/**`, and rebuild on change.

---

## Environment Variables

All configuration is read from environment variables at startup into a typed `Config` struct. Missing required variables cause an immediate startup failure with a clear error message.

| Variable | Required | Default | Notes |
|---|---|---|---|
| `APP_ENV` | No | `development` | `production` enables JSON logs, Secure cookies, hides error details |
| `DATABASE_URL` | Yes | — | Full PostgreSQL connection string |
| `DB_MAX_CONNS` | No | `25` | Max connections in pgxpool |
| `PORT` | No | `8080` | HTTP listen port |
| `JWT_SECRET` | Yes | — | Min 32 bytes; used for HS256 signing |
| `JWT_ACCESS_EXPIRY` | No | `15m` | Go duration string |
| `REFRESH_TOKEN_EXPIRY` | No | `720h` | 30 days |
| `BCRYPT_COST` | No | `12` | Integer 10–14 |
| `PASSWORD_MIN_LENGTH` | No | `8` | Integer |
| `APP_BASE_URL` | Yes | — | e.g. `https://app.example.com` — used in password reset links |
| `SMTP_HOST` | Yes | — | SMTP server hostname |
| `SMTP_PORT` | Yes | — | SMTP port (usually 587 or 465) |
| `SMTP_USERNAME` | Yes | — | SMTP auth username |
| `SMTP_PASSWORD` | Yes | — | SMTP auth password |
| `SMTP_FROM_EMAIL` | Yes | — | From address for all outbound email |
| `SMTP_FROM_NAME` | No | App name | Display name in From header |

`.env.example` ships with every variable listed, values as `<PLACEHOLDER>`, and a comment explaining each one.

---

## Observability

### Structured Logging (`log/slog`)

All logging goes through `log/slog`. The handler is configured at startup:

- `APP_ENV=production` → JSON handler (machine-parseable)
- `APP_ENV=development` → Text handler (human-readable)

**Log levels in use:**

| Level | When used |
|---|---|
| DEBUG | Detailed flow tracing (disabled in production by default) |
| INFO | Request completed, server started, shutdown events |
| WARN | Rate limit violations, recoverable errors, deprecated usage |
| ERROR | 5xx errors, DB failures, unexpected panics |

**Request ID middleware** generates a UUID per request, attaches it to `context.Context`, and logs it with every log line originating from that request's goroutine. The value is also returned as the `X-Request-ID` response header for correlation with client-side logs.

**Log fields included on every request log line:**

```
request_id, method, path, status, latency_ms, remote_addr, user_agent
```

**5xx errors** are logged at ERROR level with a full stack trace (using `runtime/debug.Stack()`).

### Health Endpoints

| Endpoint | Purpose | Response |
|---|---|---|
| `GET /health` | Liveness — is the process alive? | Always `200 OK` + `{"status":"ok"}` |
| `GET /ready` | Readiness — can it serve traffic? | `200 OK` if `pgxpool.Ping()` succeeds; `503` otherwise |

The `/ready` endpoint is used by Kubernetes/Docker health checks to gate traffic. The process can be live (healthy) but not ready (DB unreachable) — these are intentionally separate.

### Error Tracking

5xx errors are logged locally with stack traces. Sentry or a similar external error tracker is not required by default but the codebase should be structured so one can be added by wrapping the error logger — no structural changes needed.

---

## Testing Strategy

### Unit Tests

Unit tests live alongside the code they test (e.g., `internal/auth/jwt_test.go`). They use `testing` stdlib only. Table-driven tests are the default pattern.

**Minimum required coverage areas:**

| Area | Test file | Coverage target |
|---|---|---|
| JWT issue + validation | `internal/auth/jwt_test.go` | ≥80% |
| Token generation + hashing | `internal/auth/tokens_test.go` | ≥80% |
| Password validation | `internal/users/validate_test.go` | ≥80% |
| Auth middleware (mock DB) | `internal/http/middleware/auth_test.go` | ≥70% |
| Body-size limit | `internal/http/middleware/bodylimit_test.go` | ≥70% |
| Rate limiter logic | `internal/http/middleware/ratelimit_test.go` | ≥70% |
| Auth + session handlers | `internal/http/handlers/auth_test.go`, `sessions_test.go` | ≥70% |

### Integration Tests

Integration tests run against a real PostgreSQL instance. Two options are supported:

1. **testcontainers-go** — spins up a Postgres container per test suite; no external setup required
2. **Docker Compose** — a `docker-compose.test.yml` with a Postgres service; tests connect via `TEST_DATABASE_URL`

Integration test files use the `//go:build integration` build tag so `make test` (unit-only) doesn't require Docker. A separate `make test-integration` target runs them.

**Required integration test scenarios:**

| Scenario | Test |
|---|---|
| Migration up/down | Verify schema is applied and reversed cleanly |
| User repository: create, get by email, update | Full round-trip via real DB |
| Login flow | Correct credentials → tokens issued; wrong password → 401; locked account → 423 |
| Token refresh | Valid token → new access token, same refresh token; revoked/expired → 401 |
| Session revocation | `DELETE /auth/sessions/{id}` revokes only the target token; other user's token → 404 |
| Password reset | Request → email sent (mock SMTP); complete → old sessions revoked |
| Email verification | Link token → email verified + auto-login; expired/used token → rejected |
| Rate limiting | Exceed limit → 429; wait → 200 again |

### Test Conventions

- Table-driven tests for all cases with more than two scenarios
- Use `t.Parallel()` at the top of every test that doesn't share mutable state
- Subtests via `t.Run("description", ...)` for readability
- Test helpers return `testing.TB` errors (not `t.Fatal`) so they compose cleanly
- No `init()` in test files

### Coverage Target

| Package | Target |
|---|---|
| `internal/auth/` | ≥70% |
| `internal/users/` | ≥70% |
| `internal/app/` | ≥70% (with integration tests) |
| `internal/http/middleware/` | ≥70% |
| `internal/http/handlers/` | ≥60% (handlers are thin; full coverage via integration) |

Run `go test -coverprofile=coverage.out ./...` then `go tool cover -html=coverage.out` to inspect.

---

## Development Workflow

```
# One-time setup
make migrate-up         # apply DB schema
make sqlc-generate      # generate DB code
make tailwind           # initial CSS build

# Day-to-day
make dev                # hot-reload Go + Tailwind watch (concurrent)

# Before committing
make fmt                # format all Go files
make lint               # golangci-lint
make test               # unit tests
make test-integration   # integration tests (requires Docker)

# Deployment
make prod               # build production Docker image
docker run --env-file .env app:prod
```

## Production Deployment Checklist

- `APP_ENV=production` is set
- `JWT_SECRET` is ≥32 bytes of random data
- `DATABASE_URL` points to a production Postgres instance
- Migrations have been run (`make migrate-up`) before deploying a new image
- SMTP credentials are configured and tested
- Container runs as UID 1000 (enforced by Dockerfile)
- No `.env` files are present inside the image
- `/ready` endpoint is wired to the load balancer health check
- Log aggregation is consuming the JSON log stream
