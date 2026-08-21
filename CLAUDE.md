# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make dev              # Hot-reload: air (Go) + Tailwind watch + smtp4dev container, all in parallel
make run              # Run without hot-reload
make build            # Compile to ./bin/app
make test             # Run unit tests
make test-integration # Run integration tests (requires Docker; uses testcontainers)
make test-cover       # Run tests with HTML coverage report
make fmt              # gofmt the repo
make lint             # golangci-lint run

# Database
make migrate-up                   # Apply pending migrations
make migrate-down                 # Roll back last migration
make migrate-create NAME=<name>   # Create new migration pair
make migrate-status               # Show current migration version
make sqlc-generate                # Regenerate internal/db/ from db/queries/

# CSS
make daisyui-install  # Download DaisyUI .mjs bundles (re-run on DaisyUI upgrades)
make tailwind         # One-shot CSS build
make tailwind-watch   # Watch mode (called by make dev)

# Docker
make docker-build     # Build dev image
make prod             # Build production image (multi-stage, distroless, non-root)
```

To run a single test:
```bash
go test ./internal/auth/... -run TestJWTRoundTrip
go test -tags integration ./internal/app/... -run TestAuthServiceIntegration
```

## Architecture

`cmd/app/main.go` is bootstrap-only: config parsing, DB pool construction, dependency wiring, HTTP server start, and graceful shutdown (SIGINT/SIGTERM → 30s drain → pool close). No business logic lives here.

### Layer flow

```
HTTP handlers (internal/http/handlers/)
    ↓
Service layer (internal/app/)          ← business logic + orchestration
    ↓
DB layer (internal/db/)                ← sqlc-generated, pgx/v5 driver
    ↑
Middleware (internal/http/middleware/)  ← auth, rate limiting, security headers, body limit, logging, request ID
```

### Key packages

| Package | Purpose |
|---|---|
| `internal/app/` | `AuthService` and `UserService` — all auth flows, password reset, email verification |
| `internal/auth/` | JWT issue/validate, token hashing (SHA-256), refresh/reset/verification token generation |
| `internal/db/` | sqlc-generated repository code; never edit by hand — regenerate with `make sqlc-generate` |
| `internal/email/` | `Mailer` interface + `SMTPMailer` impl + `NoopMailer` for tests; email templates |
| `internal/http/` | Router wiring in `router.go`; handlers and middleware in sub-packages |
| `internal/users/` | Email normalization and validation, password validation |

### Auth design

- **Access tokens**: JWT (HS256), 15-minute lifetime, delivered as `access_token` httpOnly cookie and in response body.
- **Refresh tokens**: 32-byte random, SHA-256 hashed before DB storage, 30-day lifetime, `refresh_token` httpOnly cookie. **No rotation** — `Refresh` issues a new access token and reuses the same refresh token, updating `last_used_at` and device metadata in place.
- **Cookies**: `SameSite=Lax`, httpOnly, `Secure` in production. No CSRF tokens — CSRF is mitigated by SameSite=Lax plus same-origin form posts.
- **Email verification**: link-based. A 32-byte token (SHA-256 hashed in DB, 24h lifetime) is emailed; the public `GET /auth/verify-email?token=` endpoint marks the email verified and auto-logs the user in.
- **Login timing**: when the email is unknown, `Login` still runs a bcrypt comparison against a precomputed dummy hash so response latency doesn't reveal which emails are registered.
- **Account locking**: 10 failed login attempts → `locked_until = NOW() + 1h`.
- **Token cleanup**: background goroutine fires daily at 03:00; purges expired/revoked tokens per retention policy.

### Template rendering

`main.go` parses `base.html` once at startup into a base `*template.Template`. `handlers.TemplateRenderer` clones this base and parses the page-specific `.html` file on each request. This avoids the `{{define "content"}}` global-overwrite problem in Go's template engine when multiple page templates share a name.

### Middleware chain (outermost → innermost)

```
RequestID → Logging → SecurityHeaders → BodyLimit → mux
```

`BodyLimit` caps request bodies at 1 MiB (`DefaultMaxBodyBytes`) via `http.MaxBytesReader`. Auth middleware (`middleware.Authenticate`) is applied per-route, not globally.

### Rate limiting

In-memory (`sync.Map`), not shared across instances. Key functions: `IPKeyFunc`, `CookieRefreshTokenKeyFunc`, `FormEmailKeyFunc`. Limits: login 5/15min, register 3/hr, refresh 10/min, forgot-password 3/hr, reset-password 5/hr.

## Database

Migrations in `db/migrations/` (golang-migrate, run manually via `make migrate-up` — never on startup). SQL queries in `db/queries/` — edit these, then run `make sqlc-generate` to regenerate `internal/db/`.

pgxpool config: max 25 conns (`DB_MAX_CONNS`), min 5, max lifetime 1h, idle timeout 10min, health check 1min.

## Environment

Copy `.env.example` to `.env`. Required vars: `DATABASE_URL`, `JWT_SECRET` (≥32 bytes), `APP_BASE_URL`, and all `SMTP_*` vars. `APP_ENV=production` gates JSON logging, secure cookies, and suppresses debug error detail.

For local development, `make dev` starts smtp4dev automatically — SMTP is available at `localhost:2525`, web UI at `http://localhost:5000`.

## Tech constraints

- Go stdlib first; propose libraries with rationale before adding.
- `html/template` only (not `text/template`) — auto-escaping prevents XSS.
- No frontend frameworks; HTMX for dynamics.
- Parameterized SQL only (sqlc enforces this).
- bcrypt cost factor 12; passwords capped at 72 bytes (bcrypt's input limit) in `users.ValidatePassword`.
- UUIDv7 via `github.com/google/uuid`.
