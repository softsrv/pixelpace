package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
	"github.com/softsrv/starter/internal/realtime"
)

type raceIngester interface {
	Ingest(ctx context.Context, raceID, userID uuid.UUID, sample app.TelemetrySample) error
}

type raceBroadcaster interface {
	Subscribe(ctx context.Context, raceID string) (<-chan []byte, error)
}

var _ raceIngester = (*app.RaceService)(nil)
var _ raceBroadcaster = realtime.Broadcaster(nil)

type connKey struct {
	userID uuid.UUID
	raceID uuid.UUID
}

// RaceWSHandler connects authenticated sockets to telemetry ingest and fan-out.
// The registry enforces one connection per racer and race in this process.
type RaceWSHandler struct {
	ingester    raceIngester
	broadcaster raceBroadcaster
	mu          sync.Mutex
	active      map[connKey]struct{}
}

func NewRaceWSHandler(ingester raceIngester, broadcaster raceBroadcaster) *RaceWSHandler {
	return &RaceWSHandler{ingester: ingester, broadcaster: broadcaster, active: make(map[connKey]struct{})}
}

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
	key := connKey{userID: user.ID, raceID: raceID}
	h.mu.Lock()
	if _, exists := h.active[key]; exists {
		h.mu.Unlock()
		http.Error(w, "Connection already active", http.StatusConflict)
		return
	}
	h.active[key] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.active, key)
		h.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// ADR-0001 approves a maintained WebSocket library instead of hand-rolled
	// framing. Keep its default same-origin protection for cookie authentication.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	payloads, err := h.broadcaster.Subscribe(ctx, raceID.String())
	if err != nil {
		return
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case payload, ok := <-payloads:
				if !ok {
					return
				}
				if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		_ = conn.CloseNow()
		<-writerDone
	}()

	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var sample app.TelemetrySample
		if err := json.Unmarshal(payload, &sample); err != nil {
			return
		}
		if err := h.ingester.Ingest(ctx, raceID, user.ID, sample); err != nil {
			// Ingest may have committed already; never retry it here.
			return
		}
	}
}
