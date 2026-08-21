# Database Design

## Overview

PostgreSQL is the single source of truth. All access goes through sqlc-generated, type-safe Go functions. Schema is version-controlled via golang-migrate SQL files. The application never modifies the schema at runtime.

## Connection Pooling (`pgxpool`)

The pool is configured at startup from environment variables:

| Parameter | Default | Env override |
|---|---|---|
| Max connections | 25 | `DB_MAX_CONNS` |
| Min connections | 5 | — |
| Max connection lifetime | 1 hour | — |
| Max connection idle time | 10 minutes | — |
| Health check period | 1 minute | — |

The pool is closed last during graceful shutdown, after the HTTP server has drained all in-flight requests. This ordering ensures no handler is cut off mid-query.

## Schema

### `users`

The central identity table. Email is always stored lowercase. UUIDs are version 7 (time-ordered) to avoid index fragmentation on inserts.

```sql
CREATE TABLE users (
    id                    UUID PRIMARY KEY,            -- UUIDv7
    email                 TEXT NOT NULL UNIQUE,         -- stored lowercase
    password_hash         TEXT NOT NULL,                -- bcrypt cost 12
    email_verified        BOOLEAN NOT NULL DEFAULT false,
    failed_login_attempts INTEGER NOT NULL DEFAULT 0,
    locked_until          TIMESTAMPTZ,                  -- null = not locked
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users (email);
```

**Key invariants:**
- `email` is normalized to lowercase before every write and every lookup.
- `locked_until` is set to `NOW() + 1 hour` after 10 consecutive failed login attempts; it is cleared to `NULL` on successful login.
- `failed_login_attempts` is reset to `0` on successful login.
- `updated_at` is maintained by the application layer (not a trigger) to avoid hidden side effects.

---

### `refresh_tokens`

Stores hashed refresh tokens. The raw token is never persisted. Tokens are **not rotated**: a refresh reuses the same token row and updates `last_used_at` and device metadata in place.

```sql
CREATE TABLE refresh_tokens (
    id                  UUID PRIMARY KEY,          -- UUIDv7
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash          TEXT NOT NULL,             -- SHA-256(raw_token), hex-encoded
    device_name         TEXT,                      -- e.g. "Chrome on macOS"
    ip_address          INET,                      -- source IP at issuance
    user_agent          TEXT,
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at          TIMESTAMPTZ               -- null = active
);

CREATE INDEX idx_refresh_tokens_token_hash   ON refresh_tokens (token_hash);
CREATE INDEX idx_refresh_tokens_user_id      ON refresh_tokens (user_id);
CREATE INDEX idx_refresh_tokens_expires_at   ON refresh_tokens (expires_at);
```

> The original schema (migration 2) had `token_family` and `previous_token_id` columns for rotation-based theft detection. Migration 5 dropped them when rotation was removed.

**Key invariants:**
- `token_hash` is `hex(SHA-256(raw_token))`. The raw token is never stored.
- Each login creates exactly one row; refresh updates that row rather than creating a new one.
- `revoked_at` being non-null means the token is dead (set on logout or password reset). The daily cleanup job purges expired/revoked rows per the retention policy.

---

### `password_reset_tokens`

Single-use tokens sent via email for password reset. The raw token travels only in the reset link URL; only its hash is stored.

```sql
CREATE TABLE password_reset_tokens (
    id          UUID PRIMARY KEY,          -- UUIDv7
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL,             -- SHA-256(raw_token), hex-encoded
    expires_at  TIMESTAMPTZ NOT NULL,      -- 1 hour from creation
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_at     TIMESTAMPTZ               -- null = not yet used
);

CREATE INDEX idx_prt_token_hash ON password_reset_tokens (token_hash);
CREATE INDEX idx_prt_user_id    ON password_reset_tokens (user_id);
CREATE INDEX idx_prt_expires_at ON password_reset_tokens (expires_at);
```

**Key invariants:**
- `used_at` is set atomically when the token is consumed. A second attempt with the same token is rejected.
- Tokens older than 1 hour are considered expired regardless of `used_at`.
- The cleanup job removes rows where `used_at` or `expires_at` is older than 7 days.

---

### `email_verification_codes`

Hashed tokens backing the email-verification **link** sent to a new user. The table name is historical; verification is link-based, not a 6-digit code. The raw token travels only in the verification link URL; only its SHA-256 hash is stored.

```sql
CREATE TABLE email_verification_codes (
    id          UUID PRIMARY KEY,          -- UUIDv7
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL,             -- SHA-256(raw_token), hex-encoded
    expires_at  TIMESTAMPTZ NOT NULL,      -- 24 hours from creation
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_at     TIMESTAMPTZ               -- null = not yet used
);

CREATE INDEX idx_evc_user_id    ON email_verification_codes (user_id);
CREATE INDEX idx_evc_expires_at ON email_verification_codes (expires_at);
```

> Migration 4 created this with a plaintext `code` column; migration 6 renamed it to `token_hash` when verification switched from 6-digit codes to hashed links.

**Key invariants:**
- At most 3 verification emails may be issued per user per hour (enforced in the service layer before insert).
- A token is valid only if `used_at IS NULL` and `expires_at > NOW()`.
- The cleanup job removes rows where `used_at IS NOT NULL` and older than 7 days, or where `expires_at < NOW() - INTERVAL '1 day'`.

---

## Migration Strategy

Migrations are managed entirely by `golang-migrate` and run via Makefile targets — never automatically on application startup. This keeps schema changes deliberate and auditable.

```
db/migrations/
  000001_create_users.up.sql
  000001_create_users.down.sql
  000002_create_refresh_tokens.up.sql
  000002_create_refresh_tokens.down.sql
  000003_create_password_reset_tokens.up.sql
  000003_create_password_reset_tokens.down.sql
  000004_create_email_verification_codes.up.sql
  000004_create_email_verification_codes.down.sql
```

Each migration pair is atomic. `up` must be reversible by its corresponding `down`. Destructive migrations (column drops, table drops) require a matching `down` that re-creates the structure.

**Commands:**
- `make migrate-up` — apply all pending migrations
- `make migrate-down` — roll back the last applied migration
- `make migrate-status` — show applied vs pending
- `make migrate-create NAME=add_last_login` — scaffold a new numbered pair

---

## sqlc Configuration

`db/sqlc.yaml` directs sqlc to generate Go code into `internal/db/`:

```yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "db/queries/"
    schema: "db/migrations/"
    gen:
      go:
        package: "db"
        out: "internal/db"
        sql_driver: "pgx/v5"
        emit_json_tags: true
        emit_prepared_queries: false
        emit_interface: true
        emit_exact_table_names: false
```

Query files in `db/queries/` use sqlc annotations:

| Annotation | Use case |
|---|---|
| `:one` | Return a single row (e.g., `GetUserByEmail`) |
| `:many` | Return a slice (e.g., `ListActiveSessions`) |
| `:exec` | Execute with no return (e.g., `RevokeToken`) |
| `:execrows` | Execute and return affected row count |

Generated files in `internal/db/` are never edited by hand. Run `make sqlc-generate` after changing any `.sql` query file.

---

## Token Cleanup Background Job

A background goroutine started in `main.go` runs daily at 03:00 server time to purge stale token rows. It is stopped gracefully during shutdown (via a context cancellation passed from the signal handler).

**Cleanup schedule:**

| Table | Condition | Retention |
|---|---|---|
| `refresh_tokens` | `expires_at < NOW() - 90 days` | 90 days post-expiry |
| `refresh_tokens` | `revoked_at < NOW() - 90 days` | 90 days post-revocation |
| `password_reset_tokens` | `used_at < NOW() - 7 days` | 7 days |
| `password_reset_tokens` | `expires_at < NOW() - 7 days` | 7 days post-expiry |
| `email_verification_codes` | `used_at < NOW() - 7 days` | 7 days |
| `email_verification_codes` | `expires_at < NOW() - 1 day` | 1 day post-expiry |

Retention rationale: refresh tokens are kept 90 days for forensic audit (theft detection timelines). Reset and verification records have no forensic value after use and are removed sooner.
