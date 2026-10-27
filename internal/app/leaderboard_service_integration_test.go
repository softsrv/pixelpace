//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			"DELETE FROM telemetry_samples WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM race_participants WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM races WHERE room_id = $1",
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
			if _, err := f.pool.Exec(ctx, "DELETE FROM race_types WHERE id = $1", id); err != nil {
				t.Error(err)
			}
		}
	})
	for _, id := range []uuid.UUID{f.raceType, f.otherType} {
		f.exec(t, "INSERT INTO race_types (id, kind, target_value, label) VALUES ($1, 'distance', 500000, 'leaderboard test')", id)
	}
	for i := 0; i < count; i++ {
		id := uuid.Must(uuid.NewV7())
		name := fmt.Sprintf("Racer %d", i)
		f.exec(t, "INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'unused', $3)", id, id.String()+"@leaderboard.example", name)
		f.users = append(f.users, id)
	}
	// Finished rooms do not participate in the active join-code unique index.
	f.exec(t, "INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code) VALUES ($1, $2, $3, 'finished', 'ABCDEF')", f.room, f.raceType, f.users[0])
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
	race := uuid.Must(uuid.NewV7())
	// The race timestamp deliberately differs from the participant's completion.
	f.exec(t, "INSERT INTO races (id, room_id, race_type_id, started_at, finished_at) VALUES ($1, $2, $3, $4, $5)", race, f.room, raceType, finished.Add(-time.Hour), f.now)
	f.exec(t, "INSERT INTO race_participants (id, race_id, user_id, status, finished_at, elapsed_milliseconds) VALUES ($1, $2, $3, $4, $5, $6)", uuid.Must(uuid.NewV7()), race, f.users[user], status, finished, elapsed)
}

func (f *leaderboardFixture) friends(t *testing.T, a, b int) {
	t.Helper()
	f.exec(t, "INSERT INTO friendships (id, user_id_a, user_id_b) VALUES ($1, LEAST($2::uuid, $3::uuid), GREATEST($2::uuid, $3::uuid))", uuid.Must(uuid.NewV7()), f.users[a], f.users[b])
}

func (f *leaderboardFixture) check(t *testing.T, scope, window string, caller int, want ...app.LeaderboardRow) {
	t.Helper()
	got, err := f.svc.Leaderboard(context.Background(), scope, window, f.raceType, f.users[caller])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("%s/%s: got %+v, want %+v", scope, window, got, want)
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

		got, err := f.svc.Leaderboard(context.Background(), "global", window, f.raceType, f.users[0])
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

		got, err := f.svc.Leaderboard(context.Background(), "global", window, f.raceType, f.users[0])
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

	got, err := f.svc.Leaderboard(context.Background(), "global", "all-time", f.raceType, f.users[0])
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
