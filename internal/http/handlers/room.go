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
	Run(ctx context.Context, raceID uuid.UUID) error
}

// raceResumer lists races left unfinished, e.g. by a restart, so their Run
// can be relaunched.
type raceResumer interface {
	UnfinishedRaces(ctx context.Context) ([]uuid.UUID, error)
}

var (
	_ raceBeginner = (*app.RaceService)(nil)
	_ raceResumer  = (*app.RaceService)(nil)
)

// roomUpdatedEvent is the HX-Trigger event that refreshes an open lobby.
const roomUpdatedEvent = "room-updated"

func lobbyPath(roomID uuid.UUID) string { return "/rooms/" + roomID.String() + "/lobby" }
func racePath(roomID uuid.UUID) string  { return "/rooms/" + roomID.String() + "/race" }

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

// ResumeRaces relaunches Run, on the handler's lifetime, for every race left
// in progress when the server last stopped, so those races still finalize.
// It does nothing when the beginner cannot list unfinished races.
func (h *RoomHandler) ResumeRaces() error {
	resumer, ok := h.beginner.(raceResumer)
	if !ok {
		return nil
	}
	ids, err := resumer.UnfinishedRaces(h.ctx)
	if err != nil {
		return err
	}
	for _, raceID := range ids {
		h.background.Add(1)
		go func() {
			defer h.background.Done()
			h.runRace(h.ctx, uuid.Nil, raceID)
		}()
	}
	return nil
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
	// HTMX forms follow the redirect to the new room's lobby.
	w.Header().Set("HX-Redirect", lobbyPath(roomID))
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
	code := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("join_code")))
	if code == "" {
		http.Error(w, "Join code is required", http.StatusBadRequest)
		return
	}
	roomID, err := h.room.JoinRoom(r.Context(), user.ID, code)
	if roomID != uuid.Nil {
		// Joining, or rejoining a room already joined, leads to its lobby.
		w.Header().Set("HX-Redirect", lobbyPath(roomID))
	}
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
	w.Header().Set("HX-Redirect", "/dashboard")
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
	w.Header().Set("HX-Trigger", roomUpdatedEvent)
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
	w.Header().Set("HX-Trigger", roomUpdatedEvent)
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
	// The host goes straight to the race page; guests follow from the lobby.
	w.Header().Set("HX-Redirect", racePath(roomID))
	w.WriteHeader(http.StatusNoContent)
}

func (h *RoomHandler) beginRace(ctx context.Context, roomID uuid.UUID) {
	race, err := h.beginner.Begin(ctx, roomID)
	if err != nil {
		if errors.Is(err, app.ErrNoRacersReady) {
			// Begin dissolved the room; there is no lobby to return to.
			slog.InfoContext(ctx, "race abandoned: no racer was ready", "room_id", roomID)
			return
		}
		slog.ErrorContext(ctx, "race begin failed", "room_id", roomID, "error", err)
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resetCountdownTimeout)
		defer cancel()
		if err := h.room.ResetCountdown(resetCtx, roomID); err != nil {
			slog.ErrorContext(resetCtx, "room countdown reset failed", "room_id", roomID, "error", err)
		}
		return
	}
	h.runRace(ctx, roomID, race.ID)
}

// runRace watches a race until it is finalized or ctx ends.
func (h *RoomHandler) runRace(ctx context.Context, roomID, raceID uuid.UUID) {
	if err := h.beginner.Run(ctx, raceID); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "race run failed", "room_id", roomID, "race_id", raceID, "error", err)
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
