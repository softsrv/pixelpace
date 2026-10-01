//go:build integration

package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/db"
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
	code, err := NewRoomService(db.New(pool), pool).CreateRoom(ctx, host, roomTestID)
	if err != nil {
		t.Fatal(err)
	}
	room, err := db.New(pool).GetActiveRoomByJoinCode(ctx, code)
	if err != nil {
		t.Fatal(err)
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
	code, err := s.CreateRoom(ctx, host, roomTestID)
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
	if err := s.JoinRoom(ctx, guest, code); err != nil {
		t.Fatal(err)
	}
	if err := s.JoinRoom(ctx, guest, code); !errors.Is(err, ErrAlreadyParticipant) {
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
	if err := s.JoinRoom(ctx, roomIntegrationUser(t, pool), code); !errors.Is(err, ErrRoomNotJoinable) {
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
		if err := s.JoinRoom(ctx, user, code); err != nil {
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
	if err := s.JoinRoom(ctx, target, code); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("dissolved join = %v", err)
	}
}

func TestRoomIntegrationAtomicFailures(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, guest := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	observed := &roomObservedPool{Pool: pool, failQuery: "InsertRoomParticipant"}
	s := NewRoomService(db.New(observed), observed)
	if _, err := s.CreateRoom(ctx, host, roomTestID); err == nil {
		t.Fatal("expected create failure")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rooms WHERE host_user_id = $1", host).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan room: %d, %v", count, err)
	}
	roomID, code := roomIntegrationCreate(t, pool, host)
	if err := NewRoomService(db.New(pool), pool).JoinRoom(ctx, guest, code); err != nil {
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
			go func() { defer wg.Done(); <-start; errs[i] = s.JoinRoom(ctx, user, code) }()
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
