package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/middleware"
)

// friendServicer is the subset of FriendService used by the HTTP handlers.
type friendServicer interface {
	Send(context.Context, uuid.UUID, uuid.UUID) (db.FriendRequest, error)
	Accept(context.Context, uuid.UUID, uuid.UUID) error
	Reject(context.Context, uuid.UUID, uuid.UUID) error
	ListFriends(context.Context, uuid.UUID) ([]uuid.UUID, error)
}

// FriendHandler groups friend request and friendship HTTP handlers.
type FriendHandler struct {
	friends friendServicer
}

// NewFriendHandler constructs a FriendHandler.
func NewFriendHandler(friendSvc friendServicer) *FriendHandler {
	return &FriendHandler{friends: friendSvc}
}

// Send creates a request from the authenticated user to the form recipient_id.
func (h *FriendHandler) Send(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	recipientID, err := uuid.Parse(r.PostForm.Get("recipient_id"))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	request, err := h.friends.Send(r.Context(), user.ID, recipientID)
	if err != nil {
		friendError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(request); err != nil {
		slog.ErrorContext(r.Context(), "encode friend request", "error", err)
	}
}

// Accept accepts a request on behalf of its authenticated recipient.
func (h *FriendHandler) Accept(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, h.friends.Accept)
}

// Reject rejects a request on behalf of its authenticated recipient.
func (h *FriendHandler) Reject(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, h.friends.Reject)
}

func (h *FriendHandler) decide(w http.ResponseWriter, r *http.Request, decide func(context.Context, uuid.UUID, uuid.UUID) error) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	requestID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := decide(r.Context(), user.ID, requestID); err != nil {
		friendError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListFriends returns the authenticated user's friend IDs as JSON.
func (h *FriendHandler) ListFriends(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	friends, err := h.friends.ListFriends(r.Context(), user.ID)
	if err != nil {
		friendError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(friends); err != nil {
		slog.ErrorContext(r.Context(), "encode friends", "error", err)
	}
}

func friendError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrNotRecipient), errors.Is(err, app.ErrForbidden), errors.Is(err, app.ErrNoSuchRequest):
		// Mask ownership failures so they do not disclose a request's existence.
		http.Error(w, "Not Found", http.StatusNotFound)
	case errors.Is(err, app.ErrAlreadyFriends), errors.Is(err, app.ErrRequestPending), errors.Is(err, app.ErrCooldownActive):
		http.Error(w, "Conflict", http.StatusConflict)
	default:
		slog.ErrorContext(r.Context(), "friend operation", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
