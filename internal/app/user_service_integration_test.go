//go:build integration

package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

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
