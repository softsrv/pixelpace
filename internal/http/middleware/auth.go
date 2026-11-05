package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
)

type userContextKey struct{}

// UserFetcher is satisfied by *db.Queries.
type UserFetcher interface {
	GetUserByID(ctx context.Context, id uuid.UUID) (db.User, error)
}

// SessionRefresher renews an expired session from the request's refresh
// token: it sets fresh auth cookies on w and returns the new access token, or
// reports false when the session cannot be renewed. *handlers.AuthHandler
// implements it.
type SessionRefresher interface {
	RefreshSession(w http.ResponseWriter, r *http.Request) (accessToken string, ok bool)
}

// AuthOption configures Authenticate.
type AuthOption func(*authConfig)

type authConfig struct {
	refresher SessionRefresher
}

// WithSessionRefresh makes Authenticate renew a missing, expired or invalid
// access token from the refresh token before turning the request away, so a
// user whose session is still alive never sees the login page.
func WithSessionRefresh(r SessionRefresher) AuthOption {
	return func(c *authConfig) { c.refresher = r }
}

// Authenticate validates the JWT from either the cookie or Authorization header,
// loads the user, and attaches it to the request context. With
// WithSessionRefresh, a request without a valid access token is first renewed
// from its refresh token, and goes through if that succeeds.
func Authenticate(queries UserFetcher, jwtSecret string, opts ...AuthOption) func(http.Handler) http.Handler {
	var cfg authConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := auth.ValidateAccessToken(extractToken(r), jwtSecret)
			if err != nil && cfg.refresher != nil {
				if token, ok := cfg.refresher.RefreshSession(w, r); ok {
					claims, err = auth.ValidateAccessToken(token, jwtSecret)
				}
			}
			if err != nil {
				respondUnauthorized(w, r)
				return
			}

			userID, err := uuid.Parse(claims.Subject)
			if err != nil {
				slog.WarnContext(r.Context(), "auth: invalid user id in token claims", "subject", claims.Subject, "error", err)
				respondUnauthorized(w, r)
				return
			}

			user, err := queries.GetUserByID(r.Context(), userID)
			if err != nil {
				slog.WarnContext(r.Context(), "auth: get user by id", "user_id", userID, "error", err)
				respondUnauthorized(w, r)
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey{}, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext retrieves the authenticated user from context.
func UserFromContext(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(userContextKey{}).(db.User)
	return u, ok
}

// extractToken reads the JWT from the access_token cookie or Authorization header.
func extractToken(r *http.Request) string {
	if cookie, err := r.Cookie("access_token"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
		return strings.TrimPrefix(hdr, "Bearer ")
	}
	return ""
}

// RequireEmailVerified rejects requests from authenticated users whose email
// address has not yet been verified, redirecting them to /verify-email.
// Must be applied after Authenticate (which populates the user context).
func RequireEmailVerified(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFromContext(r.Context())
		if !ok || !user.EmailVerified {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/verify-email")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "/verify-email", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func respondUnauthorized(w http.ResponseWriter, r *http.Request) {
	// For HTMX requests, redirect via header so the partial swap works.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
