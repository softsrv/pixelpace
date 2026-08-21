# HTTP Layer Design

## Overview

The HTTP layer is built on Go's `net/http` stdlib. There is no third-party router. Route matching uses Go 1.22+ enhanced pattern matching (method + path patterns). All middleware is composed as `http.Handler` wrappers. HTMX drives all partial-page updates from the server.

---

## Middleware Stack

Middleware is applied in the following order (outermost first). Every request passes through all global middleware before hitting route-specific middleware.

```
Request
  │
  ▼
[RequestID]        — attach unique ID to context; add X-Request-ID response header
  │
  ▼
[StructuredLogging] — log method, path, status, latency, request ID at INFO
  │
  ▼
[SecurityHeaders]  — security headers / CSP
  │
  ▼
[BodyLimit]        — cap request body at 1 MiB via http.MaxBytesReader
  │
  ▼
[Router]           — dispatch to handler
  │
  ▼
[RateLimit] (route-specific) — per-endpoint limits (see auth doc); 429 + Retry-After on breach
  │
  ▼
[Auth] (route-specific) — validate JWT; attach user to context; 401 on failure
  │
  ▼
Handler
```

There is no global CSRF middleware — CSRF is mitigated by `SameSite=Lax` cookies plus same-origin posts. `RateLimit` and `Auth` are applied per-route (not globally): rate limits wrap only the specific auth endpoints, and auth wraps only protected routes so public routes (login, register, health) are never blocked by missing tokens.

---

## Router Layout (`internal/http/router.go`)

```
Public routes (no auth middleware):
  GET  /                          → redirect to /login or /dashboard
  GET  /login                     → render login page
  POST /auth/login                → process login (rate limited)
  GET  /register                  → render registration page
  POST /auth/register             → process registration (rate limited)
  GET  /forgot-password           → render forgot-password form
  POST /auth/forgot-password      → request reset email (rate limited)
  GET  /reset-password            → render reset-password form (token in query param)
  POST /auth/reset-password       → complete password reset (rate limited)
  GET  /verify-email              → render "check your email" / verification status page
  GET  /auth/verify-email         → consume verification link token; auto-login; redirect
  POST /auth/refresh              → issue new access token, reuse refresh token (rate limited)

  GET  /health                    → liveness check (always 200)
  GET  /ready                     → readiness check (200 if DB reachable, else 503)

  GET  /static/...                → serve embedded static assets (CSS, JS)

Protected routes (auth middleware applied):
  POST /auth/logout               → revoke refresh token; clear cookies; redirect /login

  GET  /auth/sessions             → list active sessions for current user
  DELETE /auth/sessions/{id}      → revoke a specific session

  GET  /dashboard                 → main authenticated view (placeholder)
```

---

## Handler Responsibilities (`internal/http/handlers/`)

Handlers are intentionally thin. Each handler:

1. Parses and validates HTTP input (form values, path params, cookies)
2. Calls exactly one service method
3. Renders a response (HTML template, HTMX fragment, or redirect)

**What handlers must not do:**
- Contain business logic
- Call the database directly
- Make decisions about password policy, token lifetimes, lockout thresholds

### `handlers/auth.go`

| Handler | Method | Path | Description |
|---|---|---|---|
| `HandleLoginPage` | GET | `/login` | Render login form |
| `HandleLogin` | POST | `/auth/login` | Call `AuthService.Login`; set cookies on success |
| `HandleRegisterPage` | GET | `/register` | Render registration form |
| `HandleRegister` | POST | `/auth/register` | Call `AuthService.Register`; send verification email |
| `HandleLogout` | POST | `/auth/logout` | Call `AuthService.Logout`; clear cookies |
| `HandleRefresh` | POST | `/auth/refresh` | Call `AuthService.Refresh`; new access token, same refresh token |
| `HandleForgotPasswordPage` | GET | `/forgot-password` | Render forgot-password form |
| `HandleForgotPassword` | POST | `/auth/forgot-password` | Call `AuthService.RequestPasswordReset` |
| `HandleResetPasswordPage` | GET | `/reset-password` | Render reset form with token from query |
| `HandleResetPassword` | POST | `/auth/reset-password` | Call `AuthService.CompletePasswordReset` |
| `HandleVerifyEmailPage` | GET | `/verify-email` | Render "check your email" / status page |
| `HandleVerifyEmail` | GET | `/auth/verify-email` | Consume link token via `AuthService.VerifyEmail`; auto-login |
| `HandleResendVerification` | POST | `/auth/resend-verification` | Call `AuthService.ResendVerification` (authenticated) |

> Handler methods are named without the `Handle` prefix in code (e.g. `Login`, `Refresh`, `VerifyEmail` on `*AuthHandler`); the table uses the descriptive form.

### `handlers/sessions.go`

| Handler | Method | Path | Description |
|---|---|---|---|
| `HandleListSessions` | GET | `/auth/sessions` | Render session list for current user |
| `HandleRevokeSession` | DELETE | `/auth/sessions/{id}` | Revoke session; return updated list fragment |

### `handlers/health.go`

| Handler | Method | Path | Description |
|---|---|---|---|
| `HandleLiveness` | GET | `/health` | Always 200 OK |
| `HandleReadiness` | GET | `/ready` | 200 if DB ping succeeds; 503 otherwise |

---

## Service Layer (`internal/app/`)

The service layer sits between handlers and the DB. It owns all business logic.

### `AuthService`

```
AuthService
  ├── Login(ctx, email, password, deviceMeta) → (TokenResult, error)
  ├── Register(ctx, email, password) → (User, error)
  ├── Logout(ctx, rawRefreshToken) → error
  ├── Refresh(ctx, rawRefreshToken, deviceMeta) → (TokenResult, error)  // same refresh token reused
  ├── RequestPasswordReset(ctx, email) → error
  ├── CompletePasswordReset(ctx, rawToken, newPassword) → error
  ├── VerifyEmail(ctx, rawToken, deviceMeta) → (TokenResult, error)     // link token; auto-login
  └── ResendVerification(ctx, userID) → error
```

### `UserService`

```
UserService
  ├── GetByID(ctx, id) → (User, error)
  ├── ListSessions(ctx, userID) → ([]Session, error)
  └── RevokeSession(ctx, userID, tokenID) → error
```

Services receive a `*db.Queries` instance (or a DB transaction factory) via constructor injection. They never accept raw `http.Request` objects — they work only with typed Go values.

---

## HTMX Response Patterns

The app is server-rendered first. HTMX is used for partial swaps and form submissions — it doesn't drive navigation (full page loads use normal `HX-Redirect` headers or standard redirects).

### Success Responses

| Scenario | Response |
|---|---|
| Form submission succeeds, redirect needed | `HX-Redirect: /path` header + 200 body |
| Partial content update | HTML fragment + 200; HTMX swaps into `hx-target` |
| Trigger client-side event | `HX-Trigger: eventName` header |
| Dynamically change swap target | `HX-Retarget: #selector` + `HX-Reswap: innerHTML` |

### Error Responses

| Scenario | Status | Response |
|---|---|---|
| Validation failure | 422 | HTML fragment with error messages; swapped into form |
| Auth failure (expired token) | 401 | `HX-Trigger: token-expired` header |
| Auth failure (no token) | 401 | `HX-Redirect: /login` |
| Rate limited | 429 | `Retry-After` header + error fragment |
| Server error | 500 | Error fragment or `HX-Reswap: innerHTML` |

### Token Expiry Handling (Client-Side)

The base template includes a small JS block:

```js
document.body.addEventListener('token-expired', async () => {
  const res = await fetch('/auth/refresh', { method: 'POST' });
  if (res.ok) {
    // Retry the original HTMX request
    htmx.trigger(document.body, 'retry-request');
  } else {
    window.location.href = '/login';
  }
});
```

This keeps the retry logic in one place and out of individual handlers.

---

## Template Organization (`web/templates/`)

All templates use Go's `html/template`. Auto-escaping is always active. Templates never use `template.HTML` casts unless the source is guaranteed safe (e.g., rendering a pre-validated integer).

```
web/templates/
  base.html                   — root layout: <html>, nav, theme switcher, script tags
  dashboard.html              — authenticated landing view
  auth/
    login.html                — login form (email + password)
    register.html             — registration form
    forgot-password.html      — forgot-password form
    reset-password.html       — reset form + token hidden field
    verify-email.html         — "check your email" / verification status page
  partials/
    error.html                — inline error message fragment (for HTMX swaps)
    flash.html                — success/info flash message fragment
    session-list.html         — full session list (for initial load + HTMX swaps)
```

### Template Data Convention

Each handler passes a typed struct to the template renderer, never a `map[string]interface{}`. This catches missing fields at compile time and makes templates self-documenting.

Example:
```go
type LoginPageData struct {
    Error string
    Email string // re-populate on validation failure
}
```

---

## Static Asset Serving

Static files (compiled CSS, JS) are embedded into the binary using Go's `embed` package at build time. This means the deployed binary contains all assets — no separate file server or CDN required for basic deployment.

```go
//go:embed web/static
var staticFiles embed.FS
```

In development, `air` reloads the binary on source changes, and Tailwind runs in watch mode separately (`make tailwind-watch`). The `make dev` target runs both concurrently.

In production, the same embedded assets are served. Cache headers (`Cache-Control: public, max-age=31536000`) are applied to static paths to allow downstream caches and browsers to cache aggressively. Cache-busting is achieved by hashing the file content into the URL (or using a build timestamp suffix).
