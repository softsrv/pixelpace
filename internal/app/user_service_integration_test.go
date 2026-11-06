//go:build integration

package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
)

// newNamedTestUser inserts a user with a unique email and registers cleanup.
func newNamedTestUser(t *testing.T, pool *pgxpool.Pool) db.User {
	t.Helper()
	ctx := context.Background()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	user, err := db.New(pool).CreateUser(ctx, db.CreateUserParams{
		ID:           id,
		Email:        "dn-" + id.String() + "@example.com",
		PasswordHash: "$2a$12$placeholder",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
	})
	return user
}

// uniqueName returns a valid display name (letters and digits, 12 chars) that
// is unique per call, so tests sharing one database never collide. It is
// mixed-case so its lower/upper variants are distinct strings.
func uniqueName(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewRandom()
	if err != nil {
		t.Fatalf("uuid.NewRandom: %v", err)
	}
	return "Rw" + strings.ReplaceAll(id.String(), "-", "")[:10]
}

func storedDisplayName(t *testing.T, svc *app.UserService, id uuid.UUID) pgtype.Text {
	t.Helper()
	u, err := svc.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	return u.DisplayName
}

func wantStoredName(t *testing.T, svc *app.UserService, id uuid.UUID, want string) {
	t.Helper()
	got := storedDisplayName(t, svc, id)
	if !got.Valid || got.String != want {
		t.Fatalf("stored display_name = %+v, want %q", got, want)
	}
}

func wantNoStoredName(t *testing.T, svc *app.UserService, id uuid.UUID) {
	t.Helper()
	if got := storedDisplayName(t, svc, id); got.Valid {
		t.Fatalf("stored display_name = %q, want NULL", got.String)
	}
}

// CLM-3: a name held by another account, compared ignoring case, is refused
// and neither account's stored name changes.
func TestIntegration_SetDisplayName_TakenIgnoringCase(t *testing.T) {
	pool := testDB(t)
	svc := app.NewUserService(db.New(pool))
	ctx := context.Background()

	a := newNamedTestUser(t, pool)
	b := newNamedTestUser(t, pool)
	name := uniqueName(t)

	if err := svc.SetDisplayName(ctx, a.ID, name); err != nil {
		t.Fatalf("A SetDisplayName(%q): %v", name, err)
	}
	err := svc.SetDisplayName(ctx, b.ID, strings.ToLower(name))
	if !errors.Is(err, app.ErrDisplayNameTaken) {
		t.Fatalf("B SetDisplayName(%q) = %v, want ErrDisplayNameTaken", strings.ToLower(name), err)
	}
	if !strings.Contains(err.Error(), "taken") {
		t.Errorf("error message %q does not say the name is taken", err.Error())
	}
	wantNoStoredName(t, svc, b.ID)
	wantStoredName(t, svc, a.ID, name)
}

// CLM-3: two players claiming case variants of one name at the same moment —
// exactly one wins, the other gets ErrDisplayNameTaken.
func TestIntegration_SetDisplayName_ConcurrentClaims(t *testing.T) {
	pool := testDB(t)
	svc := app.NewUserService(db.New(pool))
	ctx := context.Background()

	for round := 0; round < 5; round++ {
		a := newNamedTestUser(t, pool)
		b := newNamedTestUser(t, pool)
		name := uniqueName(t)
		claims := []struct {
			id   uuid.UUID
			name string
		}{{a.ID, strings.ToUpper(name)}, {b.ID, strings.ToLower(name)}}

		var (
			start sync.WaitGroup
			done  sync.WaitGroup
			errs  = make([]error, len(claims))
		)
		start.Add(1)
		for i, c := range claims {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				errs[i] = svc.SetDisplayName(ctx, c.id, c.name)
			}()
		}
		start.Done()
		done.Wait()

		winners := 0
		for i, err := range errs {
			switch {
			case err == nil:
				winners++
				wantStoredName(t, svc, claims[i].id, claims[i].name)
			case errors.Is(err, app.ErrDisplayNameTaken):
				wantNoStoredName(t, svc, claims[i].id)
			default:
				t.Fatalf("round %d claim %d: unexpected error %v", round, i, err)
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: %d claims succeeded, want exactly 1 (errs=%v)", round, winners, errs)
		}
	}
}

// CLM-5: a refused name, for any reason, leaves the stored name exactly as it
// was — an earlier name or NULL.
func TestIntegration_SetDisplayName_RefusalLeavesStoredValue(t *testing.T) {
	pool := testDB(t)
	svc := app.NewUserService(db.New(pool))
	ctx := context.Background()

	holder := newNamedTestUser(t, pool)
	taken := uniqueName(t)
	if err := svc.SetDisplayName(ctx, holder.ID, taken); err != nil {
		t.Fatalf("holder SetDisplayName: %v", err)
	}

	named := newNamedTestUser(t, pool)
	first := uniqueName(t)
	if err := svc.SetDisplayName(ctx, named.ID, first); err != nil {
		t.Fatalf("SetDisplayName(%q): %v", first, err)
	}
	unnamed := newNamedTestUser(t, pool)

	for _, tc := range []struct {
		label   string
		user    uuid.UUID
		want    *string
		attempt string
		wantErr error
	}{
		{"named, invalid", named.ID, &first, "a@b", nil},
		{"named, taken", named.ID, &first, strings.ToLower(taken), app.ErrDisplayNameTaken},
		{"unnamed, invalid", unnamed.ID, nil, "a@b", nil},
		{"unnamed, taken", unnamed.ID, nil, strings.ToUpper(taken), app.ErrDisplayNameTaken},
	} {
		err := svc.SetDisplayName(ctx, tc.user, tc.attempt)
		if err == nil {
			t.Fatalf("%s: SetDisplayName(%q) succeeded, want an error", tc.label, tc.attempt)
		}
		if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: SetDisplayName(%q) = %v, want %v", tc.label, tc.attempt, err, tc.wantErr)
		}
		if tc.want != nil {
			wantStoredName(t, svc, tc.user, *tc.want)
		} else {
			wantNoStoredName(t, svc, tc.user)
		}
	}
}

// CLM-6: a player can change their name, a released name can be claimed by
// someone else, and re-submitting your own name in a different case succeeds.
// Names are stored exactly as typed after trimming.
func TestIntegration_SetDisplayName_ChangeReclaimAndSelfRecase(t *testing.T) {
	pool := testDB(t)
	svc := app.NewUserService(db.New(pool))
	ctx := context.Background()

	a := newNamedTestUser(t, pool)
	b := newNamedTestUser(t, pool)
	oldName := uniqueName(t)
	newName := uniqueName(t)

	if err := svc.SetDisplayName(ctx, a.ID, "  "+oldName+"  "); err != nil {
		t.Fatalf("A set %q: %v", oldName, err)
	}
	wantStoredName(t, svc, a.ID, oldName)

	// Change name.
	if err := svc.SetDisplayName(ctx, a.ID, newName); err != nil {
		t.Fatalf("A change to %q: %v", newName, err)
	}
	wantStoredName(t, svc, a.ID, newName)

	// Old name is released and another player can claim it.
	if err := svc.SetDisplayName(ctx, b.ID, strings.ToLower(oldName)); err != nil {
		t.Fatalf("B reclaim %q: %v", strings.ToLower(oldName), err)
	}
	wantStoredName(t, svc, b.ID, strings.ToLower(oldName))

	// Re-submitting one's own name in a different case succeeds and stores the new casing.
	if err := svc.SetDisplayName(ctx, a.ID, strings.ToUpper(newName)); err != nil {
		t.Fatalf("A re-case own name %q: %v", strings.ToUpper(newName), err)
	}
	wantStoredName(t, svc, a.ID, strings.ToUpper(newName))
}

// CLM-10 / CLM-14: Deleting a PixelPace account deletes that user's concept2_connections
// and concept2_result_posts (if present) row with it (ON DELETE CASCADE), and leaves other users' connections intact.
func TestIntegration_DeleteAccount_Concept2Cascade(t *testing.T) {
	pool := testDB(t)
	queries := db.New(pool)
	userSvc := app.NewUserService(queries)
	ctx := context.Background()

	a := newNamedTestUser(t, pool)
	b := newNamedTestUser(t, pool)

	// Seed concept2_connections row for user A directly via queries.
	_, err := queries.UpsertConcept2Connection(ctx, db.UpsertConcept2ConnectionParams{
		UserID:          a.ID,
		C2UserID:        1001,
		C2Username:      "athlete_a",
		AccessTokenEnc:  []byte("enc-access-token-a"),
		RefreshTokenEnc: []byte("enc-refresh-token-a"),
		ExpiresAt:       pgtype.Timestamptz{Time: time.Now().Add(time.Hour).UTC(), Valid: true},
		Scope:           "user:read,results:write",
		Timezone:        "UTC",
	})
	if err != nil {
		t.Fatalf("seed concept2_connections for A: %v", err)
	}

	// Seed concept2_connections row for user B directly via queries.
	_, err = queries.UpsertConcept2Connection(ctx, db.UpsertConcept2ConnectionParams{
		UserID:          b.ID,
		C2UserID:        1002,
		C2Username:      "athlete_b",
		AccessTokenEnc:  []byte("enc-access-token-b"),
		RefreshTokenEnc: []byte("enc-refresh-token-b"),
		ExpiresAt:       pgtype.Timestamptz{Time: time.Now().Add(time.Hour).UTC(), Valid: true},
		Scope:           "user:read,results:write",
		Timezone:        "Europe/Berlin",
	})
	if err != nil {
		t.Fatalf("seed concept2_connections for B: %v", err)
	}

	// Seed concept2_result_posts to verify cascade on user deletion.
	var raceTypeID uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM presets LIMIT 1").Scan(&raceTypeID); err != nil {
		t.Fatalf("query presets: %v", err)
	}

	roomID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7 room: %v", err)
	}
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	var code [6]byte
	entropy := uuid.Must(uuid.NewV7())
	for i := range code {
		code[i] = alphabet[int(entropy[10+i])%len(alphabet)]
	}

	// Host the room with user B so deleting user A does not cascade to the room or race.
	if _, err := pool.Exec(ctx, "INSERT INTO rooms (id, preset_id, host_user_id, status, join_code) VALUES ($1, $2, $3, 'in_progress', $4)",
		roomID, raceTypeID, b.ID, string(code[:])); err != nil {
		t.Fatalf("insert room: %v", err)
	}

	raceID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7 race: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO activities (id, room_id, preset_id, started_at) VALUES ($1, $2, $3, NOW())",
		raceID, roomID, raceTypeID); err != nil {
		t.Fatalf("insert race: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM concept2_result_posts WHERE race_id = $1", raceID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM activities WHERE id = $1", raceID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM rooms WHERE id = $1", roomID)
	})

	postIDA, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7 post A: %v", err)
	}
	postIDB, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7 post B: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO concept2_result_posts (id, user_id, race_id, status) VALUES ($1, $2, $3, 'pending')", postIDA, a.ID, raceID); err != nil {
		t.Fatalf("insert concept2_result_posts for A: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO concept2_result_posts (id, user_id, race_id, status) VALUES ($1, $2, $3, 'pending')", postIDB, b.ID, raceID); err != nil {
		t.Fatalf("insert concept2_result_posts for B: %v", err)
	}

	// Delete user A's account via UserService.DeleteAccount.
	if err := userSvc.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAccount(A): %v", err)
	}

	// Assert user A's concept2_connections row is gone.
	var countA int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM concept2_connections WHERE user_id = $1", a.ID).Scan(&countA); err != nil {
		t.Fatalf("check concept2_connections for A: %v", err)
	}
	if countA != 0 {
		t.Fatalf("expected 0 concept2_connections rows for deleted user A, got %d", countA)
	}

	// Assert user B's concept2_connections row remains intact.
	var countB int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM concept2_connections WHERE user_id = $1", b.ID).Scan(&countB); err != nil {
		t.Fatalf("check concept2_connections for B: %v", err)
	}
	if countB != 1 {
		t.Fatalf("expected 1 concept2_connections row for user B, got %d", countB)
	}

	// Verify user B's stored connection details remain intact.
	connB, err := queries.GetConcept2Connection(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetConcept2Connection(B): %v", err)
	}
	if connB.C2UserID != 1002 || connB.C2Username != "athlete_b" || string(connB.AccessTokenEnc) != "enc-access-token-b" {
		t.Fatalf("user B connection corrupted: %+v", connB)
	}

	var postCountA int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM concept2_result_posts WHERE user_id = $1", a.ID).Scan(&postCountA); err != nil {
		t.Fatalf("check concept2_result_posts for A: %v", err)
	}
	if postCountA != 0 {
		t.Fatalf("expected 0 concept2_result_posts rows for deleted user A, got %d", postCountA)
	}

	var postCountB int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM concept2_result_posts WHERE user_id = $1", b.ID).Scan(&postCountB); err != nil {
		t.Fatalf("check concept2_result_posts for B: %v", err)
	}
	if postCountB != 1 {
		t.Fatalf("expected 1 concept2_result_posts row for user B, got %d", postCountB)
	}
}
