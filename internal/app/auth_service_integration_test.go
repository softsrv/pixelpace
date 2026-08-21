//go:build integration

package app_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
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
