package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
)

// quickMatchServicer is the subset needed by the HTTP endpoints.
type quickMatchServicer interface {
	Enqueue(context.Context, uuid.UUID, uuid.UUID) error
	Leave(context.Context, uuid.UUID, uuid.UUID) error
	Match(context.Context, uuid.UUID, uuid.UUID) (string, error)
}

var _ quickMatchServicer = (*app.QuickMatchService)(nil)

type QuickMatchHandler struct {
	quickMatch quickMatchServicer
}

func NewQuickMatchHandler(svc quickMatchServicer) *QuickMatchHandler {
	return &QuickMatchHandler{quickMatch: svc}
}

func (h *QuickMatchHandler) Enqueue(w http.ResponseWriter, r *http.Request) {
	userID, raceTypeID, ok := quickMatchIdentity(w, r)
	if !ok {
		return
	}
	if err := h.quickMatch.Enqueue(r.Context(), userID, raceTypeID); err != nil {
		writeQuickMatchError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *QuickMatchHandler) Leave(w http.ResponseWriter, r *http.Request) {
	userID, raceTypeID, ok := quickMatchIdentity(w, r)
	if !ok {
		return
	}
	if err := h.quickMatch.Leave(r.Context(), userID, raceTypeID); err != nil {
		writeQuickMatchError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Match is a client-driven retry: 202 while waiting, 201 with a room join code.
func (h *QuickMatchHandler) Match(w http.ResponseWriter, r *http.Request) {
	userID, raceTypeID, ok := quickMatchIdentity(w, r)
	if !ok {
		return
	}
	code, err := h.quickMatch.Match(r.Context(), userID, raceTypeID)
	if err != nil {
		writeQuickMatchError(w, r, err)
		return
	}
	if code == "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"join_code": code})
}

func quickMatchIdentity(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	raceTypeID, err := uuid.Parse(r.PostForm.Get("race_type_id"))
	if err != nil {
		http.Error(w, "Invalid race type ID", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	return user.ID, raceTypeID, true
}

func writeQuickMatchError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, app.ErrNotQueued):
		status = http.StatusNotFound
	case errors.Is(err, app.ErrAlreadyQueued), errors.Is(err, app.ErrInvalidRaceType),
		errors.Is(err, app.ErrRoomFull), errors.Is(err, app.ErrAlreadyParticipant),
		errors.Is(err, app.ErrRoomNotJoinable):
		status = http.StatusConflict
	}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "quick match operation failed", "error", err)
	}
	http.Error(w, http.StatusText(status), status)
}
