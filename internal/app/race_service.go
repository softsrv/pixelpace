package app

import (
	"context"
	"encoding/json"
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

// RaceServiceConfig controls the countdown and race clock. Zero durations use
// defaults of 10 seconds for countdown and 30 seconds for staleness. Sleep must
// wait for the requested duration or return ctx.Err(); Now and Sleep default to
// real time. Injected functions must be safe for concurrent callers.
type RaceServiceConfig struct {
	CountdownDuration  time.Duration
	StalenessThreshold time.Duration
	Now                func() time.Time
	Sleep              func(context.Context, time.Duration) error
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

// Begin waits before atomically claiming a counting-down room and copying its
// participants. Competing Begin calls cannot create a second race for the room.
func (s *RaceService) Begin(ctx context.Context, roomID uuid.UUID) (db.Race, error) {
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
		StartedAt: pgtype.Timestamptz{Time: s.cfg.Now(), Valid: true},
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
