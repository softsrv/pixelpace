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
	roomID, code, err := NewRoomService(db.New(pool), pool).CreateRoom(ctx, host, roomTestID)
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
	_, code, err := s.CreateRoom(ctx, host, roomTestID)
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
	if room.Status != "waiting" || room.HostUserID != host || room.JoinCode != code {
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
	if _, _, err := s.CreateRoom(ctx, host, roomTestID); err == nil {
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
