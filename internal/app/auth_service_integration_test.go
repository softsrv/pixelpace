//go:build integration

package app_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/email"
)

// testDB connects to the database pointed to by TEST_DATABASE_URL.
// Run integration tests with: TEST_DATABASE_URL=... go test -tags integration ./internal/app/...
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testAuthService(t *testing.T, pool *pgxpool.Pool) *app.AuthService {
	t.Helper()
	q := db.New(pool)
	return app.NewAuthService(q, pool, &email.NoopMailer{}, app.AuthServiceConfig{
		JWTSecret:      "integration-test-secret-32-bytes!!!",
		AccessExpiry:   15 * time.Minute,
		RefreshExpiry:  720 * time.Hour,
		BCryptCost:     12,
		PasswordMinLen: 8,
		AppBaseURL:     "http://localhost:8080",
		AppName:        "TestApp",
	})
}

func TestIntegration_LoginFlow(t *testing.T) {
	pool := testDB(t)
	svc := testAuthService(t, pool)
	ctx := context.Background()

	const testEmail = "integration@example.com"
	const testPassword = "correct-password-123"

	// Register.
	user, err := svc.Register(ctx, testEmail, testPassword)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	// Login with correct credentials.
	result, err := svc.Login(ctx, testEmail, testPassword, app.DeviceMeta{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		t.Fatal("expected non-empty tokens")
	}

	// Login with wrong password.
	_, err = svc.Login(ctx, testEmail, "wrong-password", app.DeviceMeta{})
	if err != app.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// CLM-7: against the migrated schema (display_name column present), a player
// who never sets a name registers, verifies and signs in exactly as before and
// has a NULL display_name.
func TestIntegration_RegisterWithoutDisplayName(t *testing.T) {
	pool := testDB(t)
	svc := testAuthService(t, pool)
	ctx := context.Background()

	const testEmail = "noname@example.com"
	const testPassword = "correct-password-123"

	user, err := svc.Register(ctx, testEmail, testPassword)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})
	if user.DisplayName.Valid {
		t.Fatalf("Register returned display_name %q, want NULL", user.DisplayName.String)
	}
	if err := db.New(pool).SetEmailVerified(ctx, user.ID); err != nil {
		t.Fatalf("SetEmailVerified: %v", err)
	}

	result, err := svc.Login(ctx, testEmail, testPassword, app.DeviceMeta{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		t.Fatal("expected non-empty tokens")
	}

	stored, err := app.NewUserService(db.New(pool)).GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.DisplayName.Valid {
		t.Fatalf("stored display_name = %q, want NULL", stored.DisplayName.String)
	}
	if !stored.EmailVerified {
		t.Fatal("expected verified user")
	}
}

func TestIntegration_Refresh(t *testing.T) {
	pool := testDB(t)
	svc := testAuthService(t, pool)
	ctx := context.Background()

	user, err := svc.Register(ctx, "refresh@example.com", "password123")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	result, err := svc.Login(ctx, "refresh@example.com", "password123", app.DeviceMeta{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Refresh returns a new access token but the same refresh token.
	result2, err := svc.Refresh(ctx, result.RefreshToken, app.DeviceMeta{})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result2.RefreshToken != result.RefreshToken {
		t.Error("refresh token must not change (no rotation)")
	}
	if result2.AccessToken == result.AccessToken {
		t.Error("access token must be newly issued on each refresh")
	}

	// Each refresh slides the session's expiry to the full idle lifetime
	// (RefreshExpiry, 720h here) from now, in the database and in the result.
	if _, err := pool.Exec(ctx, "UPDATE refresh_tokens SET expires_at = NOW() + interval '1 hour' WHERE token_hash = $1",
		auth.HashToken(result.RefreshToken)); err != nil {
		t.Fatal(err)
	}
	slid, err := svc.Refresh(ctx, result.RefreshToken, app.DeviceMeta{})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var stored time.Time
	if err := pool.QueryRow(ctx, "SELECT expires_at FROM refresh_tokens WHERE token_hash = $1",
		auth.HashToken(result.RefreshToken)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(720 * time.Hour)
	for name, got := range map[string]time.Time{"stored": stored, "returned": slid.RefreshTokenExpiry} {
		if d := got.Sub(want); d < -time.Minute || d > time.Minute {
			t.Errorf("%s refresh expiry %v, want about 720h from now (%v)", name, got, want)
		}
	}

	// Token can be refreshed again without error.
	_, err = svc.Refresh(ctx, result.RefreshToken, app.DeviceMeta{})
	if err != nil {
		t.Errorf("second refresh of same token must succeed, got %v", err)
	}

	// Revoked token must be rejected.
	if err := svc.Logout(ctx, result.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	_, err = svc.Refresh(ctx, result.RefreshToken, app.DeviceMeta{})
	if err != app.ErrTokenRevoked {
		t.Errorf("expected ErrTokenRevoked after logout, got %v", err)
	}
}

func TestIntegration_PasswordReset(t *testing.T) {
	pool := testDB(t)
	svc := testAuthService(t, pool)
	ctx := context.Background()

	user, err := svc.Register(ctx, "reset@example.com", "oldpassword123")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	// Request reset (always nil for email existence check).
	if err := svc.RequestPasswordReset(ctx, "reset@example.com"); err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}

	// Verify invalid token is rejected without needing DB lookup.
	err = svc.CompletePasswordReset(ctx, "invalid-token", "newpassword123")
	if err != app.ErrTokenNotFound {
		t.Errorf("expected ErrTokenNotFound for invalid token, got %v", err)
	}
}

func TestIntegration_AccountLockout(t *testing.T) {
	pool := testDB(t)
	svc := testAuthService(t, pool)
	ctx := context.Background()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	q := db.New(pool)
	user, err := q.CreateUser(ctx, db.CreateUserParams{
		ID:           id,
		Email:        "lockout@example.com",
		PasswordHash: "$2a$12$placeholder",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	// Lock the account directly.
	if err := q.LockAccount(ctx, db.LockAccountParams{
		ID:          user.ID,
		LockedUntil: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("LockAccount: %v", err)
	}

	_, err = svc.Login(ctx, "lockout@example.com", "any-password", app.DeviceMeta{})
	if err != app.ErrAccountLocked {
		t.Errorf("expected ErrAccountLocked, got %v", err)
	}
}

// Logging in again from a browser that still holds a refresh token replaces
// that browser's session: the old token is revoked rather than left in the
// session list until it expires. A login without one (another device) leaves
// existing sessions alone.
func TestIntegration_LoginReplacesPresentedSession(t *testing.T) {
	pool := testDB(t)
	svc := testAuthService(t, pool)
	ctx := context.Background()
	q := db.New(pool)

	user, err := svc.Register(ctx, "replace@example.com", "password123")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	first, err := svc.Login(ctx, "replace@example.com", "password123", app.DeviceMeta{})
	if err != nil {
		t.Fatalf("first Login: %v", err)
	}
	other, err := svc.Login(ctx, "replace@example.com", "password123", app.DeviceMeta{})
	if err != nil {
		t.Fatalf("other-device Login: %v", err)
	}

	// Same browser logs in again, presenting its old token plus one that
	// matches nothing (unknown tokens are ignored).
	second, err := svc.Login(ctx, "replace@example.com", "password123", app.DeviceMeta{
		ReplacesRefreshTokens: []string{first.RefreshToken, "not-a-real-token"},
	})
	if err != nil {
		t.Fatalf("second Login: %v", err)
	}

	if _, err := svc.Refresh(ctx, first.RefreshToken, app.DeviceMeta{}); !errors.Is(err, app.ErrTokenRevoked) {
		t.Errorf("Refresh(replaced) error = %v, want ErrTokenRevoked", err)
	}
	for name, tok := range map[string]string{"new": second.RefreshToken, "other device": other.RefreshToken} {
		if _, err := svc.Refresh(ctx, tok, app.DeviceMeta{}); err != nil {
			t.Errorf("Refresh(%s session): %v", name, err)
		}
	}

	active, err := q.ListActiveRefreshTokensByUserID(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListActiveRefreshTokensByUserID: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("active sessions = %d, want 2 (new + other device)", len(active))
	}

	// A failed login must not revoke the presented session.
	if _, err := svc.Login(ctx, "replace@example.com", "wrong-password", app.DeviceMeta{
		ReplacesRefreshTokens: []string{second.RefreshToken},
	}); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("bad-password Login error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Refresh(ctx, second.RefreshToken, app.DeviceMeta{}); err != nil {
		t.Errorf("Refresh after failed login: %v", err)
	}
}
