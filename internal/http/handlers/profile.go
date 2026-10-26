package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
	"github.com/softsrv/starter/internal/users"
)

type profileAuthServicer interface {
	ChangePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error
}

type profileUserServicer interface {
	DeleteAccount(ctx context.Context, userID uuid.UUID) error
	SetDisplayName(ctx context.Context, userID uuid.UUID, name string) error
}

// ProfileHandler groups profile management HTTP handlers.
type ProfileHandler struct {
	auth     profileAuthServicer
	users    profileUserServicer
	renderer *TemplateRenderer
	secure   bool
}

// NewProfileHandler constructs a ProfileHandler.
func NewProfileHandler(authSvc profileAuthServicer, userSvc profileUserServicer, renderer *TemplateRenderer, secure bool) *ProfileHandler {
	return &ProfileHandler{auth: authSvc, users: userSvc, renderer: renderer, secure: secure}
}

// ProfilePage renders the user profile page.
func (h *ProfileHandler) ProfilePage(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.renderer.Page(w, http.StatusOK, "profile.html", map[string]any{
		"User": user,
	})
}

// ChangePassword handles password change form submission.
func (h *ProfileHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data")
		return
	}

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	if newPassword != confirmPassword {
		h.renderError(w, r, http.StatusUnprocessableEntity, "New passwords do not match")
		return
	}

	if err := h.auth.ChangePassword(r.Context(), user.ID, currentPassword, newPassword); err != nil {
		slog.WarnContext(r.Context(), "change password failed", "user_id", user.ID, "error", err)
		msg := err.Error()
		if errors.Is(err, app.ErrInvalidCredentials) {
			msg = "Current password is incorrect"
		}
		h.renderError(w, r, http.StatusUnprocessableEntity, msg)
		return
	}

	h.renderer.Partial(w, http.StatusOK, "partials/flash.html", map[string]any{
		"Message": "Password changed successfully.",
	})
}

// SetDisplayName handles display name update form submission.
func (h *ProfileHandler) SetDisplayName(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form data")
		return
	}

	displayName := strings.TrimSpace(r.FormValue("display_name"))

	if err := h.users.SetDisplayName(r.Context(), user.ID, displayName); err != nil {
		switch {
		case errors.Is(err, app.ErrDisplayNameTaken):
			h.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, users.ErrDisplayNameEmpty),
			errors.Is(err, users.ErrDisplayNameTooShort),
			errors.Is(err, users.ErrDisplayNameTooLong),
			errors.Is(err, users.ErrDisplayNameLooksLikeEmail),
			errors.Is(err, users.ErrDisplayNameInvalidChars):
			h.renderError(w, r, http.StatusUnprocessableEntity, err.Error())
		default:
			slog.ErrorContext(r.Context(), "set display name", "user_id", user.ID, "error", err)
			h.renderError(w, r, http.StatusInternalServerError, "Failed to update display name. Please try again.")
		}
		return
	}

	h.renderer.Partial(w, http.StatusOK, "partials/flash.html", map[string]any{
		"Message": "Display name updated successfully.",
	})
}

// DeleteAccount permanently deletes the authenticated user's account.
func (h *ProfileHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := h.users.DeleteAccount(r.Context(), user.ID); err != nil {
		slog.ErrorContext(r.Context(), "delete account", "user_id", user.ID, "error", err)
		h.renderError(w, r, http.StatusInternalServerError, "Failed to delete account. Please try again.")
		return
	}

	clearAuthCookies(w, h.secure)
	w.Header().Set("HX-Redirect", "/login")
	w.WriteHeader(http.StatusOK)
}

func (h *ProfileHandler) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.Header.Get("HX-Request") == "true" {
		status = http.StatusOK
	}
	h.renderer.Partial(w, status, "partials/error.html", map[string]any{"Error": msg})
}
