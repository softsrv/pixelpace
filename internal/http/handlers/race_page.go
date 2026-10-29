package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
)

type roomRaceViewer interface {
	RoomRace(ctx context.Context, roomID, userID uuid.UUID) (app.RoomRace, error)
	MarkEquipmentReady(ctx context.Context, roomID, userID uuid.UUID) error
	Results(ctx context.Context, roomID, userID uuid.UUID) (app.RaceResults, error)
}

var _ roomRaceViewer = (*app.RaceService)(nil)

// RaceLane is one participant's lane on the race page, labelled with their
// public name.
type RaceLane struct {
	UserID uuid.UUID
	Label  string
	IsSelf bool
}

// RaceStatus is what the race page polls for: who is still connecting, when
// the race starts and, once it has, the race to stream telemetry to.
// ServerTime lets the page align its countdown with the server clock.
// Finished is true once the race has ended, so a reloaded page can show the
// results instead of waiting for a start.
type RaceStatus struct {
	RaceID       *uuid.UUID      `json:"race_id"`
	RaceStartsAt *time.Time      `json:"race_starts_at"`
	ServerTime   time.Time       `json:"server_time"`
	Racers       []app.RaceRacer `json:"racers"`
	Finished     bool            `json:"finished"`
}

// RacePageHandler renders the race view, collects each racer's equipment
// readiness and reports the race's progress toward its start.
type RacePageHandler struct {
	races    roomRaceViewer
	renderer *TemplateRenderer
	devMode  bool
	now      func() time.Time
}

// NewRacePageHandler constructs a RacePageHandler.
func NewRacePageHandler(races roomRaceViewer, renderer *TemplateRenderer, devMode bool) *RacePageHandler {
	return &RacePageHandler{races: races, renderer: renderer, devMode: devMode, now: time.Now}
}

// Page renders the race track for a participant of a started room. A room
// still in its lobby sends the participant back there.
func (h *RacePageHandler) Page(w http.ResponseWriter, r *http.Request) {
	userID, race, ok := h.roomRace(w, r, true)
	if !ok {
		return
	}
	lanes := make([]RaceLane, len(race.Racers))
	for i, racer := range race.Racers {
		lanes[i] = RaceLane{UserID: racer.UserID, Label: racer.Name, IsSelf: racer.UserID == userID}
	}
	user, _ := middleware.UserFromContext(r.Context())
	h.renderer.Page(w, http.StatusOK, "race.html", map[string]any{
		"User":    user,
		"DevMode": h.devMode,
		"Race":    race,
		"Lanes":   lanes,
	})
}

// Status reports the lineup's equipment readiness, the scheduled start once
// every racer is confirmed, and the race ID once the countdown has finished.
func (h *RacePageHandler) Status(w http.ResponseWriter, r *http.Request) {
	_, race, ok := h.roomRace(w, r, false)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(RaceStatus{
		RaceID: race.RaceID, RaceStartsAt: race.RaceStartsAt, ServerTime: h.now(), Racers: race.Racers,
		Finished: race.Finished,
	})
}

// Results reports every participant's outcome of the room's race, by public
// name. A racer dropped before the race gets 404 and a room with no race yet
// gets 409.
func (h *RacePageHandler) Results(w http.ResponseWriter, r *http.Request) {
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
	results, err := h.races.Results(r.Context(), roomID, user.ID)
	if err != nil {
		writeRaceError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(results)
}

// Ready records that the caller's race page has loaded and their rowing
// machine is connected.
func (h *RacePageHandler) Ready(w http.ResponseWriter, r *http.Request) {
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
	if err := h.races.MarkEquipmentReady(r.Context(), roomID, user.ID); err != nil {
		writeRaceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// roomRace loads the caller's view of the room's race. When toLobby is set, a
// room that has not started redirects to its lobby instead of failing.
func (h *RacePageHandler) roomRace(w http.ResponseWriter, r *http.Request, toLobby bool) (uuid.UUID, app.RoomRace, bool) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return uuid.Nil, app.RoomRace{}, false
	}
	roomID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid room ID", http.StatusBadRequest)
		return uuid.Nil, app.RoomRace{}, false
	}
	race, err := h.races.RoomRace(r.Context(), roomID, user.ID)
	if err != nil {
		if toLobby && errors.Is(err, app.ErrRaceNotStarted) {
			http.Redirect(w, r, lobbyPath(roomID), http.StatusSeeOther)
			return uuid.Nil, app.RoomRace{}, false
		}
		if toLobby && errors.Is(err, app.ErrParticipantNotFound) {
			// A racer dropped before the start has no participant row, so
			// race.mjs never runs for them: tell them on the page itself.
			h.renderer.Page(w, http.StatusNotFound, "room-unavailable.html", map[string]any{
				"User": user, "Message": removedFromRaceMessage,
			})
			return uuid.Nil, app.RoomRace{}, false
		}
		writeRaceError(w, r, err)
		return uuid.Nil, app.RoomRace{}, false
	}
	return user.ID, race, true
}

// removedFromRaceMessage is what the race page shows someone who is not in
// the race. RoomRace cannot tell a racer dropped before the start apart from
// someone who never joined, so it covers both without saying more.
const removedFromRaceMessage = "You're not in this race. If your rowing machine wasn't connected in time, you were removed from it before the start."

func writeRaceError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	// Non-participants, including racers dropped for unready equipment, get
	// the same answer as a missing room.
	case errors.Is(err, app.ErrRoomNotFound), errors.Is(err, app.ErrParticipantNotFound):
		status = http.StatusNotFound
	case errors.Is(err, app.ErrRaceNotStarted), errors.Is(err, app.ErrInvalidRaceType):
		status = http.StatusConflict
	}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "room race request failed", "error", err)
	}
	http.Error(w, http.StatusText(status), status)
}
