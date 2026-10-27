package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/realtime"
)

// RaceServiceConfig controls the equipment wait, countdown and race clock.
// Zero durations use defaults of 60 seconds for the equipment wait, polled
// every 500 milliseconds, 10 seconds for countdown and 30 seconds for
// staleness. Sleep must wait for the requested duration or return ctx.Err();
// Now and Sleep default to real time. Injected functions must be safe for
// concurrent callers.
type RaceServiceConfig struct {
	EquipmentTimeout      time.Duration
	EquipmentPollInterval time.Duration
	CountdownDuration     time.Duration
	StalenessThreshold    time.Duration
	Now                   func() time.Time
	Sleep                 func(context.Context, time.Duration) error
}

// TelemetrySample contains measurements shared by all telemetry sources.
// Race and participant identity are separate arguments to Ingest.
type TelemetrySample struct {
	ElapsedMilliseconds int64     `json:"elapsed_milliseconds"`
	DistanceMillimeters int64     `json:"distance_millimeters"`
	StrokeRate          int32     `json:"stroke_rate"`
	Power               int32     `json:"power"`
	SampledAt           time.Time `json:"sampled_at"`
}

// RaceStanding contains latest measurements for live standings, or time to
// target for final standings. The first final standing is the winner.
type RaceStanding struct {
	UserID              uuid.UUID
	DistanceMillimeters int64
	ElapsedMilliseconds int64
}

// RaceService owns the countdown, telemetry and fixed-distance race lifecycle.
// Callers are responsible for authorizing race and participant access.
type RaceService struct {
	q           *db.Queries
	pool        pgxBeginner
	broadcaster realtime.Broadcaster
	cfg         RaceServiceConfig
}

func NewRaceService(q *db.Queries, pool pgxBeginner, broadcaster realtime.Broadcaster, cfg RaceServiceConfig) *RaceService {
	if cfg.EquipmentTimeout == 0 {
		cfg.EquipmentTimeout = 60 * time.Second
	}
	if cfg.EquipmentPollInterval == 0 {
		cfg.EquipmentPollInterval = 500 * time.Millisecond
	}
	if cfg.CountdownDuration == 0 {
		cfg.CountdownDuration = 10 * time.Second
	}
	if cfg.StalenessThreshold == 0 {
		cfg.StalenessThreshold = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepRaceCountdown
	}
	return &RaceService{q: q, pool: pool, broadcaster: broadcaster, cfg: cfg}
}

func sleepRaceCountdown(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Begin runs a started room up to the race. It waits until every participant's
// equipment is ready, or the equipment timeout passes, then drops whoever is
// still not ready and schedules the race to start one countdown later. When
// the countdown ends it atomically claims the room and copies its remaining
// participants. Competing Begin calls cannot create a second race for the room.
func (s *RaceService) Begin(ctx context.Context, roomID uuid.UUID) (db.Race, error) {
	if err := s.awaitEquipment(ctx, roomID); err != nil {
		return db.Race{}, err
	}
	startsAt, err := s.scheduleStart(ctx, roomID)
	if err != nil {
		return db.Race{}, err
	}
	if err := s.cfg.Sleep(ctx, s.cfg.CountdownDuration); err != nil {
		return db.Race{}, fmt.Errorf("race countdown: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Race{}, fmt.Errorf("begin race transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	room, err := q.BeginRaceRoom(ctx, roomID)
	if err != nil {
		return db.Race{}, fmt.Errorf("claim counting-down room: %w", err)
	}
	race, err := q.CreateRace(ctx, db.CreateRaceParams{
		ID: uuid.Must(uuid.NewV7()), RoomID: room.ID, RaceTypeID: room.RaceTypeID,
		StartedAt: pgtype.Timestamptz{Time: startsAt, Valid: true},
	})
	if err != nil {
		return db.Race{}, fmt.Errorf("create race: %w", err)
	}
	participants, err := q.ListRaceRoomParticipants(ctx, roomID)
	if err != nil {
		return db.Race{}, fmt.Errorf("list room participants: %w", err)
	}
	for _, userID := range participants {
		if err := q.CreateRaceParticipant(ctx, db.CreateRaceParticipantParams{
			ID: uuid.Must(uuid.NewV7()), RaceID: race.ID, UserID: userID,
		}); err != nil {
			return db.Race{}, fmt.Errorf("seed race participant: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Race{}, fmt.Errorf("commit race: %w", err)
	}
	return race, nil
}

// awaitEquipment polls until no participant is waiting on equipment or the
// equipment timeout has been spent sleeping.
func (s *RaceService) awaitEquipment(ctx context.Context, roomID uuid.UUID) error {
	for waited := time.Duration(0); ; waited += s.cfg.EquipmentPollInterval {
		unready, err := s.q.CountRoomEquipmentUnready(ctx, roomID)
		if err != nil {
			return fmt.Errorf("count equipment unready: %w", err)
		}
		if unready == 0 || waited >= s.cfg.EquipmentTimeout {
			return nil
		}
		if err := s.cfg.Sleep(ctx, s.cfg.EquipmentPollInterval); err != nil {
			return fmt.Errorf("await equipment: %w", err)
		}
	}
}

// scheduleStart removes participants whose equipment is still not ready,
// handing off the host if needed, and records when the race starts. A room
// left with nobody is dissolved and reported as ErrNoRacersReady.
func (s *RaceService) scheduleStart(ctx context.Context, roomID uuid.UUID) (time.Time, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("begin schedule transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	room, err := q.GetRoomForUpdate(ctx, roomID)
	if err != nil {
		return time.Time{}, fmt.Errorf("lock room: %w", err)
	}
	if room.Status != "counting_down" {
		return time.Time{}, ErrInvalidStatusTransition
	}
	dropped, err := q.DeleteRoomEquipmentUnready(ctx, roomID)
	if err != nil {
		return time.Time{}, fmt.Errorf("drop unready participants: %w", err)
	}
	remaining, err := q.CountRoomParticipants(ctx, roomID)
	if err != nil {
		return time.Time{}, fmt.Errorf("count participants: %w", err)
	}
	if remaining == 0 {
		if err := q.DissolveRoom(ctx, roomID); err != nil {
			return time.Time{}, fmt.Errorf("dissolve room: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return time.Time{}, fmt.Errorf("commit dissolve: %w", err)
		}
		return time.Time{}, ErrNoRacersReady
	}
	for _, userID := range dropped {
		if userID != room.HostUserID {
			continue
		}
		hostID, err := q.GetRemainingRoomParticipant(ctx, roomID)
		if err != nil {
			return time.Time{}, fmt.Errorf("get remaining participant: %w", err)
		}
		if err := q.ReassignRoomHost(ctx, db.ReassignRoomHostParams{ID: roomID, HostUserID: hostID}); err != nil {
			return time.Time{}, fmt.Errorf("reassign host: %w", err)
		}
	}
	startsAt := s.cfg.Now().Add(s.cfg.CountdownDuration)
	if err := q.SetRoomRaceStartsAt(ctx, db.SetRoomRaceStartsAtParams{
		ID: roomID, RaceStartsAt: pgtype.Timestamptz{Time: startsAt, Valid: true},
	}); err != nil {
		return time.Time{}, fmt.Errorf("set race start: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, fmt.Errorf("commit schedule: %w", err)
	}
	return startsAt, nil
}

// MarkEquipmentReady records that the participant's race page has loaded and
// their rowing machine is connected. Repeating it keeps the first time.
func (s *RaceService) MarkEquipmentReady(ctx context.Context, roomID, userID uuid.UUID) error {
	n, err := s.q.MarkRoomEquipmentReady(ctx, db.MarkRoomEquipmentReadyParams{RoomID: roomID, UserID: userID})
	if err != nil {
		return fmt.Errorf("mark equipment ready: %w", err)
	}
	if n > 0 {
		return nil
	}
	// Explain the miss the same way RoomRace would.
	_, err = s.RoomRace(ctx, roomID, userID)
	if err == nil {
		err = ErrRaceNotStarted
	}
	return err
}

// IsParticipant reports whether the user was seeded into the race when it began.
func (s *RaceService) IsParticipant(ctx context.Context, raceID, userID uuid.UUID) (bool, error) {
	ok, err := s.q.IsRaceParticipant(ctx, db.IsRaceParticipantParams{RaceID: raceID, UserID: userID})
	if err != nil {
		return false, fmt.Errorf("check race participant: %w", err)
	}
	return ok, nil
}

// Roster returns the public view of every participant seeded into the race,
// in seeding order. One query reads only each participant's id and name.
func (s *RaceService) Roster(ctx context.Context, raceID uuid.UUID) ([]PublicPlayer, error) {
	rows, err := s.q.ListRaceRoster(ctx, raceID)
	if err != nil {
		return nil, fmt.Errorf("list race roster: %w", err)
	}
	players := make([]PublicPlayer, 0, len(rows))
	for _, row := range rows {
		players = append(players, NewPublicPlayer(row.ID, row.DisplayName))
	}
	return players, nil
}

// RaceRacer is one racer in a room's lineup, as the other racers see them.
type RaceRacer struct {
	PublicPlayer
	EquipmentReady bool `json:"equipment_ready"`
}

// RoomRace is a participant's view of a room's race from the moment the host
// starts it. RaceStartsAt is nil while racers are still connecting their
// equipment; RaceID is nil until the countdown has ended and Begin has created
// the race. Racers are in the order Begin seeds race participants, so lanes
// stay stable throughout.
type RoomRace struct {
	RoomID            uuid.UUID
	RaceID            *uuid.UUID
	RaceStartsAt      *time.Time
	Label             string
	TargetMillimeters int64
	Racers            []RaceRacer
}

// RoomRace returns the race view for a room participant. Rooms that are still
// waiting or were dissolved report ErrRaceNotStarted.
func (s *RaceService) RoomRace(ctx context.Context, roomID, userID uuid.UUID) (RoomRace, error) {
	row, err := s.q.GetRoomRace(ctx, roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RoomRace{}, ErrRoomNotFound
		}
		return RoomRace{}, fmt.Errorf("get room race: %w", err)
	}
	isParticipant, err := s.q.RoomParticipantExists(ctx, db.RoomParticipantExistsParams{RoomID: roomID, UserID: userID})
	if err != nil {
		return RoomRace{}, fmt.Errorf("check room participant: %w", err)
	}
	if !isParticipant {
		return RoomRace{}, ErrParticipantNotFound
	}
	switch row.Status {
	case "counting_down", "in_progress", "finished":
	default:
		return RoomRace{}, ErrRaceNotStarted
	}
	if row.Kind != "distance" {
		return RoomRace{}, ErrInvalidRaceType
	}
	lineup, err := s.q.ListRoomLineup(ctx, roomID)
	if err != nil {
		return RoomRace{}, fmt.Errorf("list room lineup: %w", err)
	}
	view := RoomRace{RoomID: roomID, Label: row.Label, TargetMillimeters: row.TargetValue, Racers: make([]RaceRacer, 0, len(lineup))}
	for _, racer := range lineup {
		view.Racers = append(view.Racers, RaceRacer{
			PublicPlayer: NewPublicPlayer(racer.ID, racer.DisplayName), EquipmentReady: racer.EquipmentReady,
		})
	}
	if row.RaceStartsAt.Valid {
		startsAt := row.RaceStartsAt.Time
		view.RaceStartsAt = &startsAt
	}
	if row.RaceID.Valid {
		raceID := uuid.UUID(row.RaceID.Bytes)
		view.RaceID = &raceID
	}
	return view, nil
}

// Ingest stores each sample, including zero measurements, and publishes it once
// after commit. A publish error means the sample is stored but delivery failed;
// callers must not retry the entire ingest to retry delivery.
func (s *RaceService) Ingest(ctx context.Context, raceID, userID uuid.UUID, sample TelemetrySample) error {
	payload, err := json.Marshal(struct {
		UserID uuid.UUID `json:"user_id"`
		TelemetrySample
	}{UserID: userID, TelemetrySample: sample})
	if err != nil {
		return fmt.Errorf("encode telemetry: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin telemetry transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	// Serialize telemetry with DNF sweeps and finalization for this race.
	if _, err := q.LockRace(ctx, raceID); err != nil {
		return fmt.Errorf("lock telemetry race: %w", err)
	}
	if err := q.InsertRaceTelemetry(ctx, db.InsertRaceTelemetryParams{
		ID: uuid.Must(uuid.NewV7()), RaceID: raceID, UserID: userID,
		ElapsedMilliseconds: pgtype.Int8{Int64: sample.ElapsedMilliseconds, Valid: true},
		DistanceMillimeters: pgtype.Int8{Int64: sample.DistanceMillimeters, Valid: true},
		StrokeRate:          pgtype.Int4{Int32: sample.StrokeRate, Valid: true},
		Power:               pgtype.Int4{Int32: sample.Power, Valid: true},
		SampledAt:           pgtype.Timestamptz{Time: sample.SampledAt, Valid: true},
	}); err != nil {
		return fmt.Errorf("insert telemetry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit telemetry: %w", err)
	}
	if err := s.broadcaster.Publish(ctx, raceID.String(), payload); err != nil {
		return fmt.Errorf("publish stored telemetry: %w", err)
	}
	return nil
}

// LiveRanking uses the latest sampled_at (then sample ID) rather than the
// largest historical distance or arrival order. DNF participants are excluded.
func (s *RaceService) LiveRanking(ctx context.Context, raceID uuid.UUID) ([]RaceStanding, error) {
	return s.rank(ctx, raceID, false, false)
}

// Winner returns the first participant to reach the target by elapsed race
// time, independently of sample arrival order. No finisher returns pgx.ErrNoRows.
func (s *RaceService) Winner(ctx context.Context, raceID uuid.UUID) (uuid.UUID, error) {
	standings, err := s.rank(ctx, raceID, true, false)
	if err != nil {
		return uuid.Nil, err
	}
	if len(standings) == 0 {
		return uuid.Nil, pgx.ErrNoRows
	}
	return standings[0].UserID, nil
}

// Finalize records finish times atomically and returns finishers ordered by
// their first time to target. Nonfinishers are marked DNF; telemetry is retained.
func (s *RaceService) Finalize(ctx context.Context, raceID uuid.UUID) ([]RaceStanding, error) {
	return s.rank(ctx, raceID, true, true)
}

func (s *RaceService) rank(ctx context.Context, raceID uuid.UUID, final, finalize bool) ([]RaceStanding, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin standings transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	race, err := q.LockRace(ctx, raceID)
	if err != nil {
		return nil, fmt.Errorf("lock standings race: %w", err)
	}
	if race.Kind != "distance" {
		return nil, fmt.Errorf("standings require a distance race, got %q", race.Kind)
	}
	rows, err := q.ListRaceStandings(ctx, db.ListRaceStandingsParams{
		RaceID: raceID, DistanceMillimeters: pgtype.Int8{Int64: race.TargetValue, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list race standings: %w", err)
	}
	now := s.cfg.Now()
	standings := make([]RaceStanding, 0, len(rows))
	for _, row := range rows {
		if row.Status == "dnf" {
			continue
		}
		lastSeen := race.StartedAt.Time
		if row.SampledAt.Valid {
			lastSeen = row.SampledAt.Time
		}
		if row.Status == "racing" && (now.Sub(lastSeen) > s.cfg.StalenessThreshold || (finalize && !row.TargetElapsedMilliseconds.Valid)) {
			if err := q.MarkRaceParticipantDNF(ctx, row.ID); err != nil {
				return nil, fmt.Errorf("mark stale participant: %w", err)
			}
			continue
		}
		standing := RaceStanding{UserID: row.UserID, DistanceMillimeters: row.DistanceMillimeters.Int64, ElapsedMilliseconds: row.ElapsedMilliseconds.Int64}
		if final {
			if !row.TargetElapsedMilliseconds.Valid {
				continue
			}
			standing.DistanceMillimeters = race.TargetValue
			standing.ElapsedMilliseconds = row.TargetElapsedMilliseconds.Int64
		}
		if finalize {
			if standing.ElapsedMilliseconds < 0 || standing.ElapsedMilliseconds > math.MaxInt32 {
				return nil, fmt.Errorf("finish elapsed time outside database integer range: %d", standing.ElapsedMilliseconds)
			}
			if err := q.FinishRaceParticipant(ctx, db.FinishRaceParticipantParams{
				ID: row.ID, ElapsedMilliseconds: pgtype.Int4{Int32: int32(standing.ElapsedMilliseconds), Valid: true},
				FinishedAt: row.TargetSampledAt,
			}); err != nil {
				return nil, fmt.Errorf("finish participant: %w", err)
			}
		}
		standings = append(standings, standing)
	}
	sort.SliceStable(standings, func(i, j int) bool {
		if !final && standings[i].DistanceMillimeters != standings[j].DistanceMillimeters {
			return standings[i].DistanceMillimeters > standings[j].DistanceMillimeters
		}
		return standings[i].ElapsedMilliseconds < standings[j].ElapsedMilliseconds
	})
	if finalize {
		if err := q.FinishRace(ctx, db.FinishRaceParams{ID: raceID, FinishedAt: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
			return nil, fmt.Errorf("finish race: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit standings: %w", err)
	}
	return standings, nil
}
