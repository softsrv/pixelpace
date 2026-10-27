package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/middleware"
)

// roomServicer defines the subset of app.RoomService that RoomHandler requires.
type roomServicer interface {
	CreateRoom(ctx context.Context, hostUserID, raceTypeID uuid.UUID) (uuid.UUID, string, error)
	JoinRoom(ctx context.Context, userID uuid.UUID, joinCode string) (uuid.UUID, error)
	Roster(ctx context.Context, callerUserID, roomID uuid.UUID) (app.RoomRoster, error)
	LeaveRoom(ctx context.Context, userID, roomID uuid.UUID) error
	KickParticipant(ctx context.Context, callerUserID, roomID, targetUserID uuid.UUID) error
	MarkReady(ctx context.Context, userID, roomID uuid.UUID) error
	StartRace(ctx context.Context, callerUserID, roomID uuid.UUID) error
	ResetCountdown(ctx context.Context, roomID uuid.UUID) error
}

var _ roomServicer = (*app.RoomService)(nil)

// raceBeginner runs a started room's countdown and creates its race.
type raceBeginner interface {
	Begin(ctx context.Context, roomID uuid.UUID) (db.Race, error)
}

var _ raceBeginner = (*app.RaceService)(nil)

// resetCountdownTimeout bounds returning a room to waiting after a failed begin.
const resetCountdownTimeout = 10 * time.Second

// RoomHandler groups the room lifecycle HTTP handlers.
type RoomHandler struct {
	ctx      context.Context
	room     roomServicer
	beginner raceBeginner
	// background tracks race begins still running after their start request.
	background sync.WaitGroup
}

// NewRoomHandler constructs a RoomHandler. Room responses do not render templates
// or set cookies, so renderer and secure are unused.
func NewRoomHandler(ctx context.Context, roomSvc roomServicer, beginner raceBeginner, _ *TemplateRenderer, _ bool) *RoomHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	return &RoomHandler{ctx: ctx, room: roomSvc, beginner: beginner}
}

// Wait blocks until every race begin launched by Start has finished.
func (h *RoomHandler) Wait() {
	h.background.Wait()
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
	roomID, code, err := h.room.CreateRoom(r.Context(), user.ID, raceTypeID)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"room_id": roomID.String(), "join_code": code})
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
	roomID, err := h.room.JoinRoom(r.Context(), user.ID, code)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"room_id": roomID.String(), "join_code": code})
}

// Roster serves the room's participants to a member; a non-member gets 404.
func (h *RoomHandler) Roster(w http.ResponseWriter, r *http.Request) {
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
	roster, err := h.room.Roster(r.Context(), user.ID, roomID)
	if err != nil {
		writeRoomError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(roster)
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
	// The countdown outlives the request, so begin on a context the response
	// does not cancel. It is cancelled when the handler's lifetime ends.
	// A failed begin returns the room to waiting.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	stop := context.AfterFunc(h.ctx, cancel)
	h.background.Add(1)
	go func() {
		defer h.background.Done()
		defer stop()
		h.beginRace(ctx, roomID)
	}()
	w.WriteHeader(http.StatusNoContent)
}

func (h *RoomHandler) beginRace(ctx context.Context, roomID uuid.UUID) {
	if _, err := h.beginner.Begin(ctx, roomID); err != nil {
		slog.ErrorContext(ctx, "race begin failed", "room_id", roomID, "error", err)
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resetCountdownTimeout)
		defer cancel()
		if err := h.room.ResetCountdown(resetCtx, roomID); err != nil {
			slog.ErrorContext(resetCtx, "room countdown reset failed", "room_id", roomID, "error", err)
		}
	}
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
