package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/mssola/useragent"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/middleware"
)

// authServicer defines the subset of app.AuthService that AuthHandler requires.
// Accepting an interface (rather than the concrete type) makes the handler
// independently testable without a real database or SMTP server.
type authServicer interface {
	Register(ctx context.Context, email, password string) (db.User, error)
	Login(ctx context.Context, email, password string, meta app.DeviceMeta) (app.TokenResult, error)
	Logout(ctx context.Context, rawRefreshToken string) error
	Refresh(ctx context.Context, rawRefreshToken string, meta app.DeviceMeta) (app.TokenResult, error)
	RequestPasswordReset(ctx context.Context, rawEmail string) error
	CompletePasswordReset(ctx context.Context, rawToken, newPassword string) error
	VerifyEmail(ctx context.Context, rawToken string, meta app.DeviceMeta) (app.TokenResult, error)
	ResendVerification(ctx context.Context, userID uuid.UUID) error
}

// AuthHandler groups all authentication HTTP handlers.
type AuthHandler struct {
	auth              authServicer
	renderer          *TemplateRenderer
	secure            bool // true in production (Secure cookie flag)
	trustedProxyCount int  // number of trusted reverse-proxy hops for client-IP extraction
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(authSvc authServicer, renderer *TemplateRenderer, secure bool, trustedProxyCount int) *AuthHandler {
	return &AuthHandler{
		auth:              authSvc,
		renderer:          renderer,
		secure:            secure,
		trustedProxyCount: trustedProxyCount,
	}
}

// ── Pages ─────────────────────────────────────────────────────────────────────

func (h *AuthHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.renderer.Page(w, http.StatusOK, "auth/login.html", nil)
}

func (h *AuthHandler) RegisterPage(w http.ResponseWriter, r *http.Request) {
	h.renderer.Page(w, http.StatusOK, "auth/register.html", nil)
}

func (h *AuthHandler) ForgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	h.renderer.Page(w, http.StatusOK, "auth/forgot-password.html", nil)
}

func (h *AuthHandler) ResetPasswordPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	h.renderer.Page(w, http.StatusOK, "auth/reset-password.html", map[string]any{
		"Token": token,
	})
}

func (h *AuthHandler) VerifyEmailPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Resent": r.URL.Query().Get("resent") == "1",
	}
	if ra := r.URL.Query().Get("retry_after"); ra != "" {
		if mins, err := strconv.Atoi(ra); err == nil && mins > 0 {
			data["RetryAfter"] = mins
		}
	}
	h.renderer.Page(w, http.StatusOK, "auth/verify-email.html", data)
}

// ── Actions ───────────────────────────────────────────────────────────────────

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		slog.WarnContext(r.Context(), "register: parse form", "error", err)
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data")
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")

	if _, err := h.auth.Register(r.Context(), email, password); err != nil {
		slog.WarnContext(r.Context(), "register failed", "error", err)
		h.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Issue tokens immediately after registration so the user is authenticated
	// when they reach /verify-email. POST /auth/verify-email is protected by
	// authMW and will reject unauthenticated requests.
	meta := h.deviceMeta(r)
	result, err := h.auth.Login(r.Context(), email, password, meta)
	if err != nil {
		// Registration succeeded — log the auto-login failure and redirect anyway.
		// The user will be prompted to log in on the verify-email page.
		slog.Error("auto-login after register", "error", err)
		htmxRedirect(w, "/verify-email")
		return
	}

	h.setTokenCookies(w, result)
	htmxRedirect(w, "/verify-email")
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		slog.WarnContext(r.Context(), "login: parse form", "error", err)
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data")
		return
	}

	meta := h.deviceMeta(r)
	result, err := h.auth.Login(r.Context(), r.FormValue("email"), r.FormValue("password"), meta)
	if err != nil {
		slog.WarnContext(r.Context(), "login failed", "error", err)
		status := http.StatusUnauthorized
		if errors.Is(err, app.ErrAccountLocked) {
			status = http.StatusLocked
		}
		h.renderError(w, r, status, err.Error())
		return
	}

	h.setTokenCookies(w, result)
	if !result.EmailVerified {
		htmxRedirect(w, "/verify-email")
		return
	}
	htmxRedirect(w, "/dashboard")
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("refresh_token"); err == nil {
		if logoutErr := h.auth.Logout(r.Context(), cookie.Value); logoutErr != nil {
			slog.WarnContext(r.Context(), "logout: revoke refresh token", "error", logoutErr)
		}
	}
	clearAuthCookies(w, h.secure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	meta := h.deviceMeta(r)
	result, err := h.auth.Refresh(r.Context(), cookie.Value, meta)
	if err != nil {
		slog.WarnContext(r.Context(), "token refresh failed", "error", err)
		clearAuthCookies(w, h.secure)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	h.setTokenCookies(w, result)
	w.WriteHeader(http.StatusOK)
}

func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		slog.WarnContext(r.Context(), "forgot password: parse form", "error", err)
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data")
		return
	}

	// Error is intentionally not returned to the caller (prevents email enumeration),
	// but we log it so server-side failures don't go unnoticed.
	if err := h.auth.RequestPasswordReset(r.Context(), r.FormValue("email")); err != nil {
		slog.ErrorContext(r.Context(), "request password reset", "error", err)
	}

	h.renderer.Partial(w, http.StatusOK, "partials/flash.html", map[string]any{
		"Message": "If that email is registered, a reset link has been sent.",
	})
}

func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		slog.WarnContext(r.Context(), "reset password: parse form", "error", err)
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data")
		return
	}

	token := r.FormValue("token")
	newPassword := r.FormValue("password")

	if err := h.auth.CompletePasswordReset(r.Context(), token, newPassword); err != nil {
		slog.WarnContext(r.Context(), "reset password failed", "error", err)
		status := http.StatusUnprocessableEntity
		if errors.Is(err, app.ErrTokenNotFound) || errors.Is(err, app.ErrTokenExpired) || errors.Is(err, app.ErrTokenUsed) {
			status = http.StatusBadRequest
		}
		h.renderError(w, r, status, err.Error())
		return
	}

	htmxRedirect(w, "/login")
}

// VerifyEmail handles the link the user clicks from their inbox.
// It is a public GET — no session required — because the token itself is the credential.
func (h *AuthHandler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Redirect(w, r, "/verify-email", http.StatusSeeOther)
		return
	}

	meta := h.deviceMeta(r)
	result, err := h.auth.VerifyEmail(r.Context(), token, meta)
	if err != nil {
		slog.WarnContext(r.Context(), "verify email failed", "error", err)
		data := map[string]any{}
		if errors.Is(err, app.ErrTokenExpired) {
			data["Expired"] = true
		} else {
			data["Error"] = true
		}
		h.renderer.Page(w, http.StatusOK, "auth/verify-email.html", data)
		return
	}

	h.setTokenCookies(w, result)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// ResendVerification issues a fresh verification link to the authenticated user.
func (h *AuthHandler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := h.auth.ResendVerification(r.Context(), user.ID); err != nil {
		if errors.Is(err, app.ErrEmailAlreadyVerified) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		var rlErr app.RateLimitedError
		if errors.As(err, &rlErr) {
			mins := int(time.Until(rlErr.RetryAt).Minutes()) + 1
			http.Redirect(w, r, fmt.Sprintf("/verify-email?retry_after=%d", mins), http.StatusSeeOther)
			return
		}
		slog.WarnContext(r.Context(), "resend verification failed", "user_id", user.ID, "error", err)
		http.Redirect(w, r, "/verify-email", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/verify-email?resent=1", http.StatusSeeOther)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (h *AuthHandler) setTokenCookies(w http.ResponseWriter, result app.TokenResult) {
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    result.AccessToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  result.AccessTokenExpiry,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    result.RefreshToken,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  result.RefreshTokenExpiry,
	})
}

func (h *AuthHandler) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	// HTMX 2.0 only processes 2xx responses for DOM swaps by default.
	// Downgrade to 200 for HTMX requests so the error partial is actually
	// swapped into the target element (e.g. #form-error).
	if r.Header.Get("HX-Request") == "true" {
		status = http.StatusOK
	}
	h.renderer.Partial(w, status, "partials/error.html", map[string]any{"Error": msg})
}

func htmxRedirect(w http.ResponseWriter, path string) {
	w.Header().Set("HX-Redirect", path)
	w.WriteHeader(http.StatusOK)
}

// deviceMeta extracts client IP and user-agent metadata from the request.
// Client-IP extraction goes through middleware.ClientIP, which only trusts
// X-Forwarded-For when the service is configured behind trusted proxies — so
// the stored IP can't be spoofed in a direct-to-internet deployment.
func (h *AuthHandler) deviceMeta(r *http.Request) app.DeviceMeta {
	var addr *netip.Addr

	if parsed, err := netip.ParseAddr(middleware.ClientIP(r, h.trustedProxyCount)); err == nil {
		addr = &parsed
	}

	ua := useragent.New(r.UserAgent())
	browser, _ := ua.Browser()
	os := ua.OS()
	deviceName := browser
	if os != "" && browser != "" {
		deviceName = browser + " on " + os
	}

	return app.DeviceMeta{
		DeviceName: deviceName,
		IPAddress:  addr,
		UserAgent:  r.UserAgent(),
	}
}
