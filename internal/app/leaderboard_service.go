package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
)

// ErrInvalidLeaderboardFilter indicates an unsupported scope or time window.
var ErrInvalidLeaderboardFilter = errors.New("invalid leaderboard filter")

type friendLister interface {
	ListFriends(context.Context, uuid.UUID) ([]uuid.UUID, error)
}

// LeaderboardServiceConfig holds the leaderboard clock.
type LeaderboardServiceConfig struct {
	// Now defaults to time.Now; injection allows deterministic boundary tests.
	Now func() time.Time
}

// LeaderboardService ranks racers by personal-best finished time.
type LeaderboardService struct {
	q       *db.Queries
	friends friendLister
	cfg     LeaderboardServiceConfig
}

// NewLeaderboardService constructs a LeaderboardService with its dependencies.
func NewLeaderboardService(q *db.Queries, friends friendLister, cfg LeaderboardServiceConfig) *LeaderboardService {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LeaderboardService{q: q, friends: friends, cfg: cfg}
}

// Leaderboard returns personal-best rows in ascending elapsed-time order.
// Windows use completion time: month is a trailing calendar month in UTC,
// week is seven days, and all-time has no lower bound. Friends includes only
// the caller's friends, not the caller, unless they are in that resolved set.
func (s *LeaderboardService) Leaderboard(ctx context.Context, scope, window string, raceTypeID, callerID uuid.UUID) ([]db.LeaderboardPersonalBestRow, error) {
	if scope != "global" && scope != "friends" {
		return nil, ErrInvalidLeaderboardFilter
	}
	params := db.LeaderboardPersonalBestParams{RaceTypeID: raceTypeID}
	switch window {
	case "all-time":
	case "month":
		params.Column2 = pgtype.Timestamptz{Time: s.cfg.Now().UTC().AddDate(0, -1, 0), Valid: true}
	case "week":
		params.Column2 = pgtype.Timestamptz{Time: s.cfg.Now().UTC().Add(-7 * 24 * time.Hour), Valid: true}
	default:
		return nil, ErrInvalidLeaderboardFilter
	}
	if scope == "friends" {
		friends, err := s.friends.ListFriends(ctx, callerID)
		if err != nil {
			return nil, fmt.Errorf("resolve leaderboard friends: %w", err)
		}
		// A nil array means Global in SQL; an empty friend set must stay empty.
		params.Column3 = append([]uuid.UUID{}, friends...)
	}
	rows, err := s.q.LeaderboardPersonalBest(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("leaderboard personal best: %w", err)
	}
	return rows, nil
}
