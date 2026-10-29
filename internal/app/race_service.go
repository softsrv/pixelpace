package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
// every 500 milliseconds, 10 seconds for countdown, 30 seconds for staleness
// and 1 second between Run's end-of-race checks. Sleep must wait for the
// requested duration or return ctx.Err(); Now and Sleep default to real time.
// Injected functions must be safe for concurrent callers.
type RaceServiceConfig struct {
	EquipmentTimeout      time.Duration
	EquipmentPollInterval time.Duration
	CountdownDuration     time.Duration
	StalenessThreshold    time.Duration
	RacePollInterval      time.Duration
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
	if cfg.RacePollInterval == 0 {
		cfg.RacePollInterval = time.Second
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

// RaceResult is one participant's line in a race's results. Finishers carry a
// 1-based Rank and their finish time; DNF racers carry neither. Distance is
// the target for a finisher and the latest sample's distance otherwise (0
// with no samples). Pace is milliseconds per 500m over that distance, nil
// when there is no distance or no time to measure it by.
type RaceResult struct {
	PublicPlayer
	Rank                    *int   `json:"rank"`
	Status                  string `json:"status"`
	ElapsedMilliseconds     *int64 `json:"elapsed_milliseconds"`
	DistanceMillimeters     int64  `json:"distance_millimeters"`
	PaceMillisecondsPer500m *int64 `json:"pace_milliseconds_per_500m"`
}

// RaceResults is a room's race outcome. Finished is true once the race has
// been finalized; until then racing participants are listed unranked.
type RaceResults struct {
	RaceID            uuid.UUID    `json:"race_id"`
	Finished          bool         `json:"finished"`
	TargetMillimeters int64        `json:"target_millimeters"`
	Results           []RaceResult `json:"results"`
}

// Results returns every participant of the room's race, for a room
// participant. Finishers come first by elapsed time, ranked 1..n; everyone
// else follows by distance covered, furthest first. Names are public names,
// never emails. A room with no race yet reports ErrRaceNotStarted.
func (s *RaceService) Results(ctx context.Context, roomID, userID uuid.UUID) (RaceResults, error) {
	room, err := s.q.GetRoomRace(ctx, roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RaceResults{}, ErrRoomNotFound
		}
		return RaceResults{}, fmt.Errorf("get room race: %w", err)
	}
	isParticipant, err := s.q.RoomParticipantExists(ctx, db.RoomParticipantExistsParams{RoomID: roomID, UserID: userID})
	if err != nil {
		return RaceResults{}, fmt.Errorf("check room participant: %w", err)
	}
	if !isParticipant {
		return RaceResults{}, ErrParticipantNotFound
	}
	if !room.RaceID.Valid {
		return RaceResults{}, ErrRaceNotStarted
	}
	raceID := uuid.UUID(room.RaceID.Bytes)
	rows, err := s.q.ListRaceResults(ctx, raceID)
	if err != nil {
		return RaceResults{}, fmt.Errorf("list race results: %w", err)
	}
	out := RaceResults{RaceID: raceID, Finished: room.Status == "finished", TargetMillimeters: room.TargetValue, Results: make([]RaceResult, 0, len(rows))}
	for _, row := range rows {
		result := RaceResult{PublicPlayer: NewPublicPlayer(row.UserID, row.DisplayName), Status: row.Status}
		var elapsed int64
		switch {
		case row.Status == "finished" && row.FinishElapsedMilliseconds.Valid:
			elapsed = int64(row.FinishElapsedMilliseconds.Int32)
			result.ElapsedMilliseconds = &elapsed
			result.DistanceMillimeters = room.TargetValue
		case row.DistanceMillimeters.Valid && row.SampledAt.Valid && row.StartedAt.Valid:
			result.DistanceMillimeters = row.DistanceMillimeters.Int64
			elapsed = row.SampledAt.Time.Sub(row.StartedAt.Time).Milliseconds()
		}
		if result.DistanceMillimeters > 0 && elapsed > 0 {
			pace := elapsed * 500000 / result.DistanceMillimeters
			result.PaceMillisecondsPer500m = &pace
		}
		out.Results = append(out.Results, result)
	}
	sort.SliceStable(out.Results, func(i, j int) bool {
		a, b := out.Results[i], out.Results[j]
		if (a.ElapsedMilliseconds != nil) != (b.ElapsedMilliseconds != nil) {
			return a.ElapsedMilliseconds != nil
		}
		if a.ElapsedMilliseconds != nil {
			return *a.ElapsedMilliseconds < *b.ElapsedMilliseconds
		}
		return a.DistanceMillimeters > b.DistanceMillimeters
	})
	for i := range out.Results {
		if out.Results[i].ElapsedMilliseconds == nil {
			break
		}
		rank := i + 1
		out.Results[i].Rank = &rank
	}
	return out, nil
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
// stay stable throughout. Finished is true once the race has ended and the
// room is marked finished.
type RoomRace struct {
	RoomID            uuid.UUID
	RaceID            *uuid.UUID
	RaceStartsAt      *time.Time
	Label             string
	TargetMillimeters int64
	Racers            []RaceRacer
	Finished          bool
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
	view := RoomRace{RoomID: roomID, Label: row.Label, TargetMillimeters: row.TargetValue, Racers: make([]RaceRacer, 0, len(lineup)), Finished: row.Status == "finished"}
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
	race, err := q.LockRace(ctx, raceID)
	if err != nil {
		return fmt.Errorf("lock telemetry race: %w", err)
	}
	// A finalized race's results are fixed. A late sample is dropped without
	// an error, so the socket stays open to deliver the finished frame.
	if race.FinishedAt.Valid {
		return nil
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
	race, rows, err := lockStandings(ctx, q, raceID)
	if err != nil {
		return nil, err
	}
	standings, err := s.settle(ctx, q, race, rows, final, finalize)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit standings: %w", err)
	}
	return standings, nil
}

// lockStandings locks the race, serializing with Ingest and other sweeps, and
// reads every participant's latest sample and first sample at the target.
func lockStandings(ctx context.Context, q *db.Queries, raceID uuid.UUID) (db.LockRaceRow, []db.ListRaceStandingsRow, error) {
	race, err := q.LockRace(ctx, raceID)
	if err != nil {
		return db.LockRaceRow{}, nil, fmt.Errorf("lock standings race: %w", err)
	}
	if race.Kind != "distance" {
		return db.LockRaceRow{}, nil, fmt.Errorf("standings require a distance race, got %q", race.Kind)
	}
	rows, err := q.ListRaceStandings(ctx, db.ListRaceStandingsParams{
		RaceID: raceID, DistanceMillimeters: pgtype.Int8{Int64: race.TargetValue, Valid: true},
	})
	if err != nil {
		return db.LockRaceRow{}, nil, fmt.Errorf("list race standings: %w", err)
	}
	return race, rows, nil
}

// stale reports whether a racer has sent nothing for longer than the
// staleness threshold. A racer with no samples is measured from race start.
func (s *RaceService) stale(race db.LockRaceRow, row db.ListRaceStandingsRow, now time.Time) bool {
	lastSeen := race.StartedAt.Time
	if row.SampledAt.Valid {
		lastSeen = row.SampledAt.Time
	}
	return now.Sub(lastSeen) > s.cfg.StalenessThreshold
}

// raceOver reports whether every participant is done: already finished or
// DNF, holding a sample at or past the target, or stale without one.
func (s *RaceService) raceOver(race db.LockRaceRow, rows []db.ListRaceStandingsRow, now time.Time) bool {
	for _, row := range rows {
		if row.Status != "racing" || row.TargetElapsedMilliseconds.Valid {
			continue
		}
		if !s.stale(race, row, now) {
			return false
		}
	}
	return true
}

// settle ranks the locked rows. Only a racer with no sample at or past the
// target can be marked DNF: once they reach it, they are a finisher whatever
// happens to their telemetry afterwards. Finalizing records each finish, marks
// the rest DNF, and moves the race and its room to finished.
func (s *RaceService) settle(ctx context.Context, q *db.Queries, race db.LockRaceRow, rows []db.ListRaceStandingsRow, final, finalize bool) ([]RaceStanding, error) {
	now := s.cfg.Now()
	standings := make([]RaceStanding, 0, len(rows))
	for _, row := range rows {
		if row.Status == "dnf" {
			continue
		}
		if row.Status == "racing" && !row.TargetElapsedMilliseconds.Valid && (finalize || s.stale(race, row, now)) {
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
		if err := q.FinishRace(ctx, db.FinishRaceParams{ID: race.ID, FinishedAt: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
			return nil, fmt.Errorf("finish race: %w", err)
		}
		if err := q.FinishRaceRoom(ctx, race.RoomID); err != nil {
			return nil, fmt.Errorf("finish race room: %w", err)
		}
	}
	return standings, nil
}

// raceFinishedFrame is published once on the race channel when Run finalizes.
type raceFinishedFrame struct {
	Type   string    `json:"type"`
	RaceID uuid.UUID `json:"race_id"`
}

// Run watches a begun race until it is over, then finalizes it and publishes
// one {"type":"finished"} frame on the race channel. The race is over once
// every participant has a sample at or past the target or has been silent for
// the staleness threshold, so a race where everyone is DNF also ends. A race
// that is already finalized returns nil without publishing again. Run polls
// every RacePollInterval and returns the wrapped context error when cancelled.
// A failed check is logged and retried with a backoff of up to
// maxRaceRetryInterval, so one transient database error never strands a race.
// A publish failure after the finalize commit is returned: the race is final,
// and clients learn it from the race status endpoint.
func (s *RaceService) Run(ctx context.Context, raceID uuid.UUID) error {
	wait := s.cfg.RacePollInterval
	for {
		done, err := s.finishIfOver(ctx, raceID)
		if done {
			return err
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("race run: %w", ctxErr)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				// The race does not exist; retrying cannot change that.
				return err
			}
			slog.ErrorContext(ctx, "race run check failed; retrying", "race_id", raceID, "retry_in", wait, "error", err)
		}
		if err := s.cfg.Sleep(ctx, wait); err != nil {
			return fmt.Errorf("race run: %w", err)
		}
		if err != nil {
			wait = min(wait*2, max(maxRaceRetryInterval, s.cfg.RacePollInterval))
		} else {
			wait = s.cfg.RacePollInterval
		}
	}
}

// maxRaceRetryInterval caps Run's backoff after failed end-of-race checks.
const maxRaceRetryInterval = 30 * time.Second

// UnfinishedRaces lists races still in progress: begun, not finalized, with
// their room in_progress. Callers run Run for each to resume finalization
// after a restart.
func (s *RaceService) UnfinishedRaces(ctx context.Context) ([]uuid.UUID, error) {
	ids, err := s.q.ListUnfinishedRaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unfinished races: %w", err)
	}
	return ids, nil
}

// finishIfOver finalizes the race in one locked transaction when it is over.
// It reports done when the race is finished, publishing only if this call
// finalized it.
func (s *RaceService) finishIfOver(ctx context.Context, raceID uuid.UUID) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin race run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	race, rows, err := lockStandings(ctx, q, raceID)
	if err != nil {
		return false, err
	}
	if race.FinishedAt.Valid {
		return true, nil
	}
	if !s.raceOver(race, rows, s.cfg.Now()) {
		return false, nil
	}
	if _, err := s.settle(ctx, q, race, rows, true, true); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit race finish: %w", err)
	}
	frame, err := json.Marshal(raceFinishedFrame{Type: "finished", RaceID: raceID})
	if err != nil {
		return true, fmt.Errorf("encode finished frame: %w", err)
	}
	if err := s.broadcaster.Publish(ctx, raceID.String(), frame); err != nil {
		return true, fmt.Errorf("publish race finished: %w", err)
	}
	return true, nil
}
