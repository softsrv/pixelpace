//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	internalhttp "github.com/softsrv/starter/internal/http"
	"github.com/softsrv/starter/internal/realtime"
)

// CLM-5: a race begun from a started room sends each participant a roster of
// every racer first, the unnamed one under their placeholder name.
func TestRaceWSIntegrationRosterIsFirstFrame(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := wsContext(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	queries := db.New(pool)
	named, unnamed := wsUser(), wsUser()
	name := "R" + strings.ReplaceAll(named.ID.String(), "-", "")[20:]
	var roomID uuid.UUID
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, statement := range []struct {
			query string
			id    uuid.UUID
		}{
			{"DELETE FROM race_participants WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)", roomID},
			{"DELETE FROM races WHERE room_id = $1", roomID},
			{"DELETE FROM rooms WHERE id = $1", roomID},
			{"DELETE FROM users WHERE id = $1", named.ID},
			{"DELETE FROM users WHERE id = $1", unnamed.ID},
		} {
			if _, err := pool.Exec(cleanupCtx, statement.query, statement.id); err != nil {
				t.Error(err)
			}
		}
	})
	for _, u := range []uuid.UUID{named.ID, unnamed.ID} {
		if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'unused')", u, u.String()+"@ws.example"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET display_name = $2 WHERE id = $1", named.ID, name); err != nil {
		t.Fatal(err)
	}
	var raceType uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM race_types WHERE kind = 'distance' AND target_value = $1", 500000).Scan(&raceType); err != nil {
		t.Fatal(err)
	}
	rooms := app.NewRoomService(queries, pool)
	roomID, code, err := rooms.CreateRoom(ctx, named.ID, raceType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.JoinRoom(ctx, unnamed.ID, code); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{named.ID, unnamed.ID} {
		if err := rooms.MarkReady(ctx, u, roomID); err != nil {
			t.Fatal(err)
		}
	}
	if err := rooms.StartRace(ctx, named.ID, roomID); err != nil {
		t.Fatal(err)
	}
	b := realtime.NewFake()
	races := app.NewRaceService(queries, pool, b, app.RaceServiceConfig{CountdownDuration: 10 * time.Millisecond})
	if _, err := races.Begin(ctx, roomID); err != nil {
		t.Fatal(err)
	}
	roster, err := rooms.Roster(ctx, unnamed.ID, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if roster.RaceID == nil {
		t.Fatal("room roster has no race id after Begin")
	}
	srv := httptest.NewServer(internalhttp.NewRouter(ctx, internalhttp.RouterConfig{
		Queries: queries, JWTSecret: testJWTSecret, RaceSvc: races, Broadcaster: b,
	}))
	t.Cleanup(srv.Close)
	conn := wsDial(t, ctx, srv.URL+"/races/"+roster.RaceID.String()+"/ws", wsHeaders(t, unnamed))
	got := wsReadRoster(t, ctx, conn)
	want := map[uuid.UUID]string{
		named.ID:   name,
		unnamed.ID: app.NewPublicPlayer(unnamed.ID, pgtype.Text{}).Name,
	}
	if len(got.Players) != len(want) {
		t.Fatalf("roster players = %+v, want %v", got.Players, want)
	}
	for _, p := range got.Players {
		if want[p.UserID] != p.Name {
			t.Fatalf("roster players = %+v, want %v", got.Players, want)
		}
	}
}

func TestRaceWSIntegrationPersistsAndBroadcasts(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := wsContext(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	user, outsider := wsUser(), wsUser()
	roomID, raceID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	// Mirror the RaceService fixture's dependency-ordered, fixture-local cleanup.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, statement := range []struct {
			query string
			id    uuid.UUID
		}{
			{"DELETE FROM telemetry_samples WHERE race_id = $1", raceID},
			{"DELETE FROM race_participants WHERE race_id = $1", raceID},
			{"DELETE FROM races WHERE id = $1", raceID},
			{"DELETE FROM rooms WHERE id = $1", roomID},
			{"DELETE FROM users WHERE id = $1", user.ID},
			{"DELETE FROM users WHERE id = $1", outsider.ID},
		} {
			if _, err := pool.Exec(cleanupCtx, statement.query, statement.id); err != nil {
				t.Error(err)
			}
		}
	})
	var raceType uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM race_types WHERE kind = 'distance' AND target_value = $1", 500000).Scan(&raceType); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{user.ID, outsider.ID} {
		if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'unused')", u, u.String()+"@ws.example"); err != nil {
			t.Fatal(err)
		}
	}
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	var code [6]byte
	for i := range code {
		code[i] = alphabet[int(roomID[10+i])%len(alphabet)]
	}
	if _, err := pool.Exec(ctx, "INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code) VALUES ($1, $2, $3, 'in_progress', $4)", roomID, raceType, user.ID, string(code[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO races (id, room_id, race_type_id, started_at) VALUES ($1, $2, $3, $4)", raceID, roomID, raceType, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO race_participants (id, race_id, user_id, status) VALUES ($1, $2, $3, 'racing')", uuid.Must(uuid.NewV7()), raceID, user.ID); err != nil {
		t.Fatal(err)
	}
	b := realtime.NewFake()
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub, err := b.Subscribe(subCtx, raceID.String())
	if err != nil {
		t.Fatal(err)
	}
	queries := db.New(pool)
	svc := app.NewRaceService(queries, pool, b, app.RaceServiceConfig{})
	srv := httptest.NewServer(internalhttp.NewRouter(ctx, internalhttp.RouterConfig{
		Queries: queries, JWTSecret: testJWTSecret, RaceSvc: svc, Broadcaster: b,
	}))
	t.Cleanup(srv.Close)
	// A signed-in user absent from race_participants is refused before upgrade.
	refused, resp, err := websocket.Dial(ctx, srv.URL+"/races/"+raceID.String()+"/ws", &websocket.DialOptions{HTTPHeader: wsHeaders(t, outsider)})
	if refused != nil {
		_ = refused.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-participant upgrade: response=%v error=%v", resp, err)
	}
	conn := wsDial(t, ctx, srv.URL+"/races/"+raceID.String()+"/ws", wsHeaders(t, user))
	sample := app.TelemetrySample{ElapsedMilliseconds: 1000, DistanceMillimeters: 2500, StrokeRate: 28, Power: 175, SampledAt: time.Now().UTC().Truncate(time.Microsecond)}
	payload, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-sub:
		var got struct {
			UserID uuid.UUID `json:"user_id"`
			app.TelemetrySample
		}
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatal(err)
		}
		if got.UserID != user.ID || !reflect.DeepEqual(got.TelemetrySample, sample) {
			t.Fatalf("broadcast = %+v, want user %s sample %+v", got, user.ID, sample)
		}
	case <-ctx.Done():
		t.Fatal("sample not broadcast")
	}
	var stored app.TelemetrySample
	if err := pool.QueryRow(ctx, "SELECT elapsed_milliseconds, distance_millimeters, stroke_rate, power, sampled_at FROM telemetry_samples WHERE race_id = $1 AND user_id = $2", raceID, user.ID).Scan(&stored.ElapsedMilliseconds, &stored.DistanceMillimeters, &stored.StrokeRate, &stored.Power, &stored.SampledAt); err != nil {
		t.Fatal(err)
	}
	if stored.ElapsedMilliseconds != sample.ElapsedMilliseconds || stored.DistanceMillimeters != sample.DistanceMillimeters || stored.StrokeRate != sample.StrokeRate || stored.Power != sample.Power || !stored.SampledAt.Equal(sample.SampledAt) {
		t.Fatalf("stored = %+v, want %+v", stored, sample)
	}
}
