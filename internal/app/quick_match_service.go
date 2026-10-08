package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/softsrv/starter/internal/db"
)

// QuickMatchServiceConfig supplies a clock and host lottery. Injected functions
// must be safe for concurrent callers; IntN must return a value in [0, n).
type QuickMatchServiceConfig struct {
	Now  func() time.Time
	IntN func(int) int
}

// QuickMatchService owns the durable queue and forms rooms using RoomService.
type QuickMatchService struct {
	q     *db.Queries
	pool  pgxBeginner
	rooms *RoomService
	cfg   QuickMatchServiceConfig
}

func NewQuickMatchService(q *db.Queries, pool pgxBeginner, rooms *RoomService, cfg QuickMatchServiceConfig) *QuickMatchService {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.IntN == nil {
		cfg.IntN = rand.IntN
	}
	return &QuickMatchService{q: q, pool: pool, rooms: rooms, cfg: cfg}
}

// Enqueue records a fresh search. A duplicate never resets the original wait.
func (s *QuickMatchService) Enqueue(ctx context.Context, userID, raceTypeID uuid.UUID) error {
	if _, err := s.q.GetRaceType(ctx, raceTypeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidRaceType
		}
		return fmt.Errorf("get race type: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate queue id: %w", err)
	}
	if err := s.q.EnqueueQuickMatch(ctx, db.EnqueueQuickMatchParams{ID: id, UserID: userID, RaceTypeID: raceTypeID}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrAlreadyQueued
		}
		return fmt.Errorf("enqueue quick match: %w", err)
	}
	return nil
}

func (s *QuickMatchService) Leave(ctx context.Context, userID, raceTypeID uuid.UUID) error {
	if err := s.q.LeaveQuickMatch(ctx, db.LeaveQuickMatchParams{UserID: userID, RaceTypeID: raceTypeID}); err != nil {
		return fmt.Errorf("leave quick match: %w", err)
	}
	return nil
}

// Match attempts to form a room containing the caller. An empty code means the
// caller is still waiting. Clients can retry this POST as the search widens.
// Queue row locks serialize competing matches and leaves across server instances.
func (s *QuickMatchService) Match(ctx context.Context, userID, raceTypeID uuid.UUID) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin quick match: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	waiting, err := q.ListWaitingByRaceType(ctx, raceTypeID)
	if err != nil {
		return "", fmt.Errorf("list waiting racers: %w", err)
	}
	racers := make([]quickMatchRacer, 0, len(waiting))
	found := false
	for _, entry := range waiting {
		finishes, err := q.RecentFinishesByRaceType(ctx, db.RecentFinishesByRaceTypeParams{RaceTypeID: raceTypeID, UserID: entry.UserID})
		if err != nil {
			return "", fmt.Errorf("recent finishes: %w", err)
		}
		average, rated := quickMatchRating(finishes)
		racers = append(racers, quickMatchRacer{userID: entry.UserID, joinedAt: entry.JoinedAt.Time, average: average, rated: rated})
		found = found || entry.UserID == userID
	}
	if !found {
		return "", ErrNotQueued
	}
	group := selectQuickMatch(racers, userID, s.cfg.Now())
	if len(group) == 0 {
		return "", nil
	}
	host := quickMatchHost(group, s.cfg.IntN)
	// pgx.Tx.Begin creates a savepoint. Rebind a copy of the existing service
	// so its unchanged CreateRoom/JoinRoom operations participate in this outer
	// transaction: a failed join or queue deletion cannot leave a partial room.
	rooms := *s.rooms
	rooms.q, rooms.pool = q, tx
	code, err := rooms.CreateRoom(ctx, host, raceTypeID)
	if err != nil {
		return "", fmt.Errorf("create quick match room: %w", err)
	}
	for _, racer := range group {
		if racer.userID != host {
			if err := rooms.JoinRoom(ctx, racer.userID, code); err != nil {
				return "", fmt.Errorf("join quick match room: %w", err)
			}
		}
	}
	for _, racer := range group {
		if err := q.LeaveQuickMatch(ctx, db.LeaveQuickMatchParams{UserID: racer.userID, RaceTypeID: raceTypeID}); err != nil {
			return "", fmt.Errorf("remove matched racer: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit quick match: %w", err)
	}
	return code, nil
}

type quickMatchRacer struct {
	userID   uuid.UUID
	joinedAt time.Time
	average  float64 // seconds; meaningful only when rated is true
	rated    bool
}

func quickMatchRating(finishes []db.RecentFinishesByRaceTypeRow) (float64, bool) {
	if len(finishes) > 5 {
		finishes = finishes[:5]
	}
	var total float64
	var count int
	for _, finish := range finishes {
		if finish.ElapsedMilliseconds.Valid {
			total += float64(finish.ElapsedMilliseconds.Int32) / 1000
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return total / float64(count), true
}

func windowWidth(elapsedWait time.Duration) float64 {
	switch {
	case elapsedWait >= 60*time.Second:
		return math.Inf(1)
	case elapsedWait >= 30*time.Second:
		return 20
	case elapsedWait >= 10*time.Second:
		return 10
	default:
		return 5
	}
}

func quickMatchCompatible(a, b quickMatchRacer, now time.Time) bool {
	if !a.rated || !b.rated {
		return a.rated == b.rated
	}
	gap := math.Abs(a.average - b.average)
	return gap <= windowWidth(now.Sub(a.joinedAt)) && gap <= windowWidth(now.Sub(b.joinedAt))
}

// Input is oldest first. The caller anchors the group; each additional racer
// must be compatible with every member, not merely with the anchor. The oldest
// selected join time starts this group's room-forming wait (survives restarts).
func selectQuickMatch(racers []quickMatchRacer, userID uuid.UUID, now time.Time) []quickMatchRacer {
	var group []quickMatchRacer
	for _, racer := range racers {
		if racer.userID == userID {
			group = append(group, racer)
			break
		}
	}
	if len(group) == 0 {
		return nil
	}
	oldest := group[0].joinedAt
	for _, racer := range racers {
		if racer.userID == userID {
			continue
		}
		compatible := true
		for _, member := range group {
			if !quickMatchCompatible(member, racer, now) {
				compatible = false
				break
			}
		}
		if compatible {
			group = append(group, racer)
			if racer.joinedAt.Before(oldest) {
				oldest = racer.joinedAt
			}
		}
		if len(group) == maxRoomParticipants {
			return group
		}
	}
	if len(group) >= 2 && now.Sub(oldest) >= 10*time.Second {
		return group
	}
	return nil
}

func quickMatchHost(group []quickMatchRacer, intN func(int) int) uuid.UUID {
	best := math.Inf(1)
	var eligible []uuid.UUID
	for _, racer := range group {
		if racer.rated && racer.average < best {
			best = racer.average
			eligible = eligible[:0]
		}
		if !racer.rated || racer.average == best {
			eligible = append(eligible, racer.userID)
		}
	}
	return eligible[intN(len(eligible))]
}
