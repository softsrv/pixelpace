//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/realtime"
)

type raceFixture struct {
	pool  *pgxpool.Pool
	svc   *app.RaceService
	fake  *realtime.Fake
	room  uuid.UUID
	users []uuid.UUID
	race  db.Race
	now   time.Time
	// onSleep runs on every injected sleep after the clock advances, so a
	// test can act between Run's polls.
	onSleep func()
}

func seedRaceRoom(t *testing.T, n int, target int64, threshold time.Duration) *raceFixture {
	t.Helper()
	f := &raceFixture{pool: testDB(t), fake: realtime.NewFake(), room: uuid.Must(uuid.NewV7()), now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	ctx := context.Background()
	var raceType uuid.UUID
	if err := f.pool.QueryRow(ctx, "SELECT id FROM race_types WHERE kind = 'distance' AND target_value = $1", target).Scan(&raceType); err != nil {
		t.Fatal(err)
	}
	// Cleanup is scoped to this fixture, in foreign-key dependency order.
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
			if _, err := f.pool.Exec(ctx, "DELETE FROM users WHERE id = $1", id); err != nil {
				t.Error(err)
			}
		}
	})
	for i := 0; i < n; i++ {
		id := uuid.Must(uuid.NewV7())
		if _, err := f.pool.Exec(ctx, "INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'unused')", id, id.String()+"@race.example"); err != nil {
			t.Fatal(err)
		}
		f.users = append(f.users, id)
	}
	// Use the schema's join-code alphabet, with fixture-local random entropy.
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	var code [6]byte
	entropy := uuid.Must(uuid.NewV7())
	for i := range code {
		code[i] = alphabet[int(entropy[10+i])%len(alphabet)]
	}
	if _, err := f.pool.Exec(ctx, "INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code) VALUES ($1, $2, $3, 'counting_down', $4)", f.room, raceType, f.users[0], string(code[:])); err != nil {
		t.Fatal(err)
	}
	for _, id := range f.users {
		// Every racer's equipment is ready, so Begin goes straight to the countdown.
		if _, err := f.pool.Exec(ctx, "INSERT INTO room_participants (id, room_id, user_id, equipment_ready_at) VALUES ($1, $2, $3, NOW())", uuid.Must(uuid.NewV7()), f.room, id); err != nil {
			t.Fatal(err)
		}
	}
	f.svc = app.NewRaceService(db.New(f.pool), f.pool, f.fake, app.RaceServiceConfig{
		CountdownDuration: time.Millisecond, StalenessThreshold: threshold, RacePollInterval: time.Millisecond,
		Now: func() time.Time { return f.now },
		Sleep: func(ctx context.Context, duration time.Duration) error {
			if duration != time.Millisecond {
				t.Fatalf("sleep = %v", duration)
			}
			f.now = f.now.Add(duration)
			if f.onSleep != nil {
				f.onSleep()
			}
			return ctx.Err()
		},
	})
	return f
}

// roomStatus reads the fixture room's status.
func (f *raceFixture) roomStatus(t *testing.T) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(context.Background(), "SELECT status FROM rooms WHERE id = $1", f.room).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// participantStatuses reads each fixture user's race status, in user order.
func (f *raceFixture) participantStatuses(t *testing.T) []string {
	t.Helper()
	statuses := make([]string, len(f.users))
	for i, id := range f.users {
		if err := f.pool.QueryRow(context.Background(), "SELECT status FROM race_participants WHERE race_id = $1 AND user_id = $2", f.race.ID, id).Scan(&statuses[i]); err != nil {
			t.Fatal(err)
		}
	}
	return statuses
}

// raceFinishedAt reads races.finished_at for the fixture race.
func (f *raceFixture) raceFinishedAt(t *testing.T) pgtype.Timestamptz {
	t.Helper()
	var at pgtype.Timestamptz
	if err := f.pool.QueryRow(context.Background(), "SELECT finished_at FROM races WHERE id = $1", f.race.ID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// sampleCount counts every stored sample of the fixture race.
func (f *raceFixture) sampleCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM telemetry_samples WHERE race_id = $1", f.race.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// subscribe listens on the fixture race's channel until the test ends.
func (f *raceFixture) subscribe(t *testing.T) <-chan []byte {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub, err := f.fake.Subscribe(ctx, f.race.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// drain returns every payload already delivered on sub.
func drain(sub <-chan []byte) [][]byte {
	var got [][]byte
	for {
		select {
		case payload := <-sub:
			got = append(got, payload)
		default:
			return got
		}
	}
}

func (f *raceFixture) begin(t *testing.T) {
	t.Helper()
	var err error
	f.race, err = f.svc.Begin(context.Background(), f.room)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *raceFixture) ingest(t *testing.T, user int, elapsed, distance int64, at time.Time) {
	t.Helper()
	if err := f.svc.Ingest(context.Background(), f.race.ID, f.users[user], app.TelemetrySample{
		ElapsedMilliseconds: elapsed, DistanceMillimeters: distance, StrokeRate: 28, Power: 175, SampledAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertRaceOrder(t *testing.T, got []app.RaceStanding, want ...uuid.UUID) {
	t.Helper()
	ids := make([]uuid.UUID, len(got))
	for i := range got {
		ids[i] = got[i].UserID
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ranking = %v, want %v", ids, want)
	}
}

func TestRaceServiceBegin(t *testing.T) {
	f := seedRaceRoom(t, 4, 500000, time.Minute)
	before := f.now
	f.begin(t)
	ctx := context.Background()
	var status string
	var started time.Time
	if err := f.pool.QueryRow(ctx, "SELECT rooms.status, races.started_at FROM rooms JOIN races ON races.room_id = rooms.id WHERE races.id = $1", f.race.ID).Scan(&status, &started); err != nil {
		t.Fatal(err)
	}
	if status != "in_progress" || !started.Equal(before.Add(time.Millisecond)) || !f.race.StartedAt.Valid {
		t.Fatalf("status=%s started=%v race=%+v", status, started, f.race)
	}
	rows, err := f.pool.Query(ctx, "SELECT user_id, status FROM race_participants WHERE race_id = $1", f.race.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := make(map[uuid.UUID]int)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		if status != "racing" {
			t.Fatalf("participant status = %s", status)
		}
		seen[id]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(f.users) {
		t.Fatalf("participants=%v, want %v", seen, f.users)
	}
	for _, id := range f.users {
		if seen[id] != 1 {
			t.Fatalf("participant %s count=%d", id, seen[id])
		}
	}
	if _, err := f.svc.Begin(ctx, f.room); !errors.Is(err, app.ErrInvalidStatusTransition) {
		t.Fatalf("duplicate begin: %v", err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM races WHERE room_id = $1", f.room).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("race count=%d", count)
	}
}

func TestRaceServiceIngestStoresAndBroadcastsEverySample(t *testing.T) {
	f := seedRaceRoom(t, 1, 500000, time.Minute)
	f.begin(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := f.fake.Subscribe(ctx, f.race.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	samples := []app.TelemetrySample{
		{SampledAt: f.now}, // Zero values must be stored as non-null measurements.
		{ElapsedMilliseconds: 1000, DistanceMillimeters: 2500, StrokeRate: 27, Power: 123, SampledAt: f.now.Add(time.Second)},
		{ElapsedMilliseconds: 2000, DistanceMillimeters: 6000, StrokeRate: 29, Power: 210, SampledAt: f.now.Add(2 * time.Second)},
	}
	for _, sample := range samples {
		if err := f.svc.Ingest(ctx, f.race.ID, f.users[0], sample); err != nil {
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
			if got.UserID != f.users[0] || !reflect.DeepEqual(got.TelemetrySample, sample) {
				t.Fatalf("broadcast=%+v, want %+v", got, sample)
			}
			var frame map[string]any
			if err := json.Unmarshal(payload, &frame); err != nil {
				t.Fatal(err)
			}
			expectedKeys := map[string]bool{
				"user_id":              true,
				"elapsed_milliseconds": true,
				"distance_millimeters": true,
				"stroke_rate":          true,
				"power":                true,
				"sampled_at":           true,
			}
			if len(frame) != len(expectedKeys) {
				t.Fatalf("broadcast frame has %d keys (%v), want exactly %d keys %v", len(frame), frame, len(expectedKeys), expectedKeys)
			}
			for k := range expectedKeys {
				if _, ok := frame[k]; !ok {
					t.Fatalf("broadcast frame missing expected key %q: %v", k, frame)
				}
			}
		case <-time.After(time.Second):
			t.Fatal("sample not broadcast")
		}
	}
	select {
	case <-sub:
		t.Fatal("extra publish")
	default:
	}
	rows, err := f.pool.Query(ctx, "SELECT elapsed_milliseconds, distance_millimeters, stroke_rate, power, sampled_at FROM telemetry_samples WHERE race_id = $1 AND user_id = $2 ORDER BY sampled_at", f.race.ID, f.users[0])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var elapsed, distance pgtype.Int8
		var stroke, power pgtype.Int4
		var at time.Time
		if err := rows.Scan(&elapsed, &distance, &stroke, &power, &at); err != nil {
			t.Fatal(err)
		}
		if count >= len(samples) {
			t.Fatal("extra stored sample")
		}
		want := samples[count]
		if !elapsed.Valid || !distance.Valid || !stroke.Valid || !power.Valid || elapsed.Int64 != want.ElapsedMilliseconds || distance.Int64 != want.DistanceMillimeters || stroke.Int32 != want.StrokeRate || power.Int32 != want.Power || !at.Equal(want.SampledAt) {
			t.Fatalf("stored sample %d differs: elapsed=%+v distance=%+v stroke=%+v power=%+v at=%v", count, elapsed, distance, stroke, power, at)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != len(samples) {
		t.Fatalf("stored %d of %d", count, len(samples))
	}
}

func TestRaceServiceLiveRankingLatestAndTieBreak(t *testing.T) {
	f := seedRaceRoom(t, 3, 500000, time.Minute)
	f.begin(t)
	f.now = f.now.Add(10 * time.Second)
	f.ingest(t, 0, 6000, 100000, f.now)
	f.ingest(t, 1, 9000, 200000, f.now)
	f.ingest(t, 2, 8000, 200000, f.now)
	// An older sample arriving last must not override the latest sample, even
	// when it has a greater distance. Ranking must not select MAX(distance).
	f.ingest(t, 0, 5000, 400000, f.now.Add(-time.Second))
	got, err := f.svc.LiveRanking(context.Background(), f.race.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRaceOrder(t, got, f.users[2], f.users[1], f.users[0])
	if got[2].DistanceMillimeters != 100000 || got[2].ElapsedMilliseconds != 6000 {
		t.Fatalf("latest sample not used: %+v", got[2])
	}
}

func TestRaceServiceWinnerAndFinalize(t *testing.T) {
	for _, target := range []int64{500000, 1000000} {
		t.Run(strconv.FormatInt(target, 10)+"mm", func(t *testing.T) {
			f := seedRaceRoom(t, 3, target, time.Minute)
			f.begin(t)
			ctx := context.Background()
			start := f.now
			f.now = start.Add(30 * time.Second)
			f.ingest(t, 0, 10000, target-1, start.Add(10*time.Second))
			if _, err := f.svc.Winner(ctx, f.race.ID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("winner below target: %v", err)
			}
			// Slow finisher arrives first; first-to-target uses elapsed, not ingestion order.
			f.ingest(t, 0, 20000, target, start.Add(20*time.Second))
			f.ingest(t, 1, 15000, target+1, start.Add(15*time.Second))
			f.ingest(t, 1, 25000, target+50000, start.Add(25*time.Second))
			f.ingest(t, 2, 28000, target, start.Add(28*time.Second))
			winner, err := f.svc.Winner(ctx, f.race.ID)
			if err != nil || winner != f.users[1] {
				t.Fatalf("winner=%v err=%v", winner, err)
			}
			got, err := f.svc.Finalize(ctx, f.race.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRaceOrder(t, got, f.users[1], f.users[0], f.users[2])
			for i, want := range []int64{15000, 20000, 28000} {
				if got[i].ElapsedMilliseconds != want || got[i].DistanceMillimeters != target {
					t.Fatalf("final standing=%+v, want elapsed %d target %d", got[i], want, target)
				}
				var status string
				var elapsed int32
				var at time.Time
				if err := f.pool.QueryRow(ctx, "SELECT status, elapsed_milliseconds, finished_at FROM race_participants WHERE race_id = $1 AND user_id = $2", f.race.ID, got[i].UserID).Scan(&status, &elapsed, &at); err != nil {
					t.Fatal(err)
				}
				if status != "finished" || int64(elapsed) != want || !at.Equal(start.Add(time.Duration(want)*time.Millisecond)) {
					t.Fatalf("finisher status=%s elapsed=%d at=%v", status, elapsed, at)
				}
			}
			var finished time.Time
			if err := f.pool.QueryRow(ctx, "SELECT finished_at FROM races WHERE id = $1", f.race.ID).Scan(&finished); err != nil {
				t.Fatal(err)
			}
			if !finished.Equal(f.now) {
				t.Fatalf("finished_at=%v, want %v", finished, f.now)
			}
			// The room leaves in_progress in the same transaction.
			if status := f.roomStatus(t); status != "finished" {
				t.Fatalf("room status=%s, want finished", status)
			}
			f.now = f.now.Add(time.Hour)
			again, err := f.svc.Finalize(ctx, f.race.ID)
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Fatalf("repeat finalize=%v err=%v", again, err)
			}
		})
	}
}

func TestRaceServiceFinalizeSweepsDNF(t *testing.T) {
	f := seedRaceRoom(t, 4, 500000, 7*time.Second)
	f.begin(t)
	f.now = f.now.Add(time.Minute)
	f.ingest(t, 0, 1000, 500000, f.now.Add(-8*time.Second))
	f.ingest(t, 1, 2000, 500000, f.now)
	f.ingest(t, 2, 3000, 499999, f.now)
	// User 0 reached the target before going silent, so stays a finisher.
	// User 3 has sent no samples at all; its grace period starts at race start.
	got, err := f.svc.Finalize(context.Background(), f.race.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRaceOrder(t, got, f.users[0], f.users[1])
	for i, want := range []string{"finished", "finished", "dnf", "dnf"} {
		var status string
		if err := f.pool.QueryRow(context.Background(), "SELECT status FROM race_participants WHERE race_id = $1 AND user_id = $2", f.race.ID, f.users[i]).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Fatalf("user %d status=%s, want %s", i, status, want)
		}
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM telemetry_samples WHERE race_id = $1", f.race.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("finalize retained %d samples, want 3", count)
	}
}

func TestRaceServiceDNFExcludesAndRetainsSamples(t *testing.T) {
	for _, threshold := range []time.Duration{5 * time.Second, 12 * time.Second} {
		t.Run(threshold.String(), func(t *testing.T) {
			f := seedRaceRoom(t, 3, 500000, threshold)
			f.begin(t)
			f.now = f.now.Add(time.Minute)
			// Stale participant short of the target; DNF excludes it.
			f.ingest(t, 0, 1000, 200000, f.now.Add(-threshold-2*time.Second))
			f.ingest(t, 0, 2000, 499999, f.now.Add(-threshold-time.Microsecond))
			// Exactly at the threshold remains eligible (strictly older is stale).
			f.ingest(t, 1, 4000, 500000, f.now.Add(-threshold))
			// A historical stale sample must not disqualify a currently active participant.
			f.ingest(t, 2, 1000, 100000, f.now.Add(-threshold-time.Second))
			f.ingest(t, 2, 5000, 500000, f.now)
			ctx := context.Background()
			countSamples := func() int {
				var count int
				if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM telemetry_samples WHERE race_id = $1 AND user_id = $2", f.race.ID, f.users[0]).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}
			before := countSamples()
			live, err := f.svc.LiveRanking(ctx, f.race.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRaceOrder(t, live, f.users[1], f.users[2])
			for i, want := range []string{"dnf", "racing", "racing"} {
				var status string
				if err := f.pool.QueryRow(ctx, "SELECT status FROM race_participants WHERE race_id = $1 AND user_id = $2", f.race.ID, f.users[i]).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != want {
					t.Fatalf("user %d status=%s want=%s", i, status, want)
				}
			}
			final, err := f.svc.Finalize(ctx, f.race.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRaceOrder(t, final, f.users[1], f.users[2])
			if after := countSamples(); before != 2 || after != before {
				t.Fatalf("DNF samples before=%d after=%d", before, after)
			}
		})
	}
}

func TestRaceServiceRoomRace(t *testing.T) {
	f := seedRaceRoom(t, 3, 500000, time.Minute)
	ctx := context.Background()

	view, err := f.svc.RoomRace(ctx, f.room, f.users[1])
	if err != nil {
		t.Fatal(err)
	}
	if view.RaceID != nil || view.RaceStartsAt != nil || view.RoomID != f.room || view.Label != "500m" || view.TargetMillimeters != 500000 {
		t.Fatalf("counting-down view = %+v", view)
	}
	if got := racerIDs(view.Racers); !reflect.DeepEqual(got, f.users) {
		t.Fatalf("racers = %v, want %v", got, f.users)
	}
	for _, racer := range view.Racers {
		if !racer.EquipmentReady || racer.Name == "" {
			t.Fatalf("racer = %+v, want a named racer with equipment ready", racer)
		}
	}

	f.begin(t)
	view, err = f.svc.RoomRace(ctx, f.room, f.users[2])
	if err != nil {
		t.Fatal(err)
	}
	if view.RaceID == nil || *view.RaceID != f.race.ID {
		t.Fatalf("race ID = %v, want %s", view.RaceID, f.race.ID)
	}
	if view.RaceStartsAt == nil || !view.RaceStartsAt.Equal(f.race.StartedAt.Time) {
		t.Fatalf("race starts at = %v, want the race's start %v", view.RaceStartsAt, f.race.StartedAt.Time)
	}
	if got := racerIDs(view.Racers); !reflect.DeepEqual(got, f.users) {
		t.Fatalf("racers after begin = %v, want %v", got, f.users)
	}
	if view.Finished {
		t.Fatalf("in-progress view reports Finished")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE rooms SET status = 'finished' WHERE id = $1", f.room); err != nil {
		t.Fatal(err)
	}
	view, err = f.svc.RoomRace(ctx, f.room, f.users[0])
	if err != nil || !view.Finished {
		t.Fatalf("finished room view = %+v err=%v, want Finished", view, err)
	}

	if _, err := f.svc.RoomRace(ctx, f.room, uuid.Must(uuid.NewV7())); !errors.Is(err, app.ErrParticipantNotFound) {
		t.Fatalf("non-participant err = %v, want ErrParticipantNotFound", err)
	}
	if _, err := f.svc.RoomRace(ctx, uuid.Must(uuid.NewV7()), f.users[0]); !errors.Is(err, app.ErrRoomNotFound) {
		t.Fatalf("missing room err = %v, want ErrRoomNotFound", err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE rooms SET status = 'waiting' WHERE id = $1", f.room); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RoomRace(ctx, f.room, f.users[0]); !errors.Is(err, app.ErrRaceNotStarted) {
		t.Fatalf("waiting room err = %v, want ErrRaceNotStarted", err)
	}
}

func racerIDs(racers []app.RaceRacer) []uuid.UUID {
	ids := make([]uuid.UUID, len(racers))
	for i, racer := range racers {
		ids[i] = racer.UserID
	}
	return ids
}

// finishedFrames returns the {"type":"finished"} frames among payloads.
func finishedFrames(t *testing.T, payloads [][]byte) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, payload := range payloads {
		var frame map[string]any
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] == "finished" {
			frames = append(frames, frame)
		}
	}
	return frames
}

func TestRaceServiceRunFinalizesOnceEveryoneIsDone(t *testing.T) {
	const target = 500000
	f := seedRaceRoom(t, 3, target, 10*time.Millisecond)
	f.begin(t)
	sub := f.subscribe(t)
	f.ingest(t, 0, 9000, target, f.now)
	polls := 0
	f.onSleep = func() {
		polls++
		switch {
		case polls <= 3:
			// User 1 is fresh and short of the target: the race goes on.
			f.ingest(t, 1, int64(polls)*1000, target/2, f.now)
		case polls == 4:
			f.ingest(t, 1, 12000, target, f.now)
		}
		// User 2 never sends anything and goes stale after the threshold.
	}
	if err := f.svc.Run(context.Background(), f.race.ID); err != nil {
		t.Fatal(err)
	}
	if polls <= 10 {
		t.Fatalf("Run finished after %d polls, before user 2 went stale", polls)
	}
	if got := f.participantStatuses(t); !reflect.DeepEqual(got, []string{"finished", "finished", "dnf"}) {
		t.Fatalf("statuses=%v", got)
	}
	if at := f.raceFinishedAt(t); !at.Valid {
		t.Fatal("races.finished_at not set")
	}
	if status := f.roomStatus(t); status != "finished" {
		t.Fatalf("room status=%s, want finished", status)
	}
	frames := finishedFrames(t, drain(sub))
	if len(frames) != 1 || frames[0]["race_id"] != f.race.ID.String() {
		t.Fatalf("finished frames=%v, want one for race %s", frames, f.race.ID)
	}
	// An already-finalized race returns at once and never publishes again.
	f.onSleep = func() { t.Fatal("Run polled a finished race") }
	if err := f.svc.Run(context.Background(), f.race.ID); err != nil {
		t.Fatal(err)
	}
	if extra := drain(sub); len(extra) != 0 {
		t.Fatalf("second Run published %d frames", len(extra))
	}
}

func TestRaceServiceRunFinalizesWhenEveryoneIsDNF(t *testing.T) {
	f := seedRaceRoom(t, 2, 500000, 5*time.Millisecond)
	f.begin(t)
	sub := f.subscribe(t)
	f.ingest(t, 0, 1000, 1000, f.now)
	if err := f.svc.Run(context.Background(), f.race.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.participantStatuses(t); !reflect.DeepEqual(got, []string{"dnf", "dnf"}) {
		t.Fatalf("statuses=%v", got)
	}
	if at := f.raceFinishedAt(t); !at.Valid {
		t.Fatal("races.finished_at not set")
	}
	if status := f.roomStatus(t); status != "finished" {
		t.Fatalf("room status=%s, want finished", status)
	}
	if frames := finishedFrames(t, drain(sub)); len(frames) != 1 {
		t.Fatalf("finished frames=%v, want one", frames)
	}
}

func TestRaceServiceRunKeepsPollingUntilCancelled(t *testing.T) {
	const target = 500000
	f := seedRaceRoom(t, 2, target, 5*time.Millisecond)
	f.begin(t)
	sub := f.subscribe(t)
	f.ingest(t, 0, 9000, target, f.now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	polls := 0
	f.onSleep = func() {
		polls++
		// User 1 keeps rowing short of the target, so never goes stale.
		f.ingest(t, 1, int64(polls)*1000, target-1, f.now)
		if polls == 20 {
			cancel()
		}
	}
	err := f.svc.Run(ctx, f.race.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err=%v, want context.Canceled", err)
	}
	if polls != 20 {
		t.Fatalf("Run polled %d times, want 20", polls)
	}
	if at := f.raceFinishedAt(t); at.Valid {
		t.Fatalf("races.finished_at=%v, want NULL", at.Time)
	}
	if got := f.participantStatuses(t); !reflect.DeepEqual(got, []string{"racing", "racing"}) {
		t.Fatalf("statuses=%v", got)
	}
	if status := f.roomStatus(t); status != "in_progress" {
		t.Fatalf("room status=%s, want in_progress", status)
	}
	if frames := finishedFrames(t, drain(sub)); len(frames) != 0 {
		t.Fatalf("finished frames=%v, want none", frames)
	}
}

func TestRaceServiceFinisherWhoGoesSilentKeepsFinish(t *testing.T) {
	const target = 500000
	threshold := 7 * time.Second
	f := seedRaceRoom(t, 2, target, threshold)
	f.begin(t)
	ctx := context.Background()
	start := f.race.StartedAt.Time
	f.now = start.Add(90 * time.Second)
	f.ingest(t, 0, 90000, target, f.now)
	// User 0 sends nothing more, well past the threshold, while user 1 rows on.
	f.now = f.now.Add(threshold + time.Second)
	f.ingest(t, 1, 98000, target-100000, f.now)
	live, err := f.svc.LiveRanking(ctx, f.race.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRaceOrder(t, live, f.users[0], f.users[1])
	if got := f.participantStatuses(t); !reflect.DeepEqual(got, []string{"racing", "racing"}) {
		t.Fatalf("statuses after live ranking=%v", got)
	}
	// User 1 reaches the target during Run's first wait.
	reached := false
	f.onSleep = func() {
		if !reached {
			reached = true
			f.ingest(t, 1, 120000, target, f.now)
		}
	}
	if err := f.svc.Run(ctx, f.race.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.participantStatuses(t); !reflect.DeepEqual(got, []string{"finished", "finished"}) {
		t.Fatalf("statuses=%v", got)
	}
	results, err := f.svc.Results(ctx, f.room, f.users[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Results) != 2 {
		t.Fatalf("results=%+v", results)
	}
	first, second := results.Results[0], results.Results[1]
	if first.UserID != f.users[0] || first.Rank == nil || *first.Rank != 1 || first.ElapsedMilliseconds == nil || *first.ElapsedMilliseconds != 90000 {
		t.Fatalf("first=%+v, want user 0 rank 1 in 90000ms", first)
	}
	if second.UserID != f.users[1] || second.Rank == nil || *second.Rank != 2 || second.ElapsedMilliseconds == nil || *second.ElapsedMilliseconds != 120000 {
		t.Fatalf("second=%+v, want user 1 rank 2 in 120000ms", second)
	}
}

func TestRaceServiceIngestAfterFinishIsDropped(t *testing.T) {
	const target = 500000
	f := seedRaceRoom(t, 2, target, 5*time.Millisecond)
	f.begin(t)
	ctx := context.Background()
	f.ingest(t, 0, 9000, target, f.now)
	f.ingest(t, 1, 9000, 1000, f.now)
	if err := f.svc.Run(ctx, f.race.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.participantStatuses(t); !reflect.DeepEqual(got, []string{"finished", "dnf"}) {
		t.Fatalf("statuses=%v", got)
	}
	before, err := f.svc.Results(ctx, f.room, f.users[1])
	if err != nil {
		t.Fatal(err)
	}
	samples := f.sampleCount(t)
	sub := f.subscribe(t)
	// The DNF racer's late sample would reach the target; it is refused
	// without an error, so the websocket reader keeps the socket open.
	if err := f.svc.Ingest(ctx, f.race.ID, f.users[1], app.TelemetrySample{
		ElapsedMilliseconds: 20000, DistanceMillimeters: target, SampledAt: f.now,
	}); err != nil {
		t.Fatalf("late ingest err=%v, want nil", err)
	}
	if got := f.sampleCount(t); got != samples {
		t.Fatalf("samples=%d after late ingest, want %d", got, samples)
	}
	if published := drain(sub); len(published) != 0 {
		t.Fatalf("late ingest published %d frames", len(published))
	}
	after, err := f.svc.Results(ctx, f.room, f.users[1])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("results changed by late sample:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestRaceServiceResults(t *testing.T) {
	const target = 500000
	f := seedRaceRoom(t, 5, target, time.Minute)
	ctx := context.Background()
	names := []string{"ResAlpha", "ResBravo", "", "ResDelta", "ResEcho"}
	for i, name := range names {
		if name == "" {
			continue // user 2 keeps no display name and gets a placeholder.
		}
		name += f.users[i].String()[:8]
		names[i] = name
		if _, err := f.pool.Exec(ctx, "UPDATE users SET display_name = $1 WHERE id = $2", name, f.users[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Results(ctx, f.room, f.users[0]); !errors.Is(err, app.ErrRaceNotStarted) {
		t.Fatalf("results before begin err=%v, want ErrRaceNotStarted", err)
	}
	f.begin(t)
	start := f.race.StartedAt.Time
	f.now = start.Add(50 * time.Second)
	f.ingest(t, 0, 20000, target, start.Add(20*time.Second))
	f.ingest(t, 1, 15000, target+1, start.Add(15*time.Second))
	f.ingest(t, 2, 30000, 300000, start.Add(30*time.Second))
	f.ingest(t, 4, 40000, 400000, start.Add(40*time.Second))
	// User 3 sends nothing.
	if _, err := f.svc.Finalize(ctx, f.race.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Results(ctx, f.room, f.users[3])
	if err != nil {
		t.Fatal(err)
	}
	if got.RaceID != f.race.ID || !got.Finished || got.TargetMillimeters != target {
		t.Fatalf("results header=%+v", got)
	}
	ptr := func(v int64) *int64 { return &v }
	rank := func(v int) *int { return &v }
	want := []app.RaceResult{
		{PublicPlayer: app.PublicPlayer{UserID: f.users[1], Name: names[1]}, Rank: rank(1), Status: "finished", ElapsedMilliseconds: ptr(15000), DistanceMillimeters: target, PaceMillisecondsPer500m: ptr(15000)},
		{PublicPlayer: app.PublicPlayer{UserID: f.users[0], Name: names[0]}, Rank: rank(2), Status: "finished", ElapsedMilliseconds: ptr(20000), DistanceMillimeters: target, PaceMillisecondsPer500m: ptr(20000)},
		{PublicPlayer: app.PublicPlayer{UserID: f.users[4], Name: names[4]}, Status: "dnf", DistanceMillimeters: 400000, PaceMillisecondsPer500m: ptr(40000 * 500000 / 400000)},
		{PublicPlayer: app.NewPublicPlayer(f.users[2], pgtype.Text{}), Status: "dnf", DistanceMillimeters: 300000, PaceMillisecondsPer500m: ptr(30000 * 500000 / 300000)},
		{PublicPlayer: app.PublicPlayer{UserID: f.users[3], Name: names[3]}, Status: "dnf", DistanceMillimeters: 0},
	}
	if !reflect.DeepEqual(got.Results, want) {
		gotJSON, _ := json.Marshal(got.Results)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("results\n got %s\nwant %s", gotJSON, wantJSON)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "@") {
		t.Fatalf("results leak an email: %s", payload)
	}
	if _, err := f.svc.Results(ctx, f.room, uuid.Must(uuid.NewV7())); !errors.Is(err, app.ErrParticipantNotFound) {
		t.Fatalf("stranger err=%v, want ErrParticipantNotFound", err)
	}
}

func TestRaceServiceResultsSingleFinisherIsRankOne(t *testing.T) {
	const target = 500000
	f := seedRaceRoom(t, 1, target, time.Minute)
	f.begin(t)
	ctx := context.Background()
	start := f.race.StartedAt.Time
	f.now = start.Add(2 * time.Minute)
	f.ingest(t, 0, 100000, target, start.Add(100*time.Second))
	// The finisher is still fresh, so reaching the target alone ends the race.
	f.onSleep = func() { t.Fatal("Run polled once every racer had reached the target") }
	if err := f.svc.Run(ctx, f.race.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Results(ctx, f.room, f.users[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 1 || got.Results[0].Rank == nil || *got.Results[0].Rank != 1 || got.Results[0].Status != "finished" {
		t.Fatalf("single racer results=%+v", got.Results)
	}
}
