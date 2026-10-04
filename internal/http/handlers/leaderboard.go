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

type leaderboardServicer interface {
	Leaderboard(context.Context, string, string, uuid.UUID, uuid.UUID) ([]db.LeaderboardPersonalBestRow, error)
}

// LeaderboardHandler serves personal-best rankings.
type LeaderboardHandler struct {
	leaderboard leaderboardServicer
}

// NewLeaderboardHandler constructs a LeaderboardHandler.
func NewLeaderboardHandler(svc leaderboardServicer) *LeaderboardHandler {
	return &LeaderboardHandler{leaderboard: svc}
}

// Leaderboard serves JSON for race_type, scope (global/friends), and window
// (all-time/month/week). Scope and window default to global and all-time.
func (h *LeaderboardHandler) Leaderboard(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	query := r.URL.Query()
	raceTypeID, err := uuid.Parse(query.Get("race_type"))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	scope, window := query.Get("scope"), query.Get("window")
	if scope == "" {
		scope = "global"
	}
	if window == "" {
		window = "all-time"
	}
	rows, err := h.leaderboard.Leaderboard(r.Context(), scope, window, raceTypeID, user.ID)
	if err != nil {
		if errors.Is(err, app.ErrInvalidLeaderboardFilter) {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		slog.ErrorContext(r.Context(), "leaderboard", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rows); err != nil {
		slog.ErrorContext(r.Context(), "encode leaderboard", "error", err)
	}
}
