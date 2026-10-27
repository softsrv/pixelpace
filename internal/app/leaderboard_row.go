package app

import (
	"github.com/google/uuid"
)

// LeaderboardRow describes one ranked participant in the leaderboard.
// It deliberately has no email field: nothing private can ride along.
type LeaderboardRow struct {
	UserID              uuid.UUID `json:"user_id"`
	Name                string    `json:"name"`
	ElapsedMilliseconds int32     `json:"elapsed_milliseconds"`
}
