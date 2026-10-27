package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/softsrv/starter/internal/db"
)

const (
	joinCodeAlphabet    = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	joinCodeLength      = 6
	maxRoomParticipants = 8
	maxJoinCodeAttempts = 10
)

// RoomService manages the lobby lifecycle through the start of a race.
type RoomService struct {
	q           *db.Queries
	pool        pgxBeginner
	newJoinCode func() (string, error)
}

// NewRoomService constructs a RoomService.
func NewRoomService(q *db.Queries, pool pgxBeginner) *RoomService {
	return &RoomService{q: q, pool: pool, newJoinCode: generateJoinCode}
}

func generateJoinCode() (string, error) {
	code := make([]byte, joinCodeLength)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(joinCodeAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generate join code: %w", err)
		}
		code[i] = joinCodeAlphabet[n.Int64()]
	}
	return string(code), nil
}

// CreateRoom atomically creates a waiting room and its host participant. It
// returns the room id, which addresses the roster, alongside the join code.
func (s *RoomService) CreateRoom(ctx context.Context, hostUserID, raceTypeID uuid.UUID) (uuid.UUID, string, error) {
	if _, err := s.q.GetRaceType(ctx, raceTypeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", ErrInvalidRaceType
		}
		return uuid.Nil, "", fmt.Errorf("get race type: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxJoinCodeAttempts; attempt++ {
		code, err := s.newJoinCode()
		if err != nil {
			return uuid.Nil, "", fmt.Errorf("create room code: %w", err)
		}
		// A unique violation aborts a PostgreSQL transaction. Each attempt
		// therefore owns a fresh transaction, rolled back before retrying.
		roomID, retry, err := s.createRoomAttempt(ctx, hostUserID, raceTypeID, code)
		if err == nil {
			return roomID, code, nil
		}
		if !retry {
			return uuid.Nil, "", err
		}
		lastErr = err
	}
	return uuid.Nil, "", fmt.Errorf("create room after %d attempts: %w", maxJoinCodeAttempts, lastErr)
}

func (s *RoomService) createRoomAttempt(ctx context.Context, hostUserID, raceTypeID uuid.UUID, code string) (uuid.UUID, bool, error) {
	roomID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("generate room id: %w", err)
	}
	participantID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("generate participant id: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if err := qtx.InsertRoom(ctx, db.InsertRoomParams{
		ID: roomID, RaceTypeID: raceTypeID, HostUserID: hostUserID, JoinCode: code,
	}); err != nil {
		var pgErr *pgconn.PgError
		retry := errors.As(err, &pgErr) && pgErr.Code == "23505"
		return uuid.Nil, retry, fmt.Errorf("insert room: %w", err)
	}
	if err := qtx.InsertRoomParticipant(ctx, db.InsertRoomParticipantParams{
		ID: participantID, RoomID: roomID, UserID: hostUserID,
	}); err != nil {
		return uuid.Nil, false, fmt.Errorf("insert host participant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, fmt.Errorf("commit: %w", err)
	}
	return roomID, false, nil
}

// JoinRoom adds a not-ready participant to the active waiting room for a code
// and returns that room's id. ErrAlreadyParticipant also carries the room id.
func (s *RoomService) JoinRoom(ctx context.Context, userID uuid.UUID, joinCode string) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	room, err := qtx.GetActiveRoomByJoinCode(ctx, joinCode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrRoomNotFound
		}
		return uuid.Nil, fmt.Errorf("get active room: %w", err)
	}
	if room.Status != "waiting" {
		return uuid.Nil, ErrRoomNotJoinable
	}
	exists, err := qtx.RoomParticipantExists(ctx, db.RoomParticipantExistsParams{RoomID: room.ID, UserID: userID})
	if err != nil {
		return uuid.Nil, fmt.Errorf("check participant: %w", err)
	}
	if exists {
		// The room id lets a returning participant be sent back to the lobby.
		return room.ID, ErrAlreadyParticipant
	}
	count, err := qtx.CountRoomParticipants(ctx, room.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("count participants: %w", err)
	}
	if count >= maxRoomParticipants {
		return uuid.Nil, ErrRoomFull
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate participant id: %w", err)
	}
	if err := qtx.InsertRoomParticipant(ctx, db.InsertRoomParticipantParams{ID: id, RoomID: room.ID, UserID: userID}); err != nil {
		return uuid.Nil, fmt.Errorf("insert participant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit: %w", err)
	}
	return room.ID, nil
}

// LeaveRoom removes a participant, handing off the host or dissolving an empty room.
func (s *RoomService) LeaveRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if room.Status != "waiting" {
			return ErrRoomNotJoinable
		}
		if _, err := getRoomParticipant(ctx, qtx, roomID, userID); err != nil {
			return err
		}
		return removeRoomParticipant(ctx, qtx, room, userID)
	})
}

// KickParticipant permits only the host to remove a not-ready lobby participant.
func (s *RoomService) KickParticipant(ctx context.Context, callerUserID, roomID, targetUserID uuid.UUID) error {
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if callerUserID != room.HostUserID {
			return ErrNotHost
		}
		if room.Status != "waiting" {
			return ErrRoomNotJoinable
		}
		participant, err := getRoomParticipant(ctx, qtx, roomID, targetUserID)
		if err != nil {
			return err
		}
		if participant.Ready {
			return ErrParticipantReady
		}
		// Self-removal follows the same host handoff rules as leaving.
		return removeRoomParticipant(ctx, qtx, room, targetUserID)
	})
}

// MarkReady sets only the caller's own participant flag in a waiting room.
func (s *RoomService) MarkReady(ctx context.Context, userID, roomID uuid.UUID) error {
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if room.Status != "waiting" {
			return ErrRoomNotJoinable
		}
		if _, err := getRoomParticipant(ctx, qtx, roomID, userID); err != nil {
			return err
		}
		if err := qtx.MarkRoomParticipantReady(ctx, db.MarkRoomParticipantReadyParams{RoomID: roomID, UserID: userID}); err != nil {
			return fmt.Errorf("mark participant ready: %w", err)
		}
		return nil
	})
}

// StartRace transitions a lobby whose guests are all ready to counting_down.
// The host need not mark themselves ready. The caller owns the countdown.
func (s *RoomService) StartRace(ctx context.Context, callerUserID, roomID uuid.UUID) error {
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if callerUserID != room.HostUserID {
			return ErrNotHost
		}
		if room.Status != "waiting" {
			return ErrInvalidStatusTransition
		}
		count, err := qtx.CountRoomParticipants(ctx, roomID)
		if err != nil {
			return fmt.Errorf("count participants: %w", err)
		}
		if count < 2 {
			return ErrNotEnoughParticipants
		}
		if count > maxRoomParticipants {
			return ErrRoomFull
		}
		unready, err := qtx.RoomHasUnreadyGuests(ctx, db.RoomHasUnreadyGuestsParams{RoomID: roomID, UserID: room.HostUserID})
		if err != nil {
			return fmt.Errorf("check participant readiness: %w", err)
		}
		if unready {
			return ErrNotAllReady
		}
		if err := qtx.StartRoomCountdown(ctx, roomID); err != nil {
			return fmt.Errorf("start countdown: %w", err)
		}
		return nil
	})
}

// BeginRace advances only a counting-down room to in_progress.
func (s *RoomService) BeginRace(ctx context.Context, roomID uuid.UUID) error {
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if room.Status != "counting_down" {
			return ErrInvalidStatusTransition
		}
		if err := qtx.BeginRoomRace(ctx, roomID); err != nil {
			return fmt.Errorf("begin race: %w", err)
		}
		return nil
	})
}

// ResetCountdown returns a counting-down room to waiting after its race could
// not begin, so it is joinable and can be started again. A room in any other
// status is left unchanged and reported as ErrInvalidStatusTransition.
func (s *RoomService) ResetCountdown(ctx context.Context, roomID uuid.UUID) error {
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if room.Status != "counting_down" {
			return ErrInvalidStatusTransition
		}
		if err := qtx.ResetRoomCountdown(ctx, roomID); err != nil {
			return fmt.Errorf("reset countdown: %w", err)
		}
		if err := qtx.ResetRoomEquipmentReadiness(ctx, roomID); err != nil {
			return fmt.Errorf("reset equipment readiness: %w", err)
		}
		return nil
	})
}

// RoomRosterEntry is one participant as another participant sees it.
type RoomRosterEntry struct {
	PublicPlayer
	Ready bool `json:"ready"`
	Host  bool `json:"host"`
}

// RoomRoster lists a room's participants and, once begun, its race id.
type RoomRoster struct {
	Participants []RoomRosterEntry `json:"participants"`
	RaceID       *uuid.UUID        `json:"race_id"`
}

// Roster returns the room's participants to one of them. A caller who is not
// in the room gets ErrParticipantNotFound and no roster.
func (s *RoomService) Roster(ctx context.Context, callerUserID, roomID uuid.UUID) (RoomRoster, error) {
	var roster RoomRoster
	err := s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		rows, err := qtx.ListRoomRoster(ctx, roomID)
		if err != nil {
			return fmt.Errorf("list room roster: %w", err)
		}
		member := false
		entries := make([]RoomRosterEntry, 0, len(rows))
		for _, row := range rows {
			member = member || row.ID == callerUserID
			entries = append(entries, RoomRosterEntry{
				PublicPlayer: NewPublicPlayer(row.ID, row.DisplayName),
				Ready:        row.Ready,
				Host:         row.ID == room.HostUserID,
			})
		}
		if !member {
			return ErrParticipantNotFound
		}
		raceID, err := qtx.GetRoomRaceID(ctx, roomID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("get room race: %w", err)
		default:
			roster.RaceID = &raceID
		}
		roster.Participants = entries
		return nil
	})
	if err != nil {
		return RoomRoster{}, err
	}
	return roster, nil
}

// All lifecycle mutations lock the room first, serializing membership, readiness,
// and status checks even though the schema has no unique participant constraint.
func (s *RoomService) withLockedRoom(ctx context.Context, roomID uuid.UUID, fn func(*db.Queries, db.Room) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	room, err := qtx.GetRoomForUpdate(ctx, roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRoomNotFound
		}
		return fmt.Errorf("get room: %w", err)
	}
	if err := fn(qtx, room); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func getRoomParticipant(ctx context.Context, qtx *db.Queries, roomID, userID uuid.UUID) (db.RoomParticipant, error) {
	participant, err := qtx.GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: roomID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RoomParticipant{}, ErrParticipantNotFound
		}
		return db.RoomParticipant{}, fmt.Errorf("get participant: %w", err)
	}
	return participant, nil
}

func removeRoomParticipant(ctx context.Context, qtx *db.Queries, room db.Room, userID uuid.UUID) error {
	if err := qtx.DeleteRoomParticipant(ctx, db.DeleteRoomParticipantParams{RoomID: room.ID, UserID: userID}); err != nil {
		return fmt.Errorf("delete participant: %w", err)
	}
	count, err := qtx.CountRoomParticipants(ctx, room.ID)
	if err != nil {
		return fmt.Errorf("count remaining participants: %w", err)
	}
	if count == 0 {
		if err := qtx.DissolveRoom(ctx, room.ID); err != nil {
			return fmt.Errorf("dissolve room: %w", err)
		}
	} else if room.HostUserID == userID {
		hostID, err := qtx.GetRemainingRoomParticipant(ctx, room.ID)
		if err != nil {
			return fmt.Errorf("get remaining participant: %w", err)
		}
		if err := qtx.ReassignRoomHost(ctx, db.ReassignRoomHostParams{ID: room.ID, HostUserID: hostID}); err != nil {
			return fmt.Errorf("reassign host: %w", err)
		}
	}
	return nil
}
