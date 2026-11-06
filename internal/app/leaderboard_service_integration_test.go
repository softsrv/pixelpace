//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	internalhttp "github.com/softsrv/starter/internal/http"
)

type leaderboardFixture struct {
	pool                      *pgxpool.Pool
	svc                       *app.LeaderboardService
	now                       time.Time
	raceType, otherType, room uuid.UUID
	users                     []uuid.UUID
}

func seedLeaderboard(t *testing.T, count int) *leaderboardFixture {
	t.Helper()
	f := &leaderboardFixture{pool: testDB(t), now: time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC), raceType: uuid.Must(uuid.NewV7()), otherType: uuid.Must(uuid.NewV7()), room: uuid.Must(uuid.NewV7())}
	ctx := context.Background()
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM telemetry_samples WHERE race_id IN (SELECT id FROM activities WHERE room_id = $1)",
			"DELETE FROM race_participants WHERE race_id IN (SELECT id FROM activities WHERE room_id = $1)",
			"DELETE FROM activities WHERE room_id = $1",
			"DELETE FROM rooms WHERE id = $1",
		} {
			if _, err := f.pool.Exec(ctx, query, f.room); err != nil {
				t.Error(err)
			}
		}
		for _, id := range f.users {
			if _, err := f.pool.Exec(ctx, "DELETE FROM friendships WHERE user_id_a = $1 OR user_id_b = $1", id); err != nil {
				t.Error(err)
			}
			if _, err := f.pool.Exec(ctx, "DELETE FROM users WHERE id = $1", id); err != nil {
				t.Error(err)
			}
		}
		for _, id := range []uuid.UUID{f.raceType, f.otherType} {
			if _, err := f.pool.Exec(ctx, "DELETE FROM presets WHERE id = $1", id); err != nil {
				t.Error(err)
			}
		}
	})
	for _, id := range []uuid.UUID{f.raceType, f.otherType} {
		f.exec(t, "INSERT INTO presets (id, kind, target_value, label) VALUES ($1, 'distance', 500000, 'leaderboard test')", id)
	}
	for i := 0; i < count; i++ {
		id := uuid.Must(uuid.NewV7())
		name := fmt.Sprintf("Racer %d", i)
		f.exec(t, "INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'unused', $3)", id, id.String()+"@leaderboard.example", name)
		f.users = append(f.users, id)
	}
	// Finished rooms do not participate in the active join-code unique index.
	f.exec(t, "INSERT INTO rooms (id, preset_id, host_user_id, status, join_code) VALUES ($1, $2, $3, 'finished', 'ABCDEF')", f.room, f.raceType, f.users[0])
	q := db.New(f.pool)
	f.svc = app.NewLeaderboardService(q, app.NewFriendService(q, f.pool, app.FriendServiceConfig{}), app.LeaderboardServiceConfig{Now: func() time.Time { return f.now }})
	return f
}

func (f *leaderboardFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *leaderboardFixture) result(t *testing.T, user int, raceType uuid.UUID, status string, elapsed int32, finished time.Time) {
	t.Helper()
	f.resultIn(t, false, user, raceType, status, elapsed, finished)
}

// timeTrial records a result from a race chosen as a time trial.
func (f *leaderboardFixture) timeTrial(t *testing.T, user int, raceType uuid.UUID, status string, elapsed int32, finished time.Time) {
	t.Helper()
	f.resultIn(t, true, user, raceType, status, elapsed, finished)
}

func (f *leaderboardFixture) resultIn(t *testing.T, timeTrial bool, user int, raceType uuid.UUID, status string, elapsed int32, finished time.Time) {
	t.Helper()
	mode := "race"
	if timeTrial {
		mode = "time_trial"
	}
	f.resultMode(t, mode, user, raceType, status, elapsed, finished)
}

func (f *leaderboardFixture) resultMode(t *testing.T, mode string, user int, raceType uuid.UUID, status string, elapsed int32, finished time.Time) {
	t.Helper()
	race := uuid.Must(uuid.NewV7())
	timeTrial := mode == "time_trial"
	// The race timestamp deliberately differs from the participant's completion.
	f.exec(t, "INSERT INTO activities (id, room_id, preset_id, started_at, finished_at, time_trial, activity_type) VALUES ($1, $2, $3, $4, $5, $6, $7)", race, f.room, raceType, finished.Add(-time.Hour), f.now, timeTrial, mode)
	f.exec(t, "INSERT INTO race_participants (id, race_id, user_id, status, finished_at, elapsed_milliseconds) VALUES ($1, $2, $3, $4, $5, $6)", uuid.Must(uuid.NewV7()), race, f.users[user], status, finished, elapsed)
}

func (f *leaderboardFixture) friends(t *testing.T, a, b int) {
	t.Helper()
	f.exec(t, "INSERT INTO friendships (id, user_id_a, user_id_b) VALUES ($1, LEAST($2::uuid, $3::uuid), GREATEST($2::uuid, $3::uuid))", uuid.Must(uuid.NewV7()), f.users[a], f.users[b])
}

func (f *leaderboardFixture) check(t *testing.T, scope, window string, caller int, want ...app.LeaderboardRow) {
	t.Helper()
	f.checkMode(t, "race", scope, window, caller, want...)
}

func (f *leaderboardFixture) checkMode(t *testing.T, mode, scope, window string, caller int, want ...app.LeaderboardRow) {
	t.Helper()
	got, err := f.svc.Leaderboard(context.Background(), scope, window, mode, f.raceType, f.users[caller])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("%s/%s/%s: got %+v, want %+v", mode, scope, window, got, want)
	}
}

func TestLeaderboardPersonalBestAndStatusesIntegration(t *testing.T) {
	f := seedLeaderboard(t, 4)
	f.result(t, 0, f.raceType, "finished", 4000, f.now)
	f.result(t, 0, f.raceType, "finished", 2000, f.now)
	f.result(t, 1, f.raceType, "finished", 3000, f.now)
	f.result(t, 0, f.raceType, "dnf", 1, f.now)
	f.result(t, 0, f.raceType, "racing", 2, f.now)
	f.result(t, 2, f.raceType, "dnf", 1, f.now)
	f.result(t, 2, f.raceType, "racing", 2, f.now)
	f.result(t, 3, f.otherType, "finished", 1, f.now)
	f.result(t, 1, f.otherType, "finished", 1, f.now)
	for _, window := range []string{"all-time", "month", "week"} {
		f.check(t, "global", window, 0,
			app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 2000},
			app.LeaderboardRow{UserID: f.users[1], Name: "Racer 1", ElapsedMilliseconds: 3000})

		got, err := f.svc.Leaderboard(context.Background(), "global", window, "race", f.raceType, f.users[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range got {
			for _, id := range f.users {
				email := id.String() + "@leaderboard.example"
				if row.Name == email || strings.Contains(row.Name, "@") {
					t.Fatalf("row leaked email %s: %+v", email, row)
				}
			}
		}
	}
}

func TestLeaderboardTimeTrialSeparationIntegration(t *testing.T) {
	f := seedLeaderboard(t, 4)
	f.friends(t, 0, 1)
	// Racer 0's faster time trial must not become their race personal best,
	// and their slower race must not appear on the time trial leaderboard.
	f.result(t, 0, f.raceType, "finished", 3000, f.now)
	f.timeTrial(t, 0, f.raceType, "finished", 1000, f.now)
	f.timeTrial(t, 0, f.raceType, "finished", 1500, f.now)
	f.result(t, 1, f.raceType, "finished", 2000, f.now)
	f.timeTrial(t, 1, f.raceType, "dnf", 1, f.now)
	f.timeTrial(t, 2, f.raceType, "finished", 2500, f.now)
	f.timeTrial(t, 3, f.otherType, "finished", 1, f.now)
	// A race-mode race with a single rower (the others dropped) stays on the
	// Race board, and a warmup ranks on neither board.
	f.result(t, 3, f.raceType, "finished", 2200, f.now)
	f.resultMode(t, "warmup", 2, f.raceType, "finished", 100, f.now)
	// The warm-up sentinel preset is never offered as a distance option.
	types, err := db.New(f.pool).ListDistancePresets(context.Background())
	if err != nil || len(types) == 0 {
		t.Fatalf("distance presets = %+v, %v", types, err)
	}
	for _, rt := range types {
		if rt.Kind != "distance" || rt.ID == uuid.MustParse("01950000-0000-7000-8000-000000000009") {
			t.Fatalf("distance options include %+v", rt)
		}
	}
	for _, window := range []string{"all-time", "month", "week"} {
		f.checkMode(t, "race", "global", window, 0,
			app.LeaderboardRow{UserID: f.users[1], Name: "Racer 1", ElapsedMilliseconds: 2000},
			app.LeaderboardRow{UserID: f.users[3], Name: "Racer 3", ElapsedMilliseconds: 2200},
			app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 3000})
		f.checkMode(t, "time-trial", "global", window, 0,
			app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 1000},
			app.LeaderboardRow{UserID: f.users[2], Name: "Racer 2", ElapsedMilliseconds: 2500})
		f.checkMode(t, "time-trial", "friends", window, 1,
			app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 1000})
		f.checkMode(t, "time-trial", "friends", window, 0) // Racer 1's only time trial was a DNF.
	}
	old := f.now.Add(-8 * 24 * time.Hour)
	f.timeTrial(t, 3, f.raceType, "finished", 500, old)
	f.checkMode(t, "time-trial", "global", "week", 0,
		app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 1000},
		app.LeaderboardRow{UserID: f.users[2], Name: "Racer 2", ElapsedMilliseconds: 2500})
	f.checkMode(t, "time-trial", "global", "all-time", 0,
		app.LeaderboardRow{UserID: f.users[3], Name: "Racer 3", ElapsedMilliseconds: 500},
		app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 1000},
		app.LeaderboardRow{UserID: f.users[2], Name: "Racer 2", ElapsedMilliseconds: 2500})
}

func TestLeaderboardWindowBoundariesIntegration(t *testing.T) {
	for _, tc := range []struct {
		window   string
		boundary time.Time
	}{
		{"week", time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)},
		{"month", time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.window, func(t *testing.T) {
			f := seedLeaderboard(t, 4)
			for _, racer := range []int{0, 1, 2} {
				f.friends(t, 3, racer)
			}
			f.result(t, 0, f.raceType, "finished", 3000, tc.boundary.Add(time.Microsecond))
			f.result(t, 1, f.raceType, "finished", 2000, tc.boundary.Add(-time.Microsecond))
			f.result(t, 2, f.raceType, "finished", 4000, tc.boundary)
			// The out-of-window personal best must not displace an eligible attempt.
			f.result(t, 0, f.raceType, "finished", 1000, tc.boundary.Add(-time.Microsecond))
			for _, scope := range []string{"global", "friends"} {
				f.check(t, scope, tc.window, 3,
					app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 3000},
					app.LeaderboardRow{UserID: f.users[2], Name: "Racer 2", ElapsedMilliseconds: 4000})
				f.check(t, scope, "all-time", 3,
					app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 1000},
					app.LeaderboardRow{UserID: f.users[1], Name: "Racer 1", ElapsedMilliseconds: 2000},
					app.LeaderboardRow{UserID: f.users[2], Name: "Racer 2", ElapsedMilliseconds: 4000})
			}
		})
	}
}

func TestLeaderboardFriendsAndFinishedIntegration(t *testing.T) {
	f := seedLeaderboard(t, 4)
	f.friends(t, 0, 1)
	f.friends(t, 0, 2)
	f.result(t, 0, f.raceType, "finished", 500, f.now)
	f.result(t, 1, f.raceType, "finished", 4000, f.now)
	f.result(t, 1, f.raceType, "finished", 3000, f.now)
	f.result(t, 1, f.raceType, "dnf", 1, f.now)
	f.result(t, 2, f.raceType, "finished", 2000, f.now)
	f.result(t, 3, f.raceType, "finished", 1000, f.now)
	for _, window := range []string{"all-time", "month", "week"} {
		f.check(t, "friends", window, 0,
			app.LeaderboardRow{UserID: f.users[2], Name: "Racer 2", ElapsedMilliseconds: 2000},
			app.LeaderboardRow{UserID: f.users[1], Name: "Racer 1", ElapsedMilliseconds: 3000})
		f.check(t, "global", window, 0,
			app.LeaderboardRow{UserID: f.users[0], Name: "Racer 0", ElapsedMilliseconds: 500},
			app.LeaderboardRow{UserID: f.users[3], Name: "Racer 3", ElapsedMilliseconds: 1000},
			app.LeaderboardRow{UserID: f.users[2], Name: "Racer 2", ElapsedMilliseconds: 2000},
			app.LeaderboardRow{UserID: f.users[1], Name: "Racer 1", ElapsedMilliseconds: 3000})
		f.check(t, "friends", window, 3) // No friends must not fall back to Global.

		got, err := f.svc.Leaderboard(context.Background(), "global", window, "race", f.raceType, f.users[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range got {
			for _, id := range f.users {
				email := id.String() + "@leaderboard.example"
				if row.Name == email || strings.Contains(row.Name, "@") {
					t.Fatalf("row leaked email %s: %+v", email, row)
				}
			}
		}
	}
}

func TestLeaderboardTieCaseIntegration(t *testing.T) {
	f := seedLeaderboard(t, 3)
	// Clear display_name for user 1 to make them a nameless player
	f.exec(t, "UPDATE users SET display_name = NULL WHERE id = $1", f.users[1])

	// Give all 3 users the exact same elapsed time (2000ms)
	f.result(t, 0, f.raceType, "finished", 2000, f.now)
	f.result(t, 1, f.raceType, "finished", 2000, f.now)
	f.result(t, 2, f.raceType, "finished", 2000, f.now)
	// Additional finished results for user 0 with equal and worse times to verify distinct finishers
	f.result(t, 0, f.raceType, "finished", 3000, f.now)
	f.result(t, 0, f.raceType, "finished", 2000, f.now)

	got, err := f.svc.Leaderboard(context.Background(), "global", "all-time", "race", f.raceType, f.users[0])
	if err != nil {
		t.Fatal(err)
	}
	// Row count equal to distinct finishers (3)
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct finishers, got %d", len(got))
	}
	// Equal times
	for _, row := range got {
		if row.ElapsedMilliseconds != 2000 {
			t.Fatalf("expected elapsed 2000, got %d", row.ElapsedMilliseconds)
		}
	}
	// Ordered by user_id ASC
	for i := 0; i < len(got)-1; i++ {
		if bytes.Compare(got[i].UserID[:], got[i+1].UserID[:]) >= 0 {
			t.Fatalf("rows not ordered by user_id ASC: %s >= %s", got[i].UserID, got[i+1].UserID)
		}
	}
	// Verify nameless player got the shared placeholder rule name
	namelessExpected := app.NewPublicPlayer(f.users[1], pgtype.Text{}).Name
	foundNameless := false
	for _, row := range got {
		if row.UserID == f.users[1] {
			foundNameless = true
			if row.Name != namelessExpected {
				t.Fatalf("got nameless name %q, want %q", row.Name, namelessExpected)
			}
		}
	}
	if !foundNameless {
		t.Fatalf("nameless user not found in results")
	}
}

func TestLeaderboardRouteIntegration(t *testing.T) {
	f := seedLeaderboard(t, 2)
	f.friends(t, 0, 1)
	f.result(t, 1, f.raceType, "finished", 1000, f.now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const secret = "leaderboard-integration-secret-at-least-32-bytes"
	h := internalhttp.NewRouter(ctx, internalhttp.RouterConfig{Queries: db.New(f.pool), JWTSecret: secret, LeaderboardSvc: f.svc})
	token, err := auth.IssueAccessToken(f.users[0], f.users[0].String()+"@leaderboard.example", secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"global", "friends"} {
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodGet, "/leaderboard?scope="+scope+"&race_type="+f.raceType.String(), nil)
			if authenticated {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if !authenticated {
				if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
					t.Fatalf("unauthenticated: %d %v", rr.Code, rr.Header())
				}
				continue
			}
			var got []app.LeaderboardRow
			if rr.Code != http.StatusOK || rr.Header().Get("Location") != "" {
				t.Fatalf("authenticated: %d %s", rr.Code, rr.Body)
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].UserID != f.users[1] || got[0].ElapsedMilliseconds != 1000 || got[0].Name != "Racer 1" {
				t.Fatalf("ranking = %s (%v)", rr.Body, err)
			}
		}
	}
}

func TestMigration000020BackfillSessionMode(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const schema = "migration_000020_test"
	for _, sql := range []string{
		"CREATE SCHEMA " + schema,
		"SET LOCAL search_path = " + schema + ", public",
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}

	// Apply migrations 000001 through 000019 in order.
	entries, err := os.ReadDir("../../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".up.sql") && name < "000020" {
			migration, err := os.ReadFile("../../db/migrations/" + name)
			if err != nil {
				t.Fatalf("read migration %s: %v", name, err)
			}
			if _, err := tx.Exec(ctx, string(migration)); err != nil {
				t.Fatalf("apply migration %s: %v", name, err)
			}
		}
	}

	// Seed users, race_type, rooms, races at schema 000019.
	userID1 := uuid.Must(uuid.NewV7())
	userID2 := uuid.Must(uuid.NewV7())
	for _, u := range []uuid.UUID{userID1, userID2} {
		if _, err := tx.Exec(ctx, "INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'unused', $3)",
			u, u.String()+"@example.com", "Racer "+u.String()); err != nil {
			t.Fatal(err)
		}
	}

	var raceTypeID uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT id FROM race_types WHERE kind = 'distance' LIMIT 1").Scan(&raceTypeID); err != nil {
		t.Fatal(err)
	}

	roomRaceID := uuid.Must(uuid.NewV7())
	roomTTID := uuid.Must(uuid.NewV7())
	roomEmptyID := uuid.Must(uuid.NewV7())

	for _, room := range []struct {
		id   uuid.UUID
		code string
	}{
		{roomRaceID, "RACEAB"},
		{roomTTID, "TTRTRX"},
		{roomEmptyID, "EMPTYX"},
	} {
		if _, err := tx.Exec(ctx, "INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code) VALUES ($1, $2, $3, 'finished', $4)",
			room.id, raceTypeID, userID1, room.code); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	raceNormalID := uuid.Must(uuid.NewV7())
	raceTTID := uuid.Must(uuid.NewV7())

	// Seed Race (time_trial = false) and Time Trial (time_trial = true)
	if _, err := tx.Exec(ctx, "INSERT INTO races (id, room_id, race_type_id, started_at, finished_at, time_trial) VALUES ($1, $2, $3, $4, $5, false)",
		raceNormalID, roomRaceID, raceTypeID, now.Add(-time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO races (id, room_id, race_type_id, started_at, finished_at, time_trial) VALUES ($1, $2, $3, $4, $5, true)",
		raceTTID, roomTTID, raceTypeID, now.Add(-time.Hour), now); err != nil {
		t.Fatal(err)
	}

	// Seed race participants
	part1ID := uuid.Must(uuid.NewV7())
	part2ID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, "INSERT INTO race_participants (id, race_id, user_id, status, finished_at, elapsed_milliseconds) VALUES ($1, $2, $3, 'finished', $4, 2000)",
		part1ID, raceNormalID, userID1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO race_participants (id, race_id, user_id, status, finished_at, elapsed_milliseconds) VALUES ($1, $2, $3, 'finished', $4, 1500)",
		part2ID, raceTTID, userID2, now); err != nil {
		t.Fatal(err)
	}

	// Pre-migration leaderboard queries and row counts
	type preRow struct {
		RaceID              uuid.UUID
		UserID              uuid.UUID
		ElapsedMilliseconds int32
	}
	queryPre := func(isTT bool) []preRow {
		rows, err := tx.Query(ctx, "SELECT r.id, rp.user_id, rp.elapsed_milliseconds FROM race_participants rp JOIN races r ON r.id = rp.race_id WHERE r.time_trial = $1 ORDER BY rp.elapsed_milliseconds", isTT)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var res []preRow
		for rows.Next() {
			var r preRow
			if err := rows.Scan(&r.RaceID, &r.UserID, &r.ElapsedMilliseconds); err != nil {
				t.Fatal(err)
			}
			res = append(res, r)
		}
		return res
	}
	preRaces := queryPre(false)
	preTTs := queryPre(true)

	countRows := func(table string) int {
		var cnt int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		return cnt
	}
	preUserCount := countRows("users")
	preRoomCount := countRows("rooms")
	preRaceCount := countRows("races")
	prePartCount := countRows("race_participants")

	// Apply migration 000020_add_session_mode.up.sql
	upMigration, err := os.ReadFile("../../db/migrations/000020_add_session_mode.up.sql")
	if err != nil {
		t.Fatalf("read migration 000020 up: %v", err)
	}
	if _, err := tx.Exec(ctx, string(upMigration)); err != nil {
		t.Fatalf("apply migration 000020: %v", err)
	}

	// Assert CLM-13: races.mode and rooms.mode backfilled
	var raceNormalMode, raceTTMode string
	if err := tx.QueryRow(ctx, "SELECT mode FROM races WHERE id = $1", raceNormalID).Scan(&raceNormalMode); err != nil {
		t.Fatal(err)
	}
	if raceNormalMode != "race" {
		t.Fatalf("expected raceNormalMode to be 'race', got %q", raceNormalMode)
	}

	if err := tx.QueryRow(ctx, "SELECT mode FROM races WHERE id = $1", raceTTID).Scan(&raceTTMode); err != nil {
		t.Fatal(err)
	}
	if raceTTMode != "time_trial" {
		t.Fatalf("expected raceTTMode to be 'time_trial', got %q", raceTTMode)
	}

	// Verify time_trial boolean column is preserved
	var ttColNormal, ttColTT bool
	if err := tx.QueryRow(ctx, "SELECT time_trial FROM races WHERE id = $1", raceNormalID).Scan(&ttColNormal); err != nil {
		t.Fatal(err)
	}
	if ttColNormal != false {
		t.Fatalf("expected time_trial to stay false for raceNormalID, got %v", ttColNormal)
	}
	if err := tx.QueryRow(ctx, "SELECT time_trial FROM races WHERE id = $1", raceTTID).Scan(&ttColTT); err != nil {
		t.Fatal(err)
	}
	if ttColTT != true {
		t.Fatalf("expected time_trial to stay true for raceTTID, got %v", ttColTT)
	}

	// Verify rooms.mode
	var roomRaceMode, roomTTMode, roomEmptyMode string
	if err := tx.QueryRow(ctx, "SELECT mode FROM rooms WHERE id = $1", roomRaceID).Scan(&roomRaceMode); err != nil {
		t.Fatal(err)
	}
	if roomRaceMode != "race" {
		t.Fatalf("expected roomRaceMode to be 'race', got %q", roomRaceMode)
	}

	if err := tx.QueryRow(ctx, "SELECT mode FROM rooms WHERE id = $1", roomTTID).Scan(&roomTTMode); err != nil {
		t.Fatal(err)
	}
	if roomTTMode != "time_trial" {
		t.Fatalf("expected roomTTMode to be 'time_trial', got %q", roomTTMode)
	}

	if err := tx.QueryRow(ctx, "SELECT mode FROM rooms WHERE id = $1", roomEmptyID).Scan(&roomEmptyMode); err != nil {
		t.Fatal(err)
	}
	if roomEmptyMode != "race" {
		t.Fatalf("expected roomEmptyMode to be 'race', got %q", roomEmptyMode)
	}

	// Assert row counts unchanged
	if postUserCount := countRows("users"); postUserCount != preUserCount {
		t.Fatalf("users row count changed: pre=%d post=%d", preUserCount, postUserCount)
	}
	if postRoomCount := countRows("rooms"); postRoomCount != preRoomCount {
		t.Fatalf("rooms row count changed: pre=%d post=%d", preRoomCount, postRoomCount)
	}
	if postRaceCount := countRows("races"); postRaceCount != preRaceCount {
		t.Fatalf("races row count changed: pre=%d post=%d", preRaceCount, postRaceCount)
	}
	if postPartCount := countRows("race_participants"); postPartCount != prePartCount {
		t.Fatalf("race_participants row count changed: pre=%d post=%d", prePartCount, postPartCount)
	}

	// Assert leaderboard queries return same rows
	queryPost := func(mode string) []preRow {
		rows, err := tx.Query(ctx, "SELECT r.id, rp.user_id, rp.elapsed_milliseconds FROM race_participants rp JOIN races r ON r.id = rp.race_id WHERE r.mode = $1 ORDER BY rp.elapsed_milliseconds", mode)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var res []preRow
		for rows.Next() {
			var r preRow
			if err := rows.Scan(&r.RaceID, &r.UserID, &r.ElapsedMilliseconds); err != nil {
				t.Fatal(err)
			}
			res = append(res, r)
		}
		return res
	}
	postRaces := queryPost("race")
	postTTs := queryPost("time_trial")

	if !reflect.DeepEqual(preRaces, postRaces) {
		t.Fatalf("race leaderboard mismatch post-migration:\npre:  %+v\npost: %+v", preRaces, postRaces)
	}
	if !reflect.DeepEqual(preTTs, postTTs) {
		t.Fatalf("time trial leaderboard mismatch post-migration:\npre:  %+v\npost: %+v", preTTs, postTTs)
	}

	// Assert sentinel race_types row exists
	var sentinelKind, sentinelLabel string
	var sentinelTarget int64
	if err := tx.QueryRow(ctx, "SELECT kind, target_value, label FROM race_types WHERE id = '01950000-0000-7000-8000-000000000009'::UUID").Scan(&sentinelKind, &sentinelTarget, &sentinelLabel); err != nil {
		t.Fatalf("sentinel race_types query failed: %v", err)
	}
	if sentinelKind != "time" || sentinelTarget != 0 || sentinelLabel != "Warmup" {
		t.Fatalf("unexpected sentinel row: kind=%s, target=%d, label=%s", sentinelKind, sentinelTarget, sentinelLabel)
	}

	// Assert check constraints on rooms. Each insert runs in a savepoint so a
	// rejected row does not abort the surrounding transaction, and every join
	// code is valid so only the mode/duration checks can reject a row.
	insertRoom := func(code, mode string, duration any) error {
		if _, err := tx.Exec(ctx, "SAVEPOINT room_check"); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, "INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code, mode, duration_seconds) VALUES ($1, $2, $3, 'waiting', $4, $5, $6)",
			uuid.Must(uuid.NewV7()), raceTypeID, userID1, code, mode, duration)
		rollback := "RELEASE SAVEPOINT room_check"
		if err != nil {
			rollback = "ROLLBACK TO SAVEPOINT room_check"
		}
		if _, rerr := tx.Exec(ctx, rollback); rerr != nil {
			t.Fatal(rerr)
		}
		return err
	}
	if err := insertRoom("WARMAB", "warmup", nil); err == nil {
		t.Fatal("expected error inserting warmup room with NULL duration_seconds")
	}
	if err := insertRoom("RACEXY", "race", 300); err == nil {
		t.Fatal("expected error inserting race room with non-NULL duration_seconds")
	}
	if err := insertRoom("WARMAC", "warmup", 45); err == nil {
		t.Fatal("expected error inserting warmup room with duration 45")
	}
	if err := insertRoom("WARMAD", "warmup", 300); err != nil {
		t.Fatalf("expected valid warmup insert to succeed: %v", err)
	}
	if err := insertRoom("BADMDX", "invalid", nil); err == nil {
		t.Fatal("expected error inserting room with invalid mode")
	}
}
