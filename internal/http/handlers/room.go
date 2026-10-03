package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
)

// roomServicer defines the subset of app.RoomService that RoomHandler requires.
type roomServicer interface {
	CreateRoom(ctx context.Context, hostUserID, raceTypeID uuid.UUID) (string, error)
	JoinRoom(ctx context.Context, userID uuid.UUID, joinCode string) error
	LeaveRoom(ctx context.Context, userID, roomID uuid.UUID) error
	KickParticipant(ctx context.Context, callerUserID, roomID, targetUserID uuid.UUID) error
	MarkReady(ctx context.Context, userID, roomID uuid.UUID) error
	StartRace(ctx context.Context, callerUserID, roomID uuid.UUID) error
}

var _ roomServicer = (*app.RoomService)(nil)

// RoomHandler groups the room lifecycle HTTP handlers.
type RoomHandler struct {
	room roomServicer
}

// NewRoomHandler constructs a RoomHandler. Room responses do not render templates
// or set cookies, so renderer and secure are unused.
func NewRoomHandler(roomSvc roomServicer, _ *TemplateRenderer, _ bool) *RoomHandler {
	return &RoomHandler{room: roomSvc}
}

func (h *RoomHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	raceTypeID, err := uuid.Parse(r.PostForm.Get("race_type_id"))
	if err != nil {
		http.Error(w, "Invalid race type ID", http.StatusBadRequest)
		return
	}
	code, err := h.room.CreateRoom(r.Context(), user.ID, raceTypeID)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"join_code": code})
}

func (h *RoomHandler) Join(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.PostForm.Get("join_code"))
	if code == "" {
		http.Error(w, "Join code is required", http.StatusBadRequest)
		return
	}
	if err := h.room.JoinRoom(r.Context(), user.ID, code); err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RoomHandler) Leave(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	roomID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}
	if err := h.room.LeaveRoom(r.Context(), user.ID, roomID); err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RoomHandler) Kick(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	roomID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	targetID, err := uuid.Parse(r.PostForm.Get("target_user_id"))
	if err != nil {
		http.Error(w, "Invalid target user ID", http.StatusBadRequest)
		return
	}
	if err := h.room.KickParticipant(r.Context(), user.ID, roomID, targetID); err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RoomHandler) MarkReady(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	roomID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}
	if err := h.room.MarkReady(r.Context(), user.ID, roomID); err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RoomHandler) Start(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	roomID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return
	}
	if err := h.room.StartRace(r.Context(), user.ID, roomID); err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeRoomError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, app.ErrRoomNotFound), errors.Is(err, app.ErrParticipantNotFound):
		status = http.StatusNotFound
	case errors.Is(err, app.ErrNotHost):
		status = http.StatusForbidden
	case errors.Is(err, app.ErrRoomFull), errors.Is(err, app.ErrAlreadyParticipant),
		errors.Is(err, app.ErrNotAllReady), errors.Is(err, app.ErrInvalidStatusTransition),
		errors.Is(err, app.ErrRoomNotJoinable), errors.Is(err, app.ErrParticipantReady),
		errors.Is(err, app.ErrNotEnoughParticipants), errors.Is(err, app.ErrInvalidRaceType):
		status = http.StatusConflict
	}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "room operation failed", "error", err)
	}
	http.Error(w, http.StatusText(status), status)
}
