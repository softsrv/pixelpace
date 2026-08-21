# Authentication System Design

## Overview

Auth is built on two token types: a short-lived JWT access token (15 minutes) and a long-lived opaque refresh token (30 days). The access token travels in a cookie and optionally in `Authorization: Bearer` headers. The refresh token travels only as an `HttpOnly` cookie and is never readable by JavaScript.

All sensitive values (refresh tokens, password reset tokens, email verification tokens) are hashed with SHA-256 before storage. The raw value is transmitted once to the client and never persisted.

---

## Token Design

### Access Token (JWT)

| Property | Value |
|---|---|
| Algorithm | HS256 |
| Secret | `JWT_SECRET` env var (minimum 32 bytes) |
| Lifetime | 15 minutes (configurable via `JWT_ACCESS_EXPIRY`) |
| Cookie name | `access_token` |
| Cookie flags | `HttpOnly`, `Secure` (production), `SameSite=Lax` |

**Claims:**

```json
{
  "sub": "<UUIDv7 user ID>",
  "email": "user@example.com",
  "iat": 1700000000,
  "exp": 1700000900
}
```

The access token is also accepted from `Authorization: Bearer <token>` to support non-browser clients.

### Refresh Token

| Property | Value |
|---|---|
| Format | 32 cryptographically random bytes, base64url-encoded |
| Storage | SHA-256 hash stored in `refresh_tokens.token_hash` |
| Lifetime | 30 days (configurable via `REFRESH_TOKEN_EXPIRY`) |
| Cookie name | `refresh_token` |
| Cookie flags | `HttpOnly`, `Secure` (production), `SameSite=Lax` |
| Rotation | None — the same refresh token is reused until it expires or is revoked |

**No rotation.** `Refresh` issues a fresh access token but returns the *same* refresh token, updating `last_used_at` and device metadata in place. There is no token-family or replay-theft-detection machinery. A refresh token is invalidated only by logout, password reset, or expiry.

---

## Password Requirements

**Validation rules:**
- Minimum 8 characters (configurable via `PASSWORD_MIN_LENGTH`)
- Maximum 72 bytes — bcrypt silently truncates input beyond 72 bytes, so `users.ValidatePassword` rejects longer passwords rather than hashing a truncated value
- No complexity requirements — length beats complexity for user-chosen passwords

**Storage:**
- bcrypt with cost factor 12 (configurable via `BCRYPT_COST`)
- Passwords are never logged, never returned in responses, never stored in plaintext

---

## Email Requirements

- Stored lowercase; normalized on every write and every lookup
- Validated against RFC 5322 via library (not regex) before any DB operation
- Unique constraint enforced at the database level and pre-checked in the service layer to return a user-friendly error

---

## Login Flow

```
Client                        Handler              AuthService             DB
  |                              |                     |                    |
  |--- POST /auth/login -------->|                     |                    |
  |    {email, password}         |                     |                    |
  |                              |-- Login(req) ------->|                    |
  |                              |                     |-- GetUserByEmail -->|
  |                              |                     |<-- User / nil ------|
  |                              |                     |                    |
  |                              |       [email not found?]                 |
  |                              |       → bcrypt.Compare against a dummy    |
  |                              |         hash (timing equalizer), 401      |
  |                              |                     |                    |
  |                              |       [locked_until in future?]          |
  |                              |       YES → return 423                   |
  |                              |                     |                    |
  |                              |       bcrypt.Compare(password)           |
  |                              |       FAIL → increment attempts,         |
  |                              |              after 10 → lock 1h, 401     |
  |                              |       OK   → reset attempts + lock       |
  |                              |                     |                    |
  |                              |       generate JWT (15m)                 |
  |                              |       generate refresh token             |
  |                              |       SHA-256 hash refresh token         |
  |                              |                     |-- InsertRefreshToken|
  |                              |                     |   (with metadata)  |
  |                              |<-- tokens -----------|                    |
  |                              |                     |                    |
  |<-- set-cookie: access_token -|                     |                    |
  |<-- set-cookie: refresh_token |                     |                    |
  |<-- 200 OK + user data -------|                     |                    |
```

**Failed login handling:**
- Every failed attempt increments `failed_login_attempts` for the matching user
- After 10 failures: `locked_until = NOW() + INTERVAL '1 hour'`
- At the start of any login attempt: if `locked_until IS NOT NULL AND locked_until > NOW()` → return 423 immediately, do not check password
- On success: set `failed_login_attempts = 0`, set `locked_until = NULL`

**Email enumeration / timing:** the error message for "email not found" is identical to "wrong password". When the email does not exist, `Login` still runs a bcrypt comparison against a precomputed dummy hash so that response latency does not reveal whether the email is registered.

**Device metadata captured at login:**
- `device_name` — derived from User-Agent string (best-effort)
- `ip_address` — from `X-Forwarded-For` (only when a trusted-proxy count is configured) or `RemoteAddr`, parsed via the shared `middleware.ClientIP` helper
- `user_agent` — raw User-Agent header value

---

## Token Refresh Flow

```
Client                        Handler              AuthService             DB
  |                              |                     |                    |
  |--- POST /auth/refresh ------->|                    |                    |
  |    cookie: refresh_token      |                    |                    |
  |                              |-- Refresh(token) -->|                    |
  |                              |                     |-- GetByTokenHash ->|
  |                              |                     |<-- row or nil -----|
  |                              |                     |                    |
  |                              |       [not found]            → 401       |
  |                              |       [revoked_at NOT NULL]  → 401       |
  |                              |       [expires_at < NOW()]   → 401       |
  |                              |                     |                    |
  |                              |       [valid token]                      |
  |                              |       load user                          |
  |                              |       update last_used_at + device meta  |
  |                              |       generate new access JWT            |
  |                              |       (refresh token unchanged)          |
  |                              |<-- new access token -|                   |
  |<-- set-cookie: access_token -|                     |                    |
  |<-- set-cookie: refresh_token |  (same value, refreshed expiry window)   |
  |<-- 200 OK --------------------|                    |                    |
```

A revoked or expired refresh token is simply rejected with 401, forcing a re-login. There is no cascading family revocation, because tokens are not rotated and therefore cannot be replayed-after-rotation in the first place.

---

## Auth Middleware

Applied to protected routes (per-route, not globally). Order of evaluation:

1. Read token from `access_token` cookie; fall back to `Authorization: Bearer <token>` header
2. Parse and validate JWT signature using `JWT_SECRET`
3. Verify `exp` claim — not expired
4. Extract `sub` claim → user ID (UUIDv7)
5. Load user from DB by ID
6. Verify user record exists
7. Attach user to `context.Context` under a typed key
8. Call `next` handler

**On expiry (401):** the middleware sets `HX-Trigger: token-expired` for HTMX requests. A listener in `web/static/js/app.js` catches the event, calls `/auth/refresh`, and retries on success or redirects to `/login` on failure. On a missing token the middleware sets `HX-Redirect: /login`. An authenticated-but-unverified user is redirected to `/verify-email`.

---

## Logout

`POST /auth/logout`:
1. Read `refresh_token` cookie
2. Hash and look up in DB
3. Set `revoked_at = NOW()` for that token (no-op if the token is unknown)
4. Clear both `access_token` and `refresh_token` cookies (zero-length, expired)
5. Redirect to `/login`

---

## Session Management

### List Active Sessions

`GET /auth/sessions` (authenticated):
- Query all `refresh_tokens` for the current user where `revoked_at IS NULL AND expires_at > NOW()`
- Return rendered table: `device_name`, `ip_address`, `last_used_at`, `created_at`
- Mark current session (matched by current `refresh_token` cookie hash) visually

### Revoke Specific Session

`DELETE /auth/sessions/{id}` (authenticated):
- Verify the token belongs to the current user (authorization check — not just authentication). Ownership failures are masked as **404** so they don't confirm a token exists.
- Set `revoked_at = NOW()`
- If the revoked token is the caller's current session, respond with a logout redirect; otherwise return the updated session-list fragment for HTMX swap

### Revoke All Sessions

Triggered automatically on **password reset**: all refresh tokens for the user are revoked in the same transaction that updates the password, forcing re-login on every device.

---

## Password Reset Flow

### Request Reset (`POST /auth/forgot-password`)

1. Always return `200 OK` with "If that email is registered, a reset link has been sent." — prevents email enumeration
2. If the email exists in DB:
   - Check rate limit: max 3 requests per hour per email (counted from the DB)
   - Generate a 32-byte cryptographically random token; URL-safe base64-encode for the link
   - SHA-256 hash the raw token; store the hash in `password_reset_tokens` with `expires_at = NOW() + 1 hour`
   - Send email containing `{APP_BASE_URL}/reset-password?token={raw_token}`

### Complete Reset (`POST /auth/reset-password`)

1. Receive `token` (URL query param or form field) and `new_password`
2. Validate the new password against policy
3. SHA-256 hash the token; look up in `password_reset_tokens`
4. Reject if: not found, `used_at IS NOT NULL`, or `expires_at < NOW()`
5. In a transaction:
   - `bcrypt.GenerateFromPassword(newPassword, cost)` → update `users.password_hash` (the `UpdatePasswordHash` query also clears `failed_login_attempts` and `locked_until`)
   - Set `password_reset_tokens.used_at = NOW()`
   - Revoke all `refresh_tokens` for the user (`revoked_at = NOW()`)
6. Send password-change confirmation email
7. Redirect to `/login`

---

## Email Verification

Verification is **link-based**, not code-based.

### Issuance (on registration, and via resend)

1. Generate a 32-byte cryptographically random token (`crypto/rand`), URL-safe base64-encoded
2. SHA-256 hash the token; store the hash in `email_verification_codes` with `expires_at = NOW() + 24 hours`
3. Email a link: `{APP_BASE_URL}/auth/verify-email?token={raw_token}`
4. Rate limit: max 3 verification emails per hour per user (`ResendVerification` returns a retry time when exceeded)

### Verification (`GET /auth/verify-email?token=...`, public)

1. SHA-256 hash the token from the query string and look up the record
2. Reject if: not found, `used_at IS NOT NULL`, or `expires_at < NOW()`
3. Mark the record used, set `users.email_verified = true`
4. Issue a fresh token pair so the click **auto-logs the user in**, then redirect to the dashboard

---

## CSRF Protection

There are **no CSRF tokens**. CSRF is mitigated by:

- `SameSite=Lax` on the auth cookies — cross-site POSTs do not carry the session cookies
- Same-origin form posts — all forms are served from and submitted to the same origin
- State-changing endpoints accept only their intended methods (`POST`/`DELETE`), and the access token must be present as a cookie set by this origin

If a stricter posture is needed later (e.g. embedding in third-party contexts), a synchronizer-token or double-submit scheme can be layered on, but it is intentionally omitted here to keep the surface small.

---

## Request Body Limit

A global `BodyLimit` middleware wraps the mux and caps request bodies at 1 MiB (`DefaultMaxBodyBytes`) via `http.MaxBytesReader`, protecting every endpoint from oversized-payload memory pressure before handlers run.

---

## Rate Limiting

Implemented as in-memory middleware using `sync.Map` with atomic counters and TTL-based expiry. Does not require Redis. Not shared across multiple instances (acceptable for initial deployment; Redis can be layered in later).

| Endpoint | Limit | Window | Key |
|---|---|---|---|
| `POST /auth/login` | 5 attempts | 15 minutes | IP address |
| `POST /auth/register` | 3 attempts | 1 hour | IP address |
| `POST /auth/refresh` | 10 attempts | 1 minute | refresh token hash (falls back to IP) |
| `POST /auth/forgot-password` | 3 requests | 1 hour | lowercase email |
| `POST /auth/reset-password` | 5 attempts | 1 hour | IP address |

**Response when limit exceeded:**
- HTTP 429 Too Many Requests
- `Retry-After: <seconds>` header set to remaining TTL

**TTL cleanup:** a background goroutine sweeps the `sync.Map` periodically to remove expired entries and prevent unbounded memory growth.

---

## Security Invariants

| Concern | Mitigation |
|---|---|
| Password storage | bcrypt cost 12; passwords capped at 72 bytes; never logged or returned |
| Refresh token storage | SHA-256 hashed; raw token transmitted once, never persisted |
| Reset / verification token storage | SHA-256 hashed; raw token only in the emailed link |
| Token comparison | tokens are looked up by SHA-256 hash; bcrypt for passwords |
| SQL injection | Parameterized queries only (sqlc-generated) |
| XSS | `html/template` auto-escaping; HTMX responses are also HTML-escaped |
| CSRF | `SameSite=Lax` cookies + same-origin form posts (no CSRF tokens) |
| Session theft | HttpOnly cookies prevent JS access to tokens |
| Cookie security | `Secure=true` gated on `APP_ENV=production` |
| Email enumeration | Identical messages for found/not-found; dummy-hash timing equalizer on login |
| Brute force | Account lockout after 10 failures; IP-level rate limits |
| Token revocation | Logout and password reset revoke refresh tokens; expired/revoked tokens rejected at refresh |
| Oversized payloads | 1 MiB request body limit via `http.MaxBytesReader` |
| CORS | Not configured; all clients served from same origin |
| Secrets in images | Docker build args used only for tooling; secrets come from env vars at runtime |
