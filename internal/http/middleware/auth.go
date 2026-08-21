package middleware

import (
	"context"
	"errors"
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

// Authenticate validates the JWT from either the cookie or Authorization header,
// loads the user, and attaches it to the request context.
func Authenticate(queries UserFetcher, jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := extractToken(r)
			if tokenStr == "" {
				respondUnauthorized(w, r)
				return
			}

			claims, err := auth.ValidateAccessToken(tokenStr, jwtSecret)
			if err != nil {
				if errors.Is(err, auth.ErrTokenExpired) {
					// Signal HTMX clients to refresh.
					w.Header().Set("HX-Trigger", "token-expired")
				}
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
