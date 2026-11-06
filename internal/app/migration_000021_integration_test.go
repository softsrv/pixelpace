//go:build integration

package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Migration 000021 renames the stored activity data (expand step). These tests
// run the real migration files against a transaction-local schema, so the
// shared test database and its migration version are never changed.
//
// Helpers for other tests of 000021 (append them to this file):
//   - migration21Schema builds the schema migrated through 000020;
//   - seedMigration21 seeds one row of every kind under the 000020 names;
//   - applyMigration21 runs 000021 up or down;
//   - migration21Rows / migration21Catalog snapshot data and schema.

const migration21SchemaName = "migration_000021_test"

// migration21Schema opens a transaction whose search_path is a fresh schema
// migrated 000001..000020. Everything is rolled back when the test ends.
func migration21Schema(t *testing.T) pgx.Tx {
	t.Helper()
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	for _, sql := range []string{
		"CREATE SCHEMA " + migration21SchemaName,
		"SET LOCAL search_path = " + migration21SchemaName + ", public",
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir("../../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".up.sql") && name < "000021" {
			migration, err := os.ReadFile("../../db/migrations/" + name)
			if err != nil {
				t.Fatalf("read migration %s: %v", name, err)
			}
			if _, err := tx.Exec(ctx, string(migration)); err != nil {
				t.Fatalf("apply migration %s: %v", name, err)
			}
		}
	}
	return tx
}

// applyMigration21 runs 000021 in the given direction ("up" or "down").
func applyMigration21(t *testing.T, tx pgx.Tx, direction string) {
	t.Helper()
	migration, err := os.ReadFile("../../db/migrations/000021_rename_activity_storage." + direction + ".sql")
	if err != nil {
		t.Fatalf("read migration 000021 %s: %v", direction, err)
	}
	if _, err := tx.Exec(context.Background(), string(migration)); err != nil {
		t.Fatalf("apply migration 000021 %s: %v", direction, err)
	}
}

// migration21Seed holds the ids seedMigration21 wrote.
type migration21Seed struct {
	Users                                 [2]uuid.UUID
	Preset500, IntervalPreset             uuid.UUID
	RaceRoom, TimeTrialRoom, WarmupRoom   uuid.UUID
	Race, TimeTrial, Warmup               uuid.UUID
	RaceParticipant, TimeTrialParticipant uuid.UUID
	QueueEntry, Concept2Post              uuid.UUID
}

var migration21WarmupPreset = uuid.MustParse("01950000-0000-7000-8000-000000000009")

// seedMigration21 writes, under the 000020 names, two users, an interval
// preset with two segments, a race, a time-trial and a warm-up room each with
// its race, participants, a telemetry sample, a queue row and a Concept2 post.
func seedMigration21(t *testing.T, tx pgx.Tx) migration21Seed {
	t.Helper()
	ctx := context.Background()
	s := migration21Seed{
		Users:                [2]uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())},
		Preset500:            uuid.MustParse("01950000-0000-7000-8000-000000000001"),
		IntervalPreset:       uuid.Must(uuid.NewV7()),
		RaceRoom:             uuid.Must(uuid.NewV7()),
		TimeTrialRoom:        uuid.Must(uuid.NewV7()),
		WarmupRoom:           uuid.Must(uuid.NewV7()),
		Race:                 uuid.Must(uuid.NewV7()),
		TimeTrial:            uuid.Must(uuid.NewV7()),
		Warmup:               uuid.Must(uuid.NewV7()),
		RaceParticipant:      uuid.Must(uuid.NewV7()),
		TimeTrialParticipant: uuid.Must(uuid.NewV7()),
		QueueEntry:           uuid.Must(uuid.NewV7()),
		Concept2Post:         uuid.Must(uuid.NewV7()),
	}
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	for i, u := range s.Users {
		exec("INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'unused', $3)",
			u, u.String()+"@migration21.example.com", "Racer "+string(rune('A'+i)))
	}
	exec("INSERT INTO race_types (id, kind, target_value, label) VALUES ($1, 'interval', 2, '2x500m')", s.IntervalPreset)
	exec("INSERT INTO race_type_segments (id, race_type_id, position, target_value) VALUES ($1, $2, 1, 500000), ($3, $2, 2, 500000)",
		uuid.Must(uuid.NewV7()), s.IntervalPreset, uuid.Must(uuid.NewV7()))
	exec(`INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code, mode, race_starts_at) VALUES
		($1, $3, $4, 'finished', 'RACEAB', 'race', $5),
		($2, $3, $4, 'finished', 'TTRACE', 'time_trial', $5)`,
		s.RaceRoom, s.TimeTrialRoom, s.Preset500, s.Users[0], now.Add(-time.Hour))
	exec("INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code, mode, duration_seconds) VALUES ($1, $2, $3, 'in_progress', 'WARMUP', 'warmup', 300)",
		s.WarmupRoom, migration21WarmupPreset, s.Users[1])
	exec("INSERT INTO room_participants (id, room_id, user_id, ready) VALUES ($1, $2, $3, true), ($4, $2, $5, true)",
		uuid.Must(uuid.NewV7()), s.RaceRoom, s.Users[0], uuid.Must(uuid.NewV7()), s.Users[1])
	exec(`INSERT INTO races (id, room_id, race_type_id, started_at, finished_at, time_trial, mode) VALUES
		($1, $4, $7, $9, $10, false, 'race'),
		($2, $5, $7, $9, $10, true, 'time_trial'),
		($3, $6, $8, $9, NULL, false, 'warmup')`,
		s.Race, s.TimeTrial, s.Warmup, s.RaceRoom, s.TimeTrialRoom, s.WarmupRoom, s.Preset500, migration21WarmupPreset, now.Add(-time.Hour), now)
	exec(`INSERT INTO race_participants (id, race_id, user_id, status, finished_at, elapsed_milliseconds, pm5_serial_number) VALUES
		($1, $3, $5, 'finished', $6, 95000, '430000001'),
		($2, $4, $5, 'finished', $6, 92000, NULL),
		($7, $3, $8, 'dnf', NULL, NULL, NULL),
		($9, $10, $8, 'racing', NULL, NULL, NULL)`,
		s.RaceParticipant, s.TimeTrialParticipant, s.Race, s.TimeTrial, s.Users[0], now, uuid.Must(uuid.NewV7()), s.Users[1], uuid.Must(uuid.NewV7()), s.Warmup)
	exec("INSERT INTO telemetry_samples (id, race_id, user_id, elapsed_milliseconds, distance_millimeters, sampled_at) VALUES ($1, $2, $3, 1000, 4000, $4)",
		uuid.Must(uuid.NewV7()), s.Race, s.Users[0], now.Add(-59*time.Minute))
	exec("INSERT INTO quick_match_queue (id, user_id, race_type_id, joined_at) VALUES ($1, $2, $3, $4)",
		s.QueueEntry, s.Users[1], s.Preset500, now)
	exec("INSERT INTO concept2_result_posts (id, user_id, race_id, status, attempts, c2_result_id, posted_at) VALUES ($1, $2, $3, 'posted', 1, 777, $4)",
		s.Concept2Post, s.Users[0], s.Race, now)
	return s
}

// migration21Table reads one table's rows as text in the same column order
// under the 000020 names (old) and the 000021 names (new).
type migration21Table struct{ name, old, new string }

var migration21Tables = []migration21Table{
	{"presets",
		"SELECT (id, kind, target_value, label)::text FROM race_types ORDER BY id",
		"SELECT (id, kind, target_value, label)::text FROM presets ORDER BY id"},
	{"preset_segments",
		"SELECT (id, race_type_id, position, target_value)::text FROM race_type_segments ORDER BY id",
		"SELECT (id, preset_id, position, target_value)::text FROM preset_segments ORDER BY id"},
	{"rooms",
		"SELECT (id, race_type_id, host_user_id, status, join_code, created_at, race_starts_at, mode, duration_seconds)::text FROM rooms ORDER BY id",
		"SELECT (id, preset_id, host_user_id, status, join_code, created_at, race_starts_at, activity_type, duration_seconds)::text FROM rooms ORDER BY id"},
	{"room_participants",
		"SELECT (id, room_id, user_id, ready, joined_at, equipment_ready_at)::text FROM room_participants ORDER BY id",
		"SELECT (id, room_id, user_id, ready, joined_at, equipment_ready_at)::text FROM room_participants ORDER BY id"},
	{"activities",
		"SELECT (id, room_id, race_type_id, started_at, finished_at, time_trial, mode)::text FROM races ORDER BY id",
		"SELECT (id, room_id, preset_id, started_at, finished_at, time_trial, activity_type)::text FROM activities ORDER BY id"},
	{"race_participants",
		"SELECT race_participants::text FROM race_participants ORDER BY id",
		"SELECT race_participants::text FROM race_participants ORDER BY id"},
	{"telemetry_samples",
		"SELECT telemetry_samples::text FROM telemetry_samples ORDER BY id",
		"SELECT telemetry_samples::text FROM telemetry_samples ORDER BY id"},
	{"quick_match_queue",
		"SELECT (id, user_id, race_type_id, joined_at)::text FROM quick_match_queue ORDER BY id",
		"SELECT (id, user_id, preset_id, joined_at)::text FROM quick_match_queue ORDER BY id"},
	{"concept2_result_posts",
		"SELECT concept2_result_posts::text FROM concept2_result_posts ORDER BY id",
		"SELECT concept2_result_posts::text FROM concept2_result_posts ORDER BY id"},
	{"users",
		"SELECT users::text FROM users ORDER BY id",
		"SELECT users::text FROM users ORDER BY id"},
}

// migration21Rows snapshots every table, read under the old or new names.
func migration21Rows(t *testing.T, tx pgx.Tx, newNames bool) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range migration21Tables {
		sql := table.old
		if newNames {
			sql = table.new
		}
		out[table.name] = migration21Strings(t, tx, sql)
	}
	return out
}

func migration21Strings(t *testing.T, tx pgx.Tx, sql string, args ...any) []string {
	t.Helper()
	rows, err := tx.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect %q: %v", sql, err)
	}
	return values
}

// migration21Catalog describes the test schema: tables and views, columns,
// constraints, indexes, user triggers and functions.
func migration21Catalog(t *testing.T, tx pgx.Tx) map[string][]string {
	t.Helper()
	queries := map[string]string{
		"tables": "SELECT table_name || ' ' || table_type FROM information_schema.tables WHERE table_schema = $1 ORDER BY 1",
		"columns": `SELECT table_name || '.' || column_name || ' #' || ordinal_position || ' ' || data_type || ' null=' || is_nullable || ' default=' || coalesce(column_default, '')
			FROM information_schema.columns WHERE table_schema = $1 ORDER BY 1`,
		"constraints": "SELECT conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid) FROM pg_constraint WHERE connamespace = $1::regnamespace ORDER BY 1",
		"indexes":     "SELECT indexdef FROM pg_indexes WHERE schemaname = $1 ORDER BY 1",
		"triggers": `SELECT c.relname || ' ' || tg.tgname FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid
			WHERE c.relnamespace = $1::regnamespace AND NOT tg.tgisinternal ORDER BY 1`,
		"functions": "SELECT proname FROM pg_proc WHERE pronamespace = $1::regnamespace ORDER BY 1",
	}
	out := map[string][]string{}
	for name, sql := range queries {
		out[name] = migration21Strings(t, tx, sql, migration21SchemaName)
	}
	return out
}

func migration21Scalar[T any](t *testing.T, tx pgx.Tx, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := tx.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

// migration21Rejects runs sql inside a savepoint and returns the SQLSTATE it
// failed with ("" when it succeeded). The savepoint is always rolled back.
func migration21Rejects(t *testing.T, tx pgx.Tx, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sp.Rollback(ctx) }()
	_, err = sp.Exec(ctx, sql, args...)
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("exec %q: %v", sql, err)
	}
	return pgErr.Code
}

func migration21Equal(t *testing.T, what string, got, want map[string][]string) {
	t.Helper()
	for name := range want {
		if !reflect.DeepEqual(got[name], want[name]) {
			t.Errorf("%s %s:\n got  %q\n want %q", what, name, got[name], want[name])
		}
	}
	if len(got) != len(want) {
		t.Errorf("%s: %d groups, want %d", what, len(got), len(want))
	}
}

// After 000021 up every seeded row is still there with the same values under
// the new names; the twins equal the old columns; stored activity-type values
// and kinds are unchanged; and the new columns carry the old constraints.
func TestMigration000021UpPreservesDataIntegration(t *testing.T) {
	tx := migration21Schema(t)
	s := seedMigration21(t, tx)
	before := migration21Rows(t, tx, false)
	beforeModes := migration21Strings(t, tx, "SELECT DISTINCT mode FROM races ORDER BY 1")
	beforeKinds := migration21Strings(t, tx, "SELECT kind || ' ' || count(*) FROM race_types GROUP BY kind ORDER BY 1")
	for _, table := range []string{"rooms", "activities", "quick_match_queue", "concept2_result_posts", "presets", "preset_segments"} {
		if len(before[table]) == 0 {
			t.Fatalf("seed wrote no %s", table)
		}
	}

	applyMigration21(t, tx, "up")

	migration21Equal(t, "rows after up", migration21Rows(t, tx, true), before)

	if got := migration21Strings(t, tx, "SELECT DISTINCT activity_type FROM activities ORDER BY 1"); !reflect.DeepEqual(got, beforeModes) {
		t.Errorf("activity types = %q, want %q", got, beforeModes)
	}
	if got := migration21Strings(t, tx, "SELECT DISTINCT activity_type FROM rooms ORDER BY 1"); !reflect.DeepEqual(got, []string{"race", "time_trial", "warmup"}) {
		t.Errorf("room activity types = %q", got)
	}
	if got := migration21Strings(t, tx, "SELECT kind || ' ' || count(*) FROM presets GROUP BY kind ORDER BY 1"); !reflect.DeepEqual(got, beforeKinds) {
		t.Errorf("preset kinds = %q, want %q", got, beforeKinds)
	}
	if n := migration21Scalar[int64](t, tx, "SELECT count(*) FROM rooms WHERE activity_type IS DISTINCT FROM mode OR preset_id IS DISTINCT FROM race_type_id"); n != 0 {
		t.Errorf("%d rooms whose twin columns differ", n)
	}
	if n := migration21Scalar[int64](t, tx, "SELECT count(*) FROM quick_match_queue WHERE preset_id IS DISTINCT FROM race_type_id"); n != 0 {
		t.Errorf("%d queue rows whose twin columns differ", n)
	}
	if got := migration21Scalar[string](t, tx, "SELECT activity_type FROM activities WHERE id = $1", s.TimeTrial); got != "time_trial" {
		t.Errorf("time trial activity_type = %q", got)
	}
	if got := migration21Scalar[uuid.UUID](t, tx, "SELECT preset_id FROM rooms WHERE id = $1", s.WarmupRoom); got != migration21WarmupPreset {
		t.Errorf("warm-up room preset = %s", got)
	}

	// The renamed tables keep their keys and checks.
	constraints := migration21Strings(t, tx, "SELECT conrelid::regclass::text || ' ' || pg_get_constraintdef(oid) FROM pg_constraint WHERE connamespace = $1::regnamespace ORDER BY 1", migration21SchemaName)
	for _, want := range []string{
		"activities FOREIGN KEY (preset_id) REFERENCES presets(id)",
		"activities CHECK ((activity_type = ANY (ARRAY['race'::text, 'time_trial'::text, 'warmup'::text])))",
		"preset_segments FOREIGN KEY (preset_id) REFERENCES presets(id) ON DELETE CASCADE",
		"presets CHECK ((kind = ANY (ARRAY['distance'::text, 'time'::text, 'interval'::text])))",
		"race_participants FOREIGN KEY (race_id) REFERENCES activities(id)",
		"concept2_result_posts FOREIGN KEY (race_id) REFERENCES activities(id) ON DELETE CASCADE",
		"rooms FOREIGN KEY (preset_id) REFERENCES presets(id)",
		"rooms CHECK ((activity_type = ANY (ARRAY['race'::text, 'time_trial'::text, 'warmup'::text])))",
		"rooms CHECK (((activity_type = 'warmup'::text) = (duration_seconds IS NOT NULL)))",
		"quick_match_queue FOREIGN KEY (preset_id) REFERENCES presets(id)",
		"quick_match_queue UNIQUE (user_id, preset_id)",
	} {
		if !migration21Contains(constraints, want) {
			t.Errorf("missing constraint %q in %q", want, constraints)
		}
	}
	for _, col := range []string{"rooms.activity_type", "rooms.preset_id", "quick_match_queue.preset_id", "activities.activity_type", "activities.preset_id"} {
		table, column, _ := strings.Cut(col, ".")
		if nullable := migration21Scalar[string](t, tx, "SELECT is_nullable FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3", migration21SchemaName, table, column); nullable != "NO" {
			t.Errorf("%s nullable = %s", col, nullable)
		}
	}
	if idx := migration21Strings(t, tx, "SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexdef LIKE '%quick_match_queue USING btree (preset_id, joined_at)%'", migration21SchemaName); len(idx) != 1 {
		t.Errorf("queue waiting index on preset_id = %q", idx)
	}

	// A new-binary write with a bad value is refused by the new columns.
	bogus := uuid.Must(uuid.NewV7())
	for _, c := range []struct {
		name, sql, code string
		args            []any
	}{
		{"room unknown preset", "INSERT INTO rooms (id, preset_id, host_user_id, status, join_code, activity_type) VALUES ($1, $2, $3, 'waiting', 'BADPRE', 'race')", "23503", []any{uuid.Must(uuid.NewV7()), bogus, s.Users[0]}},
		{"room unknown activity type", "INSERT INTO rooms (id, preset_id, host_user_id, status, join_code, activity_type) VALUES ($1, $2, $3, 'waiting', 'BADACT', 'sprint')", "23514", []any{uuid.Must(uuid.NewV7()), s.Preset500, s.Users[0]}},
		{"room warm-up without minutes", "INSERT INTO rooms (id, preset_id, host_user_id, status, join_code, activity_type) VALUES ($1, $2, $3, 'counting_down', 'BADWRM', 'warmup')", "23514", []any{uuid.Must(uuid.NewV7()), migration21WarmupPreset, s.Users[0]}},
		{"room preset update to unknown", "UPDATE rooms SET preset_id = $2 WHERE id = $1", "23503", []any{s.RaceRoom, bogus}},
		{"queue unknown preset", "INSERT INTO quick_match_queue (id, user_id, preset_id) VALUES ($1, $2, $3)", "23503", []any{uuid.Must(uuid.NewV7()), s.Users[0], bogus}},
		{"queue duplicate", "INSERT INTO quick_match_queue (id, user_id, preset_id) VALUES ($1, $2, $3)", "23505", []any{uuid.Must(uuid.NewV7()), s.Users[1], s.Preset500}},
		{"activity unknown activity type", "INSERT INTO activities (id, room_id, preset_id, started_at, activity_type) VALUES ($1, $2, $3, NOW(), 'sprint')", "23514", []any{uuid.Must(uuid.NewV7()), s.RaceRoom, s.Preset500}},
		{"segment unknown preset", "INSERT INTO preset_segments (id, preset_id, position, target_value) VALUES ($1, $2, 1, 1)", "23503", []any{uuid.Must(uuid.NewV7()), bogus}},
	} {
		if code := migration21Rejects(t, tx, c.sql, c.args...); code != c.code {
			t.Errorf("%s: SQLSTATE %q, want %q", c.name, code, c.code)
		}
	}
}

// While 000021 is applied, the pre-rename binary's statements still work and
// land under the new names, and the renamed binary's writes read back under
// the old names.
func TestMigration000021OldAndNewBinaryIntegration(t *testing.T) {
	tx := migration21Schema(t)
	s := seedMigration21(t, tx)
	applyMigration21(t, tx, "up")
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

	// Old binary: the 000020-era generated statements, verbatim.
	oldRoom := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code, mode, duration_seconds)
VALUES ($1, $2, $3, 'waiting', $4, $5, $6)`, oldRoom, s.Preset500, s.Users[0], "PREVAB", "time_trial", nil); err != nil {
		t.Fatalf("old InsertRoom: %v", err)
	}
	if got := migration21Scalar[string](t, tx, "SELECT activity_type || ' ' || preset_id FROM rooms WHERE id = $1", oldRoom); got != "time_trial "+s.Preset500.String() {
		t.Errorf("old room under new names = %q", got)
	}
	oldRace := uuid.Must(uuid.NewV7())
	var raceTypeID uuid.UUID
	var timeTrial bool
	var mode string
	if err := tx.QueryRow(ctx, `INSERT INTO races (id, room_id, race_type_id, started_at, mode, time_trial)
VALUES ($1, $2, $3, $4, $5::text, $5::text = 'time_trial')
RETURNING id, room_id, race_type_id, started_at, finished_at, time_trial, mode`, oldRace, oldRoom, s.Preset500, now, "time_trial").Scan(new(uuid.UUID), new(uuid.UUID), &raceTypeID, new(time.Time), new(*time.Time), &timeTrial, &mode); err != nil {
		t.Fatalf("old CreateRace: %v", err)
	}
	if raceTypeID != s.Preset500 || !timeTrial || mode != "time_trial" {
		t.Errorf("old CreateRace returned %s %v %s", raceTypeID, timeTrial, mode)
	}
	if got := migration21Scalar[string](t, tx, "SELECT activity_type || ' ' || preset_id || ' ' || time_trial FROM activities WHERE id = $1", oldRace); got != "time_trial "+s.Preset500.String()+" true" {
		t.Errorf("old race under new names = %q", got)
	}
	var kind string
	var target int64
	if err := tx.QueryRow(ctx, `SELECT r.id, r.room_id, r.race_type_id, r.started_at, r.finished_at, r.time_trial, r.mode, rt.kind, rt.target_value, ro.duration_seconds
FROM races r
JOIN race_types rt ON rt.id = r.race_type_id
JOIN rooms ro ON ro.id = r.room_id
WHERE r.id = $1 FOR UPDATE OF r`, oldRace).Scan(new(uuid.UUID), new(uuid.UUID), new(uuid.UUID), new(time.Time), new(*time.Time), new(bool), &mode, &kind, &target, new(*int32)); err != nil {
		t.Fatalf("old LockRace: %v", err)
	}
	if kind != "distance" || target != 500000 || mode != "time_trial" {
		t.Errorf("old LockRace = %s %d %s", kind, target, mode)
	}
	if tag, err := tx.Exec(ctx, "UPDATE races SET finished_at = $2 WHERE id = $1 AND finished_at IS NULL", oldRace, now.Add(time.Minute)); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("old FinishRace: %v (%d rows)", err, tag.RowsAffected())
	}
	if finished := migration21Scalar[time.Time](t, tx, "SELECT finished_at FROM activities WHERE id = $1", oldRace); !finished.Equal(now.Add(time.Minute)) {
		t.Errorf("finished_at under new names = %s", finished)
	}
	if got := migration21Scalar[string](t, tx, "SELECT id || ' ' || kind || ' ' || target_value || ' ' || label FROM race_types WHERE id = $1", s.Preset500); got != s.Preset500.String()+" distance 500000 500m" {
		t.Errorf("old GetRaceType = %q", got)
	}
	distances := migration21Strings(t, tx, "SELECT label FROM race_types WHERE kind = 'distance' ORDER BY target_value")
	if !reflect.DeepEqual(distances, []string{"500m", "1000m", "2000m", "5000m", "10000m"}) {
		t.Errorf("old ListDistanceRaceTypes = %q (the warm-up preset must stay out)", distances)
	}
	if _, err := tx.Exec(ctx, "UPDATE rooms SET race_type_id = $2 WHERE id = $1", oldRoom, s.IntervalPreset); err != nil {
		t.Fatalf("old UpdateRoomRaceType: %v", err)
	}
	if got := migration21Scalar[uuid.UUID](t, tx, "SELECT preset_id FROM rooms WHERE id = $1", oldRoom); got != s.IntervalPreset {
		t.Errorf("preset_id after old update = %s", got)
	}
	if _, err := tx.Exec(ctx, "UPDATE rooms SET mode = 'race' WHERE id = $1", oldRoom); err != nil {
		t.Fatal(err)
	}
	if got := migration21Scalar[string](t, tx, "SELECT activity_type FROM rooms WHERE id = $1", oldRoom); got != "race" {
		t.Errorf("activity_type after old update = %q", got)
	}
	if got := migration21Scalar[string](t, tx, `SELECT r.race_type_id || ' ' || rt.label || ' ' || rt.kind || ' ' || r.mode
FROM rooms r JOIN race_types rt ON rt.id = r.race_type_id
WHERE r.id = $1`, s.WarmupRoom); got != migration21WarmupPreset.String()+" Warmup time warmup" {
		t.Errorf("old GetRoomLobby = %q", got)
	}
	if got := migration21Scalar[string](t, tx, `SELECT races.id || ' ' || r.mode || ' ' || rt.target_value
FROM rooms r
JOIN race_types rt ON rt.id = r.race_type_id
LEFT JOIN races ON races.room_id = r.id
WHERE r.id = $1`, s.RaceRoom); got != s.Race.String()+" race 500000" {
		t.Errorf("old GetRoomRace = %q", got)
	}
	oldQueue := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, "INSERT INTO quick_match_queue (id, user_id, race_type_id)\nVALUES ($1, $2, $3)", oldQueue, s.Users[0], s.Preset500); err != nil {
		t.Fatalf("old EnqueueQuickMatch: %v", err)
	}
	if got := migration21Scalar[uuid.UUID](t, tx, "SELECT preset_id FROM quick_match_queue WHERE id = $1", oldQueue); got != s.Preset500 {
		t.Errorf("old queue row preset_id = %s", got)
	}
	waiting := migration21Strings(t, tx, "SELECT user_id::text FROM quick_match_queue WHERE race_type_id = $1 ORDER BY joined_at ASC, id ASC FOR UPDATE", s.Preset500)
	if len(waiting) != 2 {
		t.Errorf("old ListWaitingByRaceType = %q", waiting)
	}
	if code := migration21Rejects(t, tx, "INSERT INTO quick_match_queue (id, user_id, race_type_id) VALUES ($1, $2, $3)", uuid.Must(uuid.NewV7()), s.Users[0], s.Preset500); code != "23505" {
		t.Errorf("old duplicate enqueue SQLSTATE = %q, want 23505", code)
	}

	// New binary: writes only the new names; the old names read the same.
	newRoom := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, "INSERT INTO rooms (id, preset_id, host_user_id, status, join_code, activity_type, duration_seconds) VALUES ($1, $2, $3, 'counting_down', 'NEWBXZ', 'warmup', 600)",
		newRoom, migration21WarmupPreset, s.Users[1]); err != nil {
		t.Fatalf("new InsertRoom: %v", err)
	}
	if got := migration21Scalar[string](t, tx, "SELECT mode || ' ' || race_type_id FROM rooms WHERE id = $1", newRoom); got != "warmup "+migration21WarmupPreset.String() {
		t.Errorf("new room under old names = %q", got)
	}
	newRace := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, "INSERT INTO activities (id, room_id, preset_id, started_at, activity_type, time_trial) VALUES ($1, $2, $3, $4, 'warmup', false)",
		newRace, newRoom, migration21WarmupPreset, now); err != nil {
		t.Fatalf("new CreateActivity: %v", err)
	}
	if got := migration21Scalar[string](t, tx, "SELECT mode || ' ' || race_type_id FROM races WHERE id = $1", newRace); got != "warmup "+migration21WarmupPreset.String() {
		t.Errorf("new activity under old names = %q", got)
	}
	if _, err := tx.Exec(ctx, "UPDATE rooms SET activity_type = 'race', duration_seconds = NULL, preset_id = $2 WHERE id = $1", newRoom, s.Preset500); err != nil {
		t.Fatalf("new update: %v", err)
	}
	if got := migration21Scalar[string](t, tx, "SELECT mode || ' ' || race_type_id FROM rooms WHERE id = $1", newRoom); got != "race "+s.Preset500.String() {
		t.Errorf("old names after new update = %q", got)
	}
	newQueue := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, "INSERT INTO quick_match_queue (id, user_id, preset_id) VALUES ($1, $2, $3)", newQueue, s.Users[1], s.IntervalPreset); err != nil {
		t.Fatalf("new Enqueue: %v", err)
	}
	if got := migration21Scalar[uuid.UUID](t, tx, "SELECT race_type_id FROM quick_match_queue WHERE id = $1", newQueue); got != s.IntervalPreset {
		t.Errorf("new queue row race_type_id = %s", got)
	}
	if _, err := tx.Exec(ctx, "UPDATE quick_match_queue SET preset_id = $2 WHERE id = $1", newQueue, migration21WarmupPreset); err != nil {
		t.Fatal(err)
	}
	if got := migration21Scalar[uuid.UUID](t, tx, "SELECT race_type_id FROM quick_match_queue WHERE id = $1", newQueue); got != migration21WarmupPreset {
		t.Errorf("race_type_id after new queue update = %s", got)
	}
	if _, err := tx.Exec(ctx, "UPDATE quick_match_queue SET race_type_id = $2 WHERE id = $1", newQueue, s.IntervalPreset); err != nil {
		t.Fatal(err)
	}
	if got := migration21Scalar[uuid.UUID](t, tx, "SELECT preset_id FROM quick_match_queue WHERE id = $1", newQueue); got != s.IntervalPreset {
		t.Errorf("preset_id after old queue update = %s", got)
	}
	if n := migration21Scalar[int64](t, tx, "SELECT count(*) FROM rooms WHERE activity_type IS DISTINCT FROM mode OR preset_id IS DISTINCT FROM race_type_id"); n != 0 {
		t.Errorf("%d rooms whose twin columns differ", n)
	}
}

// 000021 down after up restores the exact 000020 schema with every row,
// including one the renamed binary wrote; up again gives the same data.
func TestMigration000021RoundTripIntegration(t *testing.T) {
	tx := migration21Schema(t)
	s := seedMigration21(t, tx)
	catalog20 := migration21Catalog(t, tx)
	before := migration21Rows(t, tx, false)

	applyMigration21(t, tx, "up")
	migration21Equal(t, "rows after up", migration21Rows(t, tx, true), before)
	if got := migration21Catalog(t, tx); reflect.DeepEqual(got, catalog20) {
		t.Fatal("up left the catalog unchanged")
	}

	// Rows the renamed binary writes while 000021 is applied.
	ctx := context.Background()
	newRoom := uuid.Must(uuid.NewV7())
	newActivity := uuid.Must(uuid.NewV7())
	newQueue := uuid.Must(uuid.NewV7())
	for _, w := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO rooms (id, preset_id, host_user_id, status, join_code, activity_type) VALUES ($1, $2, $3, 'in_progress', 'NEWRXW', 'time_trial')", []any{newRoom, s.Preset500, s.Users[1]}},
		{"INSERT INTO activities (id, room_id, preset_id, started_at, activity_type, time_trial) VALUES ($1, $2, $3, NOW(), 'time_trial', true)", []any{newActivity, newRoom, s.Preset500}},
		{"INSERT INTO quick_match_queue (id, user_id, preset_id) VALUES ($1, $2, $3)", []any{newQueue, s.Users[0], s.IntervalPreset}},
		{"UPDATE rooms SET activity_type = 'race' WHERE id = $1", []any{s.TimeTrialRoom}},
	} {
		if _, err := tx.Exec(ctx, w.sql, w.args...); err != nil {
			t.Fatalf("new binary write %q: %v", w.sql, err)
		}
	}
	beforeDown := migration21Rows(t, tx, true)

	applyMigration21(t, tx, "down")

	migration21Equal(t, "catalog after down", migration21Catalog(t, tx), catalog20)
	migration21Equal(t, "rows after down", migration21Rows(t, tx, false), beforeDown)
	if got := migration21Scalar[string](t, tx, "SELECT mode || ' ' || race_type_id FROM rooms WHERE id = $1", newRoom); got != "time_trial "+s.Preset500.String() {
		t.Errorf("new-binary room after down = %q", got)
	}
	if got := migration21Scalar[string](t, tx, "SELECT mode FROM rooms WHERE id = $1", s.TimeTrialRoom); got != "race" {
		t.Errorf("new-binary update after down = %q", got)
	}
	if got := migration21Scalar[uuid.UUID](t, tx, "SELECT race_type_id FROM quick_match_queue WHERE id = $1", newQueue); got != s.IntervalPreset {
		t.Errorf("new-binary queue row after down = %s", got)
	}
	if got := migration21Scalar[string](t, tx, "SELECT mode || ' ' || time_trial FROM races WHERE id = $1", newActivity); got != "time_trial true" {
		t.Errorf("new-binary activity after down = %q", got)
	}

	applyMigration21(t, tx, "up")
	migration21Equal(t, "rows after second up", migration21Rows(t, tx, true), beforeDown)
}

func migration21Contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
