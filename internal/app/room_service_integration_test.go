//go:build integration

package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/realtime"
)

func roomIntegrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func roomIntegrationUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.New(pool).CreateUser(context.Background(), db.CreateUserParams{ID: id, Email: id.String() + "@room.example.com", PasswordHash: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.New(pool).DeleteUserByID(context.Background(), id); err != nil {
			t.Error(err)
		}
	})
	return id
}

func roomIntegrationCreate(t *testing.T, pool *pgxpool.Pool, host uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	roomID, code, err := NewRoomService(db.New(pool), pool).CreateRoom(ctx, host, roomTestID, ModeRace, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := db.New(pool).GetActiveRoomByJoinCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if roomID != room.ID {
		t.Fatalf("CreateRoom id = %s, want %s", roomID, room.ID)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, "DELETE FROM rooms WHERE id = $1", room.ID); err != nil {
			t.Error(err)
		}
	})
	return room.ID, code
}

// Observe the real PostgreSQL error and transaction boundary, without replacing
// the insert or synthesizing a unique violation. Also reject uniqueness SELECTs.
type roomObservedPool struct {
	*pgxpool.Pool
	queries                                []string
	collisions, begins, commits, rollbacks int
	failQuery                              string
}

func (p *roomObservedPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	p.queries = append(p.queries, sql)
	return p.Pool.QueryRow(ctx, sql, args...)
}
func (p *roomObservedPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	p.begins++
	return &roomObservedTx{Tx: tx, pool: p}, nil
}

type roomObservedTx struct {
	pgx.Tx
	pool *roomObservedPool
}

func (tx *roomObservedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.pool.queries = append(tx.pool.queries, sql)
	if tx.pool.failQuery != "" && strings.HasPrefix(sql, "-- name: "+tx.pool.failQuery+" ") {
		return pgconn.CommandTag{}, errors.New("injected write failure")
	}
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_rooms_join_code_active" {
		tx.pool.collisions++
	}
	return tag, err
}
func (tx *roomObservedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.pool.queries = append(tx.pool.queries, sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func (tx *roomObservedTx) Commit(ctx context.Context) error {
	err := tx.Tx.Commit(ctx)
	if err == nil {
		tx.pool.commits++
	}
	return err
}
func (tx *roomObservedTx) Rollback(ctx context.Context) error {
	err := tx.Tx.Rollback(ctx)
	if err == nil {
		tx.pool.rollbacks++
	}
	return err
}

func TestRoomIntegrationCreateCollision(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host := roomIntegrationUser(t, pool)
	_, existingCode := roomIntegrationCreate(t, pool, host)
	observed := &roomObservedPool{Pool: pool}
	s := NewRoomService(db.New(observed), observed)
	calls := 0
	s.newJoinCode = func() (string, error) {
		calls++
		if calls == 1 {
			return existingCode, nil
		}
		return generateJoinCode()
	}
	_, code, err := s.CreateRoom(ctx, host, roomTestID, ModeRace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code == existingCode || calls != 2 || observed.collisions != 1 || observed.begins != 2 || observed.commits != 1 || observed.rollbacks != 1 {
		t.Fatalf("collision did not recover: code %q, calls %d, observations %+v", code, calls, observed)
	}
	for _, sql := range observed.queries {
		if strings.Contains(sql, "SELECT") && !strings.HasPrefix(sql, "-- name: GetRaceType ") {
			t.Fatalf("unexpected precheck: %s", sql)
		}
	}
	room, err := db.New(pool).GetActiveRoomByJoinCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if room.Status != "waiting" || room.HostUserID != host || room.JoinCode != code || room.Mode != ModeRace || room.DurationSeconds.Valid {
		t.Fatalf("created room: %+v", room)
	}
	participant, err := db.New(pool).GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: room.ID, UserID: host})
	if err != nil || participant.Ready {
		t.Fatalf("host participant: %+v, err %v", participant, err)
	}
	count, err := db.New(pool).CountRoomParticipants(ctx, room.ID)
	if err != nil || count != 1 {
		t.Fatalf("participant count %d, err %v", count, err)
	}
}

func TestRoomIntegrationLifecycle(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, guest := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	roomID, code := roomIntegrationCreate(t, pool, host)
	q := db.New(pool)
	s := NewRoomService(q, pool)
	if joined, err := s.JoinRoom(ctx, guest, code); err != nil || joined != roomID {
		t.Fatalf("join = %s, %v; want %s", joined, err, roomID)
	}
	if _, err := s.JoinRoom(ctx, guest, code); !errors.Is(err, ErrAlreadyParticipant) {
		t.Fatalf("duplicate join = %v", err)
	}
	participant, err := q.GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: roomID, UserID: guest})
	if err != nil || participant.Ready {
		t.Fatalf("join ready: %+v, %v", participant, err)
	}
	if err := s.StartRace(ctx, host, roomID); !errors.Is(err, ErrNotAllReady) {
		t.Fatalf("unready start = %v", err)
	}
	if err := s.MarkReady(ctx, guest, roomID); err != nil {
		t.Fatal(err)
	}
	hostParticipant, err := q.GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: roomID, UserID: host})
	if err != nil || hostParticipant.Ready {
		t.Fatalf("marked another participant ready: %+v, %v", hostParticipant, err)
	}
	if err := s.KickParticipant(ctx, host, roomID, guest); !errors.Is(err, ErrParticipantReady) {
		t.Fatalf("ready kick = %v", err)
	}
	if err := s.MarkReady(ctx, host, roomID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRace(ctx, host, roomID); err != nil {
		t.Fatal(err)
	}
	room, err := q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.Status != "counting_down" {
		t.Fatalf("start status = %s, %v", room.Status, err)
	}
	if _, err := s.JoinRoom(ctx, roomIntegrationUser(t, pool), code); !errors.Is(err, ErrRoomNotJoinable) {
		t.Fatalf("countdown join = %v", err)
	}
	if err := s.BeginRace(ctx, roomID); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRace(ctx, roomID); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("second begin = %v", err)
	}
	room, err = q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.Status != "in_progress" {
		t.Fatalf("begin status = %s, %v", room.Status, err)
	}
}

func TestRoomIntegrationLeaveAndKick(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, guest, target := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	roomID, code := roomIntegrationCreate(t, pool, host)
	q := db.New(pool)
	s := NewRoomService(q, pool)
	for _, user := range []uuid.UUID{guest, target} {
		if _, err := s.JoinRoom(ctx, user, code); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.KickParticipant(ctx, guest, roomID, target); !errors.Is(err, ErrNotHost) {
		t.Fatalf("non-host kick = %v", err)
	}
	if err := s.KickParticipant(ctx, host, roomID, target); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: roomID, UserID: target}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("target remains: %v", err)
	}
	if err := s.LeaveRoom(ctx, host, roomID); err != nil {
		t.Fatal(err)
	}
	room, err := q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.HostUserID != guest || room.Status != "waiting" {
		t.Fatalf("host handoff: %+v, %v", room, err)
	}
	if err := s.LeaveRoom(ctx, guest, roomID); err != nil {
		t.Fatal(err)
	}
	room, err = q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.Status != "dissolved" {
		t.Fatalf("last leaver: %+v, %v", room, err)
	}
	if _, err := s.JoinRoom(ctx, target, code); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("dissolved join = %v", err)
	}
}

func TestRoomIntegrationAtomicFailures(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, guest := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	observed := &roomObservedPool{Pool: pool, failQuery: "InsertRoomParticipant"}
	s := NewRoomService(db.New(observed), observed)
	if _, _, err := s.CreateRoom(ctx, host, roomTestID, ModeRace, nil); err == nil {
		t.Fatal("expected create failure")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rooms WHERE host_user_id = $1", host).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan room: %d, %v", count, err)
	}
	roomID, code := roomIntegrationCreate(t, pool, host)
	if _, err := NewRoomService(db.New(pool), pool).JoinRoom(ctx, guest, code); err != nil {
		t.Fatal(err)
	}
	observed.failQuery = "ReassignRoomHost"
	if err := s.LeaveRoom(ctx, host, roomID); err == nil {
		t.Fatal("expected handoff failure")
	}
	if _, err := db.New(pool).GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: roomID, UserID: host}); err != nil {
		t.Fatalf("host delete was not rolled back: %v", err)
	}
}

func TestRoomIntegrationConcurrentJoins(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, guest := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	roomID, code := roomIntegrationCreate(t, pool, host)
	s := NewRoomService(db.New(pool), pool)
	runJoins := func(users []uuid.UUID) []error {
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, len(users))
		for i, user := range users {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; _, errs[i] = s.JoinRoom(ctx, user, code) }()
		}
		close(start)
		wg.Wait()
		return errs
	}
	errs := runJoins([]uuid.UUID{guest, guest})
	if (errs[0] != nil || !errors.Is(errs[1], ErrAlreadyParticipant)) && (errs[1] != nil || !errors.Is(errs[0], ErrAlreadyParticipant)) {
		t.Fatalf("duplicate concurrent joins: %v", errs)
	}
	users := make([]uuid.UUID, 12)
	for i := range users {
		users[i] = roomIntegrationUser(t, pool)
	}
	successes := 0
	for _, err := range runJoins(users) {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrRoomFull) {
			t.Fatal(err)
		}
	}
	if successes != 6 {
		t.Fatalf("joined %d, want 6", successes)
	}
	count, err := db.New(pool).CountRoomParticipants(ctx, roomID)
	if err != nil || count != 8 {
		t.Fatalf("cap: count %d, err %v", count, err)
	}
}

// The roster is read from the real participants and accounts: a named host,
// an unnamed joiner shown by placeholder, ready flags from MarkReady, the
// kicked participant gone, and the race id once a race row exists.
func TestRoomIntegrationRoster(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, joiner, target, outsider := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool), roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	q := db.New(pool)
	hostName := "Host" + strings.ReplaceAll(host.String(), "-", "")[24:]
	if err := q.SetUserDisplayName(ctx, db.SetUserDisplayNameParams{ID: host, DisplayName: pgtype.Text{String: hostName, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	roomID, code := roomIntegrationCreate(t, pool, host)
	s := NewRoomService(q, pool)
	for _, user := range []uuid.UUID{joiner, target} {
		if _, err := s.JoinRoom(ctx, user, code); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkReady(ctx, joiner, roomID); err != nil {
		t.Fatal(err)
	}
	if err := s.KickParticipant(ctx, host, roomID, target); err != nil {
		t.Fatal(err)
	}
	roster, err := s.Roster(ctx, joiner, roomID)
	if err != nil {
		t.Fatal(err)
	}
	want := []RoomRosterEntry{
		{PublicPlayer: PublicPlayer{UserID: host, Name: hostName}, Ready: false, Host: true},
		{PublicPlayer: NewPublicPlayer(joiner, pgtype.Text{}), Ready: true, Host: false},
	}
	if !reflect.DeepEqual(roster.Participants, want) || roster.RaceID != nil {
		t.Fatalf("roster = %+v (race %v), want %+v and no race", roster.Participants, roster.RaceID, want)
	}
	if !strings.HasPrefix(roster.Participants[1].Name, "Racer ") {
		t.Fatalf("joiner placeholder = %q", roster.Participants[1].Name)
	}
	for _, caller := range []uuid.UUID{outsider, target} {
		if got, err := s.Roster(ctx, caller, roomID); !errors.Is(err, ErrParticipantNotFound) || got.Participants != nil {
			t.Fatalf("non-member roster = %+v, %v", got, err)
		}
	}
	raceID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, "INSERT INTO races (id, room_id, race_type_id, started_at) VALUES ($1, $2, $3, now())", raceID, roomID, roomTestID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM races WHERE id = $1", raceID); err != nil {
			t.Error(err)
		}
	})
	roster, err = s.Roster(ctx, host, roomID)
	if err != nil || roster.RaceID == nil || *roster.RaceID != raceID {
		t.Fatalf("roster race = %v, %v; want %s", roster.RaceID, err, raceID)
	}
}

// roomIntegrationReadyRoom creates a room with a host and a guest, both ready,
// and removes any races begun for it before the room itself is deleted.
func roomIntegrationReadyRoom(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	host, guest := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	roomID, code := roomIntegrationCreate(t, pool, host)
	t.Cleanup(func() {
		for _, sql := range []string{
			"DELETE FROM race_participants WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM races WHERE room_id = $1",
		} {
			if _, err := pool.Exec(context.Background(), sql, roomID); err != nil {
				t.Error(err)
			}
		}
	})
	s := NewRoomService(db.New(pool), pool)
	if _, err := s.JoinRoom(ctx, guest, code); err != nil {
		t.Fatal(err)
	}
	for _, user := range []uuid.UUID{host, guest} {
		if err := s.MarkReady(ctx, user, roomID); err != nil {
			t.Fatal(err)
		}
	}
	return roomID, host, guest
}

func roomIntegrationRaceService(q *db.Queries, pool pgxBeginner) *RaceService {
	return NewRaceService(q, pool, realtime.NewFake(), RaceServiceConfig{
		EquipmentTimeout: 50 * time.Millisecond, EquipmentPollInterval: 5 * time.Millisecond,
		CountdownDuration: 10 * time.Millisecond,
	})
}

// roomIntegrationEquipmentReady reports each user's equipment ready on the
// race page of a started room.
func roomIntegrationEquipmentReady(t *testing.T, pool *pgxpool.Pool, roomID uuid.UUID, users ...uuid.UUID) {
	t.Helper()
	races := roomIntegrationRaceService(db.New(pool), pool)
	for _, user := range users {
		if err := races.MarkEquipmentReady(context.Background(), roomID, user); err != nil {
			t.Fatal(err)
		}
	}
}

// A started room's countdown begins its race: one race row for the room, every
// room participant a race participant, and the roster naming that race.
func TestRoomIntegrationStartBeginsRace(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	roomID, host, guest := roomIntegrationReadyRoom(t, pool)
	q := db.New(pool)
	s := NewRoomService(q, pool)
	if err := s.StartRace(ctx, host, roomID); err != nil {
		t.Fatal(err)
	}
	roomIntegrationEquipmentReady(t, pool, roomID, host, guest)
	race, err := roomIntegrationRaceService(q, pool).Begin(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	var races int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM races WHERE room_id = $1", roomID).Scan(&races); err != nil || races != 1 {
		t.Fatalf("races for room = %d, %v; want 1", races, err)
	}
	rows, err := pool.Query(ctx, "SELECT user_id FROM race_participants WHERE race_id = $1", race.ID)
	if err != nil {
		t.Fatal(err)
	}
	racers, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	for _, racer := range racers {
		seen[racer] = true
	}
	if len(racers) != 2 || !seen[host] || !seen[guest] {
		t.Fatalf("race participants = %v, want %s and %s", racers, host, guest)
	}
	roster, err := s.Roster(ctx, guest, roomID)
	if err != nil || roster.RaceID == nil || *roster.RaceID != race.ID {
		t.Fatalf("roster race = %v, %v; want %s", roster.RaceID, err, race.ID)
	}
}

// A begin that is cancelled, or whose race write fails, leaves no race behind;
// ResetCountdown returns the room to waiting and it can be started again.
func TestRoomIntegrationFailedBeginResetsRoom(t *testing.T) {
	for _, tc := range []struct {
		name  string
		begin func(*testing.T, *pgxpool.Pool, uuid.UUID) error
	}{
		{"countdown cancelled", func(_ *testing.T, pool *pgxpool.Pool, roomID uuid.UUID) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := roomIntegrationRaceService(db.New(pool), pool).Begin(ctx, roomID)
			return err
		}},
		{"race participant write fails", func(_ *testing.T, pool *pgxpool.Pool, roomID uuid.UUID) error {
			observed := &roomObservedPool{Pool: pool, failQuery: "CreateRaceParticipant"}
			_, err := roomIntegrationRaceService(db.New(observed), observed).Begin(context.Background(), roomID)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := roomIntegrationDB(t)
			ctx := context.Background()
			roomID, host, guest := roomIntegrationReadyRoom(t, pool)
			q := db.New(pool)
			s := NewRoomService(q, pool)
			if err := s.StartRace(ctx, host, roomID); err != nil {
				t.Fatal(err)
			}
			roomIntegrationEquipmentReady(t, pool, roomID, host, guest)
			if err := tc.begin(t, pool, roomID); err == nil {
				t.Fatal("expected begin failure")
			}
			if err := s.ResetCountdown(ctx, roomID); err != nil {
				t.Fatal(err)
			}
			room, err := q.GetRoomForUpdate(ctx, roomID)
			if err != nil || room.Status != "waiting" || room.RaceStartsAt.Valid {
				t.Fatalf("room after reset = %+v, %v; want waiting with no start time", room, err)
			}
			if unready, err := q.CountRoomEquipmentUnready(ctx, roomID); err != nil || unready != 2 {
				t.Fatalf("equipment unready after reset = %d, %v; want every racer to reconnect", unready, err)
			}
			var races int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM races WHERE room_id = $1", roomID).Scan(&races); err != nil || races != 0 {
				t.Fatalf("races after failed begin = %d, %v; want 0", races, err)
			}
			if err := s.ResetCountdown(ctx, roomID); !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("reset of waiting room = %v", err)
			}
			if err := s.StartRace(ctx, host, roomID); err != nil {
				t.Fatalf("second start = %v", err)
			}
		})
	}
}

func TestRoomIntegrationCreateInvalidModeRejected(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host := roomIntegrationUser(t, pool)
	s := NewRoomService(db.New(pool), pool)

	var countBefore int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rooms").Scan(&countBefore); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"", "sprint", "invalid"} {
		_, _, err := s.CreateRoom(ctx, host, roomTestID, mode, nil)
		if !errors.Is(err, ErrInvalidSessionMode) {
			t.Fatalf("mode %q err = %v, want ErrInvalidSessionMode", mode, err)
		}
	}

	var countAfter int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rooms").Scan(&countAfter); err != nil {
		t.Fatal(err)
	}
	if countAfter != countBefore {
		t.Fatalf("rooms count changed: before = %d, after = %d", countBefore, countAfter)
	}
}

// roomIntegrationTimeTrial creates a time trial through CreateRoom, as the
// dashboard's 'Start time trial' does, with cleanup of what it leaves behind.
func roomIntegrationTimeTrial(t *testing.T, pool *pgxpool.Pool, host uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	roomID, code, err := NewRoomService(db.New(pool), pool).CreateRoom(ctx, host, roomTestID, ModeTimeTrial, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			"DELETE FROM race_participants WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM races WHERE room_id = $1",
			"DELETE FROM rooms WHERE id = $1",
		} {
			if _, err := pool.Exec(context.Background(), sql, roomID); err != nil {
				t.Error(err)
			}
		}
	})
	return roomID, code
}

// A time trial needs no lobby: CreateRoom leaves the host as its only
// participant with the room already counting down, and the begin the handler
// launches schedules the race one 10-second countdown later and creates a
// time_trial race.
func TestRoomIntegrationCreateTimeTrialBeginsWithoutLobby(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, outsider := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	roomID, code := roomIntegrationTimeTrial(t, pool, host)
	q := db.New(pool)
	rooms := NewRoomService(q, pool)

	room, err := q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.Status != "counting_down" || room.Mode != ModeTimeTrial || room.HostUserID != host {
		t.Fatalf("created room = %+v, %v; want a counting-down time_trial hosted by %s", room, err, host)
	}
	if count, err := q.CountRoomParticipants(ctx, roomID); err != nil || count != 1 {
		t.Fatalf("participants = %d, %v; want the host alone", count, err)
	}
	// Nobody joins a time trial: there is no lobby to join.
	if _, err := rooms.JoinRoom(ctx, outsider, code); !errors.Is(err, ErrRoomNotJoinable) {
		t.Fatalf("join time trial = %v, want ErrRoomNotJoinable", err)
	}

	begunAt := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	races := NewRaceService(q, pool, realtime.NewFake(), RaceServiceConfig{
		EquipmentTimeout: 50 * time.Millisecond, EquipmentPollInterval: 5 * time.Millisecond,
		Now:   func() time.Time { return begunAt },
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	// The host's race page connects their machine during the countdown.
	if err := races.MarkEquipmentReady(ctx, roomID, host); err != nil {
		t.Fatal(err)
	}
	race, err := races.Begin(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	var timeTrial bool
	if err := pool.QueryRow(ctx, "SELECT mode, time_trial FROM races WHERE id = $1", race.ID).Scan(&mode, &timeTrial); err != nil {
		t.Fatal(err)
	}
	if mode != ModeTimeTrial || !timeTrial {
		t.Fatalf("races.mode = %q, time_trial = %v; want time_trial, true", mode, timeTrial)
	}
	row, err := q.GetRoomRace(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status == "waiting" || row.Mode != ModeTimeTrial {
		t.Fatalf("room race = %+v; want past waiting in time_trial mode", row)
	}
	if want := begunAt.Add(10 * time.Second); !row.RaceStartsAt.Valid || !row.RaceStartsAt.Time.Equal(want) {
		t.Fatalf("race starts at %v, want %v (10 s after begin)", row.RaceStartsAt, want)
	}
}

// A time trial whose begin fails has no lobby to return to: ResetCountdown
// dissolves it rather than leaving a joinable waiting room behind.
func TestRoomIntegrationTimeTrialFailedBeginDissolves(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host := roomIntegrationUser(t, pool)
	roomID, _ := roomIntegrationTimeTrial(t, pool, host)
	q := db.New(pool)
	roomIntegrationEquipmentReady(t, pool, roomID, host)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := roomIntegrationRaceService(q, pool).Begin(cancelled, roomID); err == nil {
		t.Fatal("expected begin failure")
	}
	if err := NewRoomService(q, pool).ResetCountdown(ctx, roomID); err != nil {
		t.Fatal(err)
	}
	room, err := q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.Status != "dissolved" {
		t.Fatalf("time trial after failed begin = %+v, %v; want dissolved", room, err)
	}
}

// A warmup is created solo and already counting down, on the warmup race type
// with its length stored in seconds, whatever race type the caller names.
func TestRoomIntegrationCreateWarmupIsSoloAndCountingDown(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, outsider := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	minutes := int32(5)
	s := NewRoomService(db.New(pool), pool)
	roomID, code, err := s.CreateRoom(ctx, host, uuid.Nil, ModeWarmup, &minutes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM rooms WHERE id = $1", roomID); err != nil {
			t.Error(err)
		}
	})

	var mode, status string
	var raceType uuid.UUID
	var duration int32
	if err := pool.QueryRow(ctx, "SELECT mode, status, race_type_id, duration_seconds FROM rooms WHERE id = $1", roomID).Scan(&mode, &status, &raceType, &duration); err != nil {
		t.Fatal(err)
	}
	if mode != ModeWarmup || status != "counting_down" || raceType != WarmupRaceTypeID || duration != 300 {
		t.Fatalf("room = %q/%q/%s/%ds; want warmup/counting_down/%s/300s", mode, status, raceType, duration, WarmupRaceTypeID)
	}
	var participants []uuid.UUID
	rows, err := pool.Query(ctx, "SELECT user_id FROM room_participants WHERE room_id = $1", roomID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		participants = append(participants, id)
	}
	if len(participants) != 1 || participants[0] != host {
		t.Fatalf("participants = %v; want the host %s alone", participants, host)
	}
	if _, err := s.JoinRoom(ctx, outsider, code); !errors.Is(err, ErrRoomNotJoinable) {
		t.Fatalf("join warmup = %v, want ErrRoomNotJoinable", err)
	}
}

// A warmup whose minutes are outside 1-20 or missing is refused with the typed
// error and leaves no room behind.
func TestRoomIntegrationCreateWarmupInvalidMinutesRejected(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host := roomIntegrationUser(t, pool)
	s := NewRoomService(db.New(pool), pool)
	var before int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rooms").Scan(&before); err != nil {
		t.Fatal(err)
	}
	zero, negative, over, huge := int32(0), int32(-1), int32(21), int32(1<<30)
	for name, minutes := range map[string]*int32{"missing": nil, "zero": &zero, "negative": &negative, "over": &over, "huge": &huge} {
		if _, _, err := s.CreateRoom(ctx, host, WarmupRaceTypeID, ModeWarmup, minutes); !errors.Is(err, ErrInvalidWarmupMinutes) {
			t.Fatalf("%s minutes err = %v, want ErrInvalidWarmupMinutes", name, err)
		}
	}
	var after int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rooms").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("rooms before = %d, after = %d; want unchanged", before, after)
	}
}
