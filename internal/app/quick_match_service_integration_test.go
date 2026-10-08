//go:build integration

package app

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/db"
)

func quickMatchIntegrationDB(t *testing.T) *pgxpool.Pool {
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

// Give each test its own race type so queue selection cannot consume another
// test's racers. Copy the seeded 500m preset rather than inventing a format.
func quickMatchIntegrationType(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(context.Background(), "INSERT INTO race_types (id, kind, target_value, label) SELECT $1, kind, target_value, label FROM race_types WHERE id = $2", id, roomTestID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM race_types WHERE id = $1", id)
		if err != nil {
			t.Error(err)
		}
	})
	return id
}

func quickMatchIntegrationService(pool *pgxpool.Pool, now func() time.Time) *QuickMatchService {
	q := db.New(pool)
	return NewQuickMatchService(q, pool, NewRoomService(q, pool), QuickMatchServiceConfig{Now: now})
}

func quickMatchIntegrationWait(t *testing.T, pool *pgxpool.Pool, user, raceType uuid.UUID, joined time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "UPDATE quick_match_queue SET joined_at = $3 WHERE user_id = $1 AND race_type_id = $2", user, raceType, joined)
	if err != nil {
		t.Fatal(err)
	}
}

func quickMatchIntegrationFinish(t *testing.T, pool *pgxpool.Pool, user, raceType uuid.UUID, milliseconds int32, finished time.Time, status string) {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)
	code, err := NewRoomService(q, pool).CreateRoom(ctx, user, raceType)
	if err != nil {
		t.Fatal(err)
	}
	room, err := q.GetActiveRoomByJoinCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	raceID := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, "INSERT INTO races (id, room_id, race_type_id, started_at, finished_at) VALUES ($1, $2, $3, $4, $5)", raceID, room.ID, raceType, finished.Add(-time.Duration(milliseconds)*time.Millisecond), finished)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{"DELETE FROM race_participants WHERE race_id = $1", "DELETE FROM races WHERE id = $1"} {
			if _, err := pool.Exec(ctx, statement, raceID); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(ctx, "DELETE FROM rooms WHERE id = $1", room.ID); err != nil {
			t.Error(err)
		}
	})
	_, err = pool.Exec(ctx, "INSERT INTO race_participants (id, race_id, user_id, status, elapsed_milliseconds, finished_at) VALUES ($1, $2, $3, $4, $5, $6)", uuid.Must(uuid.NewV7()), raceID, user, status, milliseconds, finished)
	if err != nil {
		t.Fatal(err)
	}
}

func TestQuickMatchIntegrationQueue(t *testing.T) {
	pool := quickMatchIntegrationDB(t)
	ctx := context.Background()
	raceType := quickMatchIntegrationType(t, pool)
	a, b := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	s := quickMatchIntegrationService(pool, nil)
	for _, user := range []uuid.UUID{a, b} {
		if err := s.Enqueue(ctx, user, raceType); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.q.ListWaitingByRaceType(ctx, raceType)
	if err != nil || len(rows) != 2 {
		t.Fatalf("queue = %+v, %v", rows, err)
	}
	var joined time.Time
	var version int
	var id uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id, joined_at FROM quick_match_queue WHERE user_id = $1 AND race_type_id = $2", a, raceType).Scan(&id, &joined); err != nil {
		t.Fatal(err)
	}
	version = int(id.Version())
	if version != 7 || time.Since(joined) > time.Minute {
		t.Fatalf("queue defaults = %v/%v", id, joined)
	}
	if err := s.Enqueue(ctx, a, raceType); !errors.Is(err, ErrAlreadyQueued) {
		t.Fatalf("duplicate = %v", err)
	}
	var retained time.Time
	if err := pool.QueryRow(ctx, "SELECT joined_at FROM quick_match_queue WHERE user_id = $1 AND race_type_id = $2", a, raceType).Scan(&retained); err != nil || !retained.Equal(joined) {
		t.Fatalf("duplicate reset wait: %v, %v", retained, err)
	}
	now := time.Now().UTC()
	quickMatchIntegrationWait(t, pool, a, raceType, now.Add(-30*time.Second))
	quickMatchIntegrationWait(t, pool, b, raceType, now.Add(-10*time.Second))
	rows, err = s.q.ListWaitingByRaceType(ctx, raceType)
	if err != nil || len(rows) != 2 {
		t.Fatalf("queue = %+v, %v", rows, err)
	}
	if rows[0].UserID != a || rows[1].UserID != b || math.Abs(rows[0].WaitedSeconds-rows[1].WaitedSeconds-20) > 0.01 {
		t.Fatalf("wait derivation = %+v", rows)
	}
	for _, row := range rows {
		if math.Abs(row.WaitedSeconds-time.Since(row.JoinedAt.Time).Seconds()) > 1 {
			t.Fatalf("wait not derived from joined_at: %+v", row)
		}
	}
	if err := s.Leave(ctx, a, raceType); err != nil {
		t.Fatal(err)
	}
	rows, err = s.q.ListWaitingByRaceType(ctx, raceType)
	if err != nil || len(rows) != 1 || rows[0].UserID != b {
		t.Fatalf("after leave = %+v, %v", rows, err)
	}
	if err := s.Leave(ctx, a, raceType); err != nil {
		t.Fatalf("repeat leave = %v", err)
	}
	if err := s.Enqueue(ctx, a, uuid.Nil); !errors.Is(err, ErrInvalidRaceType) {
		t.Fatalf("invalid type = %v", err)
	}
}

func TestQuickMatchIntegrationMigrationDown(t *testing.T) {
	pool := quickMatchIntegrationDB(t)
	ctx := context.Background()
	// Transaction-local schema exercises the actual up/down pair without
	// dropping the shared queue or changing the database's migration version.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, sql := range []string{"CREATE SCHEMA quick_match_migration_test", "SET LOCAL search_path = quick_match_migration_test, public"} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, direction := range []string{"up", "down"} {
		migration, err := os.ReadFile("../../db/migrations/000012_create_quick_match_queue." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass('quick_match_migration_test.quick_match_queue') IS NOT NULL").Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != (direction == "up") {
			t.Fatalf("after %s exists = %v", direction, exists)
		}
	}
}

func TestQuickMatchIntegrationRecentFinishes(t *testing.T) {
	pool := quickMatchIntegrationDB(t)
	raceType, otherType := quickMatchIntegrationType(t, pool), quickMatchIntegrationType(t, pool)
	user, other := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		quickMatchIntegrationFinish(t, pool, user, raceType, int32(60000+i*1000), now.Add(-time.Duration(i)*time.Hour), "finished")
	}
	quickMatchIntegrationFinish(t, pool, user, otherType, 1, now, "finished")
	quickMatchIntegrationFinish(t, pool, other, raceType, 2, now, "finished")
	quickMatchIntegrationFinish(t, pool, user, raceType, 3, now, "dnf")
	rows, err := db.New(pool).RecentFinishesByRaceType(context.Background(), db.RecentFinishesByRaceTypeParams{RaceTypeID: raceType, UserID: user})
	if err != nil || len(rows) != 7 {
		t.Fatalf("finishes = %+v, %v", rows, err)
	}
	for i, row := range rows {
		if !row.ElapsedMilliseconds.Valid || row.ElapsedMilliseconds.Int32 != int32(60000+i*1000) || !row.FinishedAt.Valid {
			t.Fatalf("finish %d = %+v", i, row)
		}
		if i > 0 && !rows[i-1].FinishedAt.Time.After(row.FinishedAt.Time) {
			t.Fatal("not most recent first")
		}
	}
	if average, rated := quickMatchRating(rows); average != 62 || !rated {
		t.Fatalf("rolling average = %v/%v", average, rated)
	}
}

func TestQuickMatchIntegrationHostAndWindows(t *testing.T) {
	pool := quickMatchIntegrationDB(t)
	ctx := context.Background()
	raceType := quickMatchIntegrationType(t, pool)
	slow, fast := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	unrated := roomIntegrationUser(t, pool)
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		fastMS := int32(60000)
		if i == 5 {
			fastMS = 600000
		} // Older outlier must not change host or grouping.
		quickMatchIntegrationFinish(t, pool, fast, raceType, fastMS, now.Add(-time.Duration(i)*time.Hour), "finished")
		quickMatchIntegrationFinish(t, pool, slow, raceType, 68000, now.Add(-time.Duration(i)*time.Hour), "finished")
	}
	s := quickMatchIntegrationService(pool, func() time.Time { return now })
	for _, user := range []uuid.UUID{slow, fast, unrated} {
		if err := s.Enqueue(ctx, user, raceType); err != nil {
			t.Fatal(err)
		}
	}
	quickMatchIntegrationWait(t, pool, slow, raceType, now.Add(-time.Minute))
	quickMatchIntegrationWait(t, pool, fast, raceType, now)
	quickMatchIntegrationWait(t, pool, unrated, raceType, now.Add(-time.Minute))
	if code, err := s.Match(ctx, slow, raceType); err != nil || code != "" {
		t.Fatalf("asymmetric matched: %q, %v", code, err)
	}
	now = now.Add(10 * time.Second)
	code, err := s.Match(ctx, slow, raceType)
	if err != nil || code == "" {
		t.Fatalf("mutual match: %q, %v", code, err)
	}
	room, err := s.q.GetActiveRoomByJoinCode(ctx, code)
	if err != nil || room.HostUserID != fast {
		t.Fatalf("host = %+v, %v", room, err)
	}
	for _, user := range []uuid.UUID{slow, fast} {
		if _, err := s.q.GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: room.ID, UserID: user}); err != nil {
			t.Fatal(err)
		}
	}
	count, err := s.q.CountRoomParticipants(ctx, room.ID)
	if err != nil || count != 2 {
		t.Fatalf("count = %d, %v", count, err)
	}
	rows, err := s.q.ListWaitingByRaceType(ctx, raceType)
	if err != nil || len(rows) != 1 || rows[0].UserID != unrated {
		t.Fatalf("remaining queue = %+v, %v", rows, err)
	}
}

func TestQuickMatchIntegrationUnratedGroup(t *testing.T) {
	pool := quickMatchIntegrationDB(t)
	ctx := context.Background()
	raceType := quickMatchIntegrationType(t, pool)
	a, b, rated := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	now := time.Now().UTC()
	quickMatchIntegrationFinish(t, pool, rated, raceType, 60000, now, "finished")
	s := quickMatchIntegrationService(pool, func() time.Time { return now })
	for _, user := range []uuid.UUID{a, rated, b} {
		if err := s.Enqueue(ctx, user, raceType); err != nil {
			t.Fatal(err)
		}
		quickMatchIntegrationWait(t, pool, user, raceType, now.Add(-time.Minute))
	}
	code, err := s.Match(ctx, a, raceType)
	if err != nil || code == "" {
		t.Fatalf("unrated match: %q, %v", code, err)
	}
	room, err := s.q.GetActiveRoomByJoinCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if room.HostUserID != a && room.HostUserID != b {
		t.Fatal("rated host in unrated group")
	}
	for _, user := range []uuid.UUID{a, b} {
		if _, err := s.q.GetRoomParticipant(ctx, db.GetRoomParticipantParams{RoomID: room.ID, UserID: user}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.q.ListWaitingByRaceType(ctx, raceType)
	if err != nil || len(rows) != 1 || rows[0].UserID != rated {
		t.Fatalf("rated did not remain: %+v, %v", rows, err)
	}
}

func TestQuickMatchIntegrationAtomicFailure(t *testing.T) {
	pool := quickMatchIntegrationDB(t)
	ctx := context.Background()
	raceType := quickMatchIntegrationType(t, pool)
	a, b := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	now := time.Now().UTC()
	s := quickMatchIntegrationService(pool, func() time.Time { return now })
	for _, user := range []uuid.UUID{a, b} {
		if err := s.Enqueue(ctx, user, raceType); err != nil {
			t.Fatal(err)
		}
		quickMatchIntegrationWait(t, pool, user, raceType, now.Add(-time.Minute))
	}
	// Use the room harness's transaction observer to fail queue removal after
	// real room writes, then assert PostgreSQL rolls the whole operation back.
	observed := &roomObservedPool{Pool: pool, failQuery: "LeaveQuickMatch"}
	s.q, s.pool = db.New(observed), observed
	if _, err := s.Match(ctx, a, raceType); err == nil {
		t.Fatal("expected delete failure")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM rooms WHERE race_type_id = $1", raceType).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial room: %d, %v", count, err)
	}
	rows, err := s.q.ListWaitingByRaceType(ctx, raceType)
	if err != nil || len(rows) != 2 {
		t.Fatalf("lost queue: %+v, %v", rows, err)
	}
}

func TestQuickMatchIntegrationConcurrentMatch(t *testing.T) {
	pool := quickMatchIntegrationDB(t)
	ctx := context.Background()
	raceType := quickMatchIntegrationType(t, pool)
	a, b := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	now := time.Now().UTC()
	s := quickMatchIntegrationService(pool, func() time.Time { return now })
	for _, user := range []uuid.UUID{a, b} {
		if err := s.Enqueue(ctx, user, raceType); err != nil {
			t.Fatal(err)
		}
		quickMatchIntegrationWait(t, pool, user, raceType, now.Add(-time.Minute))
	}
	var wg sync.WaitGroup
	codes, errs := make([]string, 2), make([]error, 2)
	start := make(chan struct{})
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; codes[i], errs[i] = s.Match(ctx, a, raceType) }()
	}
	close(start)
	wg.Wait()
	successes := 0
	for i, err := range errs {
		if err == nil && codes[i] != "" {
			successes++
		} else if !errors.Is(err, ErrNotQueued) {
			t.Fatalf("match %d = %q, %v", i, codes[i], err)
		}
	}
	if successes != 1 {
		t.Fatalf("formed %d rooms", successes)
	}
}
