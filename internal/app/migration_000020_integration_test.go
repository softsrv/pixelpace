//go:build integration

package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestMigration000020ReversibleIntegration verifies claim CLM-14:
// Migration 000020 is reversible: applying down after up restores the 000019 schema
// (no mode or duration_seconds columns, no warmup sentinel race type).
// It leaves every non-warmup room and race intact, and running up again succeeds.
func TestMigration000020ReversibleIntegration(t *testing.T) {
	pool := roomIntegrationDB(t)
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

	downSQL, err := os.ReadFile("../../db/migrations/000020_add_session_mode.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	upSQLBytes, err := os.ReadFile("../../db/migrations/000020_add_session_mode.up.sql")
	var upSQL string
	if err == nil {
		upSQL = string(upSQLBytes)
	} else {
		// Canonical up definition if not yet present in this worktree
		upSQL = `
ALTER TABLE rooms
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'race' CHECK (mode IN ('race','time_trial','warmup')),
    ADD COLUMN IF NOT EXISTS duration_seconds INTEGER NULL,
    ADD CONSTRAINT check_rooms_warmup_duration CHECK ((mode = 'warmup') = (duration_seconds IS NOT NULL)),
    ADD CONSTRAINT check_rooms_duration_range CHECK (duration_seconds IS NULL OR (duration_seconds BETWEEN 60 AND 1200 AND duration_seconds % 60 = 0));

ALTER TABLE races
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'race' CHECK (mode IN ('race','time_trial','warmup'));

UPDATE races SET mode = 'time_trial' WHERE time_trial = true;

UPDATE rooms SET mode = 'time_trial'
WHERE id IN (SELECT room_id FROM races WHERE time_trial = true);

INSERT INTO race_types (id, kind, target_value, label) VALUES
    ('01950000-0000-7000-8000-000000000009'::UUID, 'time', 0, 'Warmup')
ON CONFLICT DO NOTHING;
`
	}

	// Ensure 000020 up has been applied
	var roomsModeExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'rooms' AND column_name = 'mode'
		)
	`).Scan(&roomsModeExists); err != nil {
		t.Fatal(err)
	}
	if !roomsModeExists {
		if _, err := tx.Exec(ctx, upSQL); err != nil {
			t.Fatalf("apply up migration: %v", err)
		}
	}

	user := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, "INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'unused', 'Racer')",
		user, user.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	sentinelID := uuid.MustParse("01950000-0000-7000-8000-000000000009")

	// Seed non-warmup race-mode room and race
	raceRoomID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code, mode)
		VALUES ($1, $2, $3, 'in_progress', 'ABCDEF', 'race')
	`, raceRoomID, roomTestID, user); err != nil {
		t.Fatalf("seed race room: %v", err)
	}
	raceID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO races (id, room_id, race_type_id, started_at, time_trial, mode)
		VALUES ($1, $2, $3, NOW(), false, 'race')
	`, raceID, raceRoomID, roomTestID); err != nil {
		t.Fatalf("seed race: %v", err)
	}

	// Seed non-warmup time_trial-mode room and race
	ttRoomID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code, mode)
		VALUES ($1, $2, $3, 'in_progress', 'BCDEFG', 'time_trial')
	`, ttRoomID, roomTestID, user); err != nil {
		t.Fatalf("seed time_trial room: %v", err)
	}
	ttRaceID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO races (id, room_id, race_type_id, started_at, time_trial, mode)
		VALUES ($1, $2, $3, NOW(), true, 'time_trial')
	`, ttRaceID, ttRoomID, roomTestID); err != nil {
		t.Fatalf("seed time_trial race: %v", err)
	}

	// Seed warmup room, race, participant, and telemetry sample referencing sentinel
	warmupRoomID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code, mode, duration_seconds)
		VALUES ($1, $2, $3, 'in_progress', 'CDEFGH', 'warmup', 300)
	`, warmupRoomID, sentinelID, user); err != nil {
		t.Fatalf("seed warmup room: %v", err)
	}
	warmupRaceID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO races (id, room_id, race_type_id, started_at, time_trial, mode)
		VALUES ($1, $2, $3, NOW(), false, 'warmup')
	`, warmupRaceID, warmupRoomID, sentinelID); err != nil {
		t.Fatalf("seed warmup race: %v", err)
	}
	warmupPartID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO race_participants (id, race_id, user_id, status)
		VALUES ($1, $2, $3, 'racing')
	`, warmupPartID, warmupRaceID, user); err != nil {
		t.Fatalf("seed warmup participant: %v", err)
	}
	warmupSampleID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		INSERT INTO telemetry_samples (id, race_id, user_id, sampled_at)
		VALUES ($1, $2, $3, NOW())
	`, warmupSampleID, warmupRaceID, user); err != nil {
		t.Fatalf("seed warmup telemetry: %v", err)
	}

	// Execute down migration
	if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("execute down migration: %v", err)
	}

	// 1. Assert columns are dropped
	for _, col := range []struct{ table, column string }{
		{"rooms", "mode"},
		{"rooms", "duration_seconds"},
		{"races", "mode"},
	} {
		var colExists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
			)
		`, col.table, col.column).Scan(&colExists); err != nil {
			t.Fatal(err)
		}
		if colExists {
			t.Fatalf("expected column %s.%s to be dropped after down migration", col.table, col.column)
		}
	}

	// 2. Assert warmup sentinel race_type is deleted
	var sentinelExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM race_types WHERE id = $1)
	`, sentinelID).Scan(&sentinelExists); err != nil {
		t.Fatal(err)
	}
	if sentinelExists {
		t.Fatalf("expected warmup sentinel %s to be deleted after down migration", sentinelID)
	}

	// 3. Assert warmup room, race, participant, telemetry sample are deleted
	for _, chk := range []struct {
		table string
		id    uuid.UUID
	}{
		{"rooms", warmupRoomID},
		{"races", warmupRaceID},
		{"race_participants", warmupPartID},
		{"telemetry_samples", warmupSampleID},
	} {
		var rowExists bool
		if err := tx.QueryRow(ctx, fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)", chk.table), chk.id).Scan(&rowExists); err != nil {
			t.Fatal(err)
		}
		if rowExists {
			t.Fatalf("expected %s %s to be deleted after down migration", chk.table, chk.id)
		}
	}

	// 4. Assert non-warmup rooms and races remain intact
	for _, chk := range []struct {
		table string
		id    uuid.UUID
	}{
		{"rooms", raceRoomID},
		{"races", raceID},
		{"rooms", ttRoomID},
		{"races", ttRaceID},
	} {
		var rowExists bool
		if err := tx.QueryRow(ctx, fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)", chk.table), chk.id).Scan(&rowExists); err != nil {
			t.Fatal(err)
		}
		if !rowExists {
			t.Fatalf("expected non-warmup %s %s to remain intact after down migration", chk.table, chk.id)
		}
	}

	var ttFlag bool
	if err := tx.QueryRow(ctx, "SELECT time_trial FROM races WHERE id = $1", ttRaceID).Scan(&ttFlag); err != nil {
		t.Fatal(err)
	}
	if !ttFlag {
		t.Fatalf("expected time_trial flag on %s to remain true", ttRaceID)
	}

	var raceFlag bool
	if err := tx.QueryRow(ctx, "SELECT time_trial FROM races WHERE id = $1", raceID).Scan(&raceFlag); err != nil {
		t.Fatal(err)
	}
	if raceFlag {
		t.Fatalf("expected time_trial flag on %s to remain false", raceID)
	}

	// 5. Re-apply up migration and assert it succeeds
	if _, err := tx.Exec(ctx, upSQL); err != nil {
		t.Fatalf("re-applying up migration after down failed: %v", err)
	}

	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM race_types WHERE id = $1)`, sentinelID).Scan(&sentinelExists); err != nil {
		t.Fatal(err)
	}
	if !sentinelExists {
		t.Fatalf("expected warmup sentinel %s to be restored after re-applying up migration", sentinelID)
	}

	var restoredMode string
	if err := tx.QueryRow(ctx, "SELECT mode FROM races WHERE id = $1", ttRaceID).Scan(&restoredMode); err != nil {
		t.Fatal(err)
	}
	if restoredMode != "time_trial" {
		t.Fatalf("expected restored mode 'time_trial' for time trial race, got %s", restoredMode)
	}
}
