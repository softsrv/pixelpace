package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/middleware"
)

// userServicer defines the subset of app.UserService that SessionHandler requires.
// Accepting an interface makes the handler independently testable.
type userServicer interface {
	ListSessions(ctx context.Context, userID uuid.UUID) ([]db.RefreshToken, error)
	RevokeSession(ctx context.Context, userID, tokenID uuid.UUID) (db.RefreshToken, error)
}

// SessionHandler groups session management HTTP handlers.
type SessionHandler struct {
	users    userServicer
	renderer *TemplateRenderer
	secure   bool
}

// NewSessionHandler constructs a SessionHandler.
func NewSessionHandler(userSvc userServicer, renderer *TemplateRenderer, secure bool) *SessionHandler {
	return &SessionHandler{users: userSvc, renderer: renderer, secure: secure}
}

// ListSessions renders the active sessions for the authenticated user.
func (h *SessionHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	sessions, err := h.users.ListSessions(r.Context(), user.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list sessions", "user_id", user.ID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.renderer.Partial(w, http.StatusOK, "partials/session-list.html", map[string]any{
		"Sessions": sessions,
		"UserID":   user.ID,
	})
}

// RevokeSession revokes a specific refresh token. If the revoked token belongs
// to the current request's session, it clears auth cookies and redirects to
// /login so the browser doesn't continue with a now-invalid session.
func (h *SessionHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	tokenIDStr := r.PathValue("id")
	tokenID, err := uuid.Parse(tokenIDStr)
	if err != nil {
		slog.WarnContext(r.Context(), "revoke session: invalid token id", "token_id", tokenIDStr, "error", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	revoked, err := h.users.RevokeSession(r.Context(), user.ID, tokenID)
	if err != nil {
		if errors.Is(err, app.ErrForbidden) || errors.Is(err, app.ErrTokenNotFound) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "revoke session", "user_id", user.ID, "token_id", tokenID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// If the user just revoked their own current session, clear cookies and
	// force a login. The JWT is still technically valid until it expires, but
	// this prevents the browser from continuing to use a revoked session.
	if cookie, cookieErr := r.Cookie("refresh_token"); cookieErr == nil {
		if auth.CompareTokenHash(cookie.Value, revoked.TokenHash) {
			clearAuthCookies(w, h.secure)
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Return updated session list fragment for HTMX swap.
	sessions, err := h.users.ListSessions(r.Context(), user.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list sessions after revoke", "user_id", user.ID, "error", err)
	}
	h.renderer.Partial(w, http.StatusOK, "partials/session-list.html", map[string]any{
		"Sessions": sessions,
		"UserID":   user.ID,
	})
}
