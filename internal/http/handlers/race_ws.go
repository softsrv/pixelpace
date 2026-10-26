package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
	"github.com/softsrv/starter/internal/realtime"
)

type telemetryIngester interface {
	Ingest(ctx context.Context, raceID, userID uuid.UUID, sample app.TelemetrySample) error
}

// raceParticipation decides who may open a race's live channel and names the
// race's participants in the roster frame each connection receives first.
type raceParticipation interface {
	IsParticipant(ctx context.Context, raceID, userID uuid.UUID) (bool, error)
	Roster(ctx context.Context, raceID uuid.UUID) ([]app.PublicPlayer, error)
}

// rosterFrame is the first frame on every race connection, so clients can put
// names beside each other's telemetry and standings.
type rosterFrame struct {
	Type    string             `json:"type"`
	Players []app.PublicPlayer `json:"players"`
}

var (
	_ telemetryIngester = (*app.RaceService)(nil)
	_ raceParticipation = (*app.RaceService)(nil)
)

type connKey struct {
	raceID  uuid.UUID
	racerID uuid.UUID
}

// RaceWSHandler transports telemetry without owning race business logic.
type RaceWSHandler struct {
	ingester     telemetryIngester
	participants raceParticipation
	broadcaster  realtime.Broadcaster
	mu           sync.Mutex
	connections  map[connKey]struct{}
}

func NewRaceWSHandler(ingester telemetryIngester, participants raceParticipation, broadcaster realtime.Broadcaster) *RaceWSHandler {
	return &RaceWSHandler{
		ingester: ingester, participants: participants, broadcaster: broadcaster,
		connections: make(map[connKey]struct{}),
	}
}

// Serve accepts one connection per authenticated racer and race. Only the race's
// participants are upgraded; anyone else is refused before the connection slot,
// the subscription or any telemetry exists.
func (h *RaceWSHandler) Serve(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	raceID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid race ID", http.StatusBadRequest)
		return
	}
	member, err := h.participants.IsParticipant(r.Context(), raceID, user.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "race participation check failed", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if !member {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	key := connKey{raceID: raceID, racerID: user.ID}
	h.mu.Lock()
	_, exists := h.connections[key]
	if !exists {
		h.connections[key] = struct{}{}
	}
	h.mu.Unlock()
	if exists {
		http.Error(w, "Race connection already active", http.StatusConflict)
		return
	}
	defer func() {
		h.mu.Lock()
		delete(h.connections, key)
		h.mu.Unlock()
	}()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub, err := h.broadcaster.Subscribe(ctx, raceID.String())
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "Subscription unavailable")
		return
	}
	// The roster is written before either pump starts, so it precedes any
	// telemetry published once the subscription exists.
	players, err := h.participants.Roster(ctx, raceID)
	if err != nil {
		slog.ErrorContext(ctx, "race roster failed", "error", err)
		_ = conn.Close(websocket.StatusInternalError, "Roster unavailable")
		return
	}
	frame, err := json.Marshal(rosterFrame{Type: "roster", Players: players})
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "Roster unavailable")
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		return
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		h.readTelemetry(ctx, conn, key)
	}()
	h.writeTelemetry(ctx, conn, sub)
	cancel()
	// Do not release the connection slot until both pumps have stopped.
	<-readDone
}

func (h *RaceWSHandler) readTelemetry(ctx context.Context, conn *websocket.Conn, key connKey) {
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var sample app.TelemetrySample
		if err := json.Unmarshal(payload, &sample); err != nil {
			continue
		}
		if err := h.ingester.Ingest(ctx, key.raceID, key.racerID, sample); err != nil {
			// Ingest may have committed before publication failed; never retry it.
			_ = conn.Close(websocket.StatusInternalError, "Telemetry unavailable")
			return
		}
	}
}

func (h *RaceWSHandler) writeTelemetry(ctx context.Context, conn *websocket.Conn, sub <-chan []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-sub:
			if !ok {
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
				return
			}
		}
	}
}
