//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
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
		if _, err := f.pool.Exec(ctx, "INSERT INTO room_participants (id, room_id, user_id) VALUES ($1, $2, $3)", uuid.Must(uuid.NewV7()), f.room, id); err != nil {
			t.Fatal(err)
		}
	}
	f.svc = app.NewRaceService(db.New(f.pool), f.pool, f.fake, app.RaceServiceConfig{
		CountdownDuration: time.Millisecond, StalenessThreshold: threshold,
		Now: func() time.Time { return f.now },
		Sleep: func(_ context.Context, duration time.Duration) error {
			if duration != time.Millisecond {
				t.Fatalf("countdown = %v", duration)
			}
			f.now = f.now.Add(duration)
			return nil
		},
	})
	return f
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
	if _, err := f.svc.Begin(ctx, f.room); !errors.Is(err, pgx.ErrNoRows) {
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
	// User 3 has sent no samples at all; its grace period starts at race start.
	got, err := f.svc.Finalize(context.Background(), f.race.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRaceOrder(t, got, f.users[1])
	for i, want := range []string{"dnf", "finished", "dnf", "dnf"} {
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
			// Stale participant has reached the target; DNF still excludes it.
			f.ingest(t, 0, 1000, 200000, f.now.Add(-threshold-2*time.Second))
			f.ingest(t, 0, 2000, 500000, f.now.Add(-threshold-time.Microsecond))
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
