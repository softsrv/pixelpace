package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/middleware"
)

const authTestSecret = "auth-test-secret-that-is-at-least-32-bytes!!"

// stubFetcher implements middleware.UserFetcher for testing.
type stubFetcher struct {
	user db.User
	err  error
}

func (s *stubFetcher) GetUserByID(_ context.Context, _ uuid.UUID) (db.User, error) {
	return s.user, s.err
}

func makeTestToken(t *testing.T, userID uuid.UUID, expiry time.Duration) string {
	t.Helper()
	tp, err := auth.IssueAccessToken(userID, "test@example.com", authTestSecret, expiry)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	return tp.AccessToken
}

func TestAuthMiddleware(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	validUser := db.User{ID: userID, Email: "test@example.com"}

	protected := middleware.Authenticate(&stubFetcher{user: validUser}, authTestSecret)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, ok := middleware.UserFromContext(r.Context())
			if !ok {
				t.Error("user not found in context")
			}
			w.WriteHeader(http.StatusOK)
		}),
	)

	t.Run("valid cookie passes through", func(t *testing.T) {
		t.Parallel()
		token := makeTestToken(t, userID, 15*time.Minute)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: token})
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("got %d, want 200", rr.Code)
		}
	})

	t.Run("valid bearer header passes through", func(t *testing.T) {
		t.Parallel()
		token := makeTestToken(t, userID, 15*time.Minute)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("got %d, want 200", rr.Code)
		}
	})

	t.Run("missing token redirects", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != http.StatusSeeOther {
			t.Errorf("got %d, want 303", rr.Code)
		}
	})

	t.Run("expired token sets HX-Trigger for HTMX requests", func(t *testing.T) {
		t.Parallel()
		token := makeTestToken(t, userID, -time.Second)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: token})
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Header().Get("HX-Trigger") != "token-expired" {
			t.Errorf("expected HX-Trigger: token-expired, got %q", rr.Header().Get("HX-Trigger"))
		}
	})
}
