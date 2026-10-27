package auth_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/auth"
)

const testSecret = "test-secret-that-is-at-least-32-bytes!!"

func TestIssueAndValidateAccessToken(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	email := "user@example.com"
	expiry := 15 * time.Minute

	t.Run("valid token roundtrip", func(t *testing.T) {
		t.Parallel()
		tp, err := auth.IssueAccessToken(userID, email, testSecret, expiry)
		if err != nil {
			t.Fatalf("IssueAccessToken: %v", err)
		}
		if tp.AccessToken == "" {
			t.Fatal("expected non-empty access token")
		}

		claims, err := auth.ValidateAccessToken(tp.AccessToken, testSecret)
		if err != nil {
			t.Fatalf("ValidateAccessToken: %v", err)
		}
		if claims.Subject != userID.String() {
			t.Errorf("subject: got %q, want %q", claims.Subject, userID.String())
		}
		if claims.Email != email {
			t.Errorf("email: got %q, want %q", claims.Email, email)
		}
	})

	t.Run("wrong secret returns error", func(t *testing.T) {
		t.Parallel()
		tp, _ := auth.IssueAccessToken(userID, email, testSecret, expiry)
		_, err := auth.ValidateAccessToken(tp.AccessToken, "wrong-secret-that-is-definitely-long-enough")
		if err == nil {
			t.Fatal("expected error with wrong secret")
		}
	})

	t.Run("expired token returns ErrTokenExpired", func(t *testing.T) {
		t.Parallel()
		tp, _ := auth.IssueAccessToken(userID, email, testSecret, -time.Second)
		_, err := auth.ValidateAccessToken(tp.AccessToken, testSecret)
		if err != auth.ErrTokenExpired {
			t.Errorf("expected ErrTokenExpired, got %v", err)
		}
	})

	t.Run("malformed token returns ErrTokenInvalid", func(t *testing.T) {
		t.Parallel()
		_, err := auth.ValidateAccessToken("not.a.valid.jwt", testSecret)
		if err != auth.ErrTokenInvalid {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	t.Run("consecutive tokens have distinct access tokens and non-empty distinct IDs", func(t *testing.T) {
		t.Parallel()
		tp1, err := auth.IssueAccessToken(userID, email, testSecret, expiry)
		if err != nil {
			t.Fatalf("first IssueAccessToken: %v", err)
		}
		tp2, err := auth.IssueAccessToken(userID, email, testSecret, expiry)
		if err != nil {
			t.Fatalf("second IssueAccessToken: %v", err)
		}
		if tp1.AccessToken == tp2.AccessToken {
			t.Fatal("expected different access tokens for consecutive issuances")
		}

		claims1, err := auth.ValidateAccessToken(tp1.AccessToken, testSecret)
		if err != nil {
			t.Fatalf("ValidateAccessToken(tp1): %v", err)
		}
		claims2, err := auth.ValidateAccessToken(tp2.AccessToken, testSecret)
		if err != nil {
			t.Fatalf("ValidateAccessToken(tp2): %v", err)
		}

		if claims1.ID == "" {
			t.Error("expected non-empty ID claim on first token")
		}
		if claims2.ID == "" {
			t.Error("expected non-empty ID claim on second token")
		}
		if claims1.ID == claims2.ID {
			t.Errorf("expected distinct IDs, got %q for both", claims1.ID)
		}
	})
}
