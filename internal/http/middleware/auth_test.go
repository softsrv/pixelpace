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

	t.Run("expired token without a refresher sends HTMX to login", func(t *testing.T) {
		t.Parallel()
		token := makeTestToken(t, userID, -time.Second)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: token})
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized || rr.Header().Get("HX-Redirect") != "/login" {
			t.Errorf("got %d with HX-Redirect %q, want 401 to /login", rr.Code, rr.Header().Get("HX-Redirect"))
		}
	})
}

// stubRefresher implements middleware.SessionRefresher for testing.
type stubRefresher struct {
	token string
	ok    bool
	calls int
}

func (s *stubRefresher) RefreshSession(w http.ResponseWriter, _ *http.Request) (string, bool) {
	s.calls++
	if s.ok {
		http.SetCookie(w, &http.Cookie{Name: "access_token", Value: s.token, Path: "/"})
	}
	return s.token, s.ok
}

func TestAuthMiddlewareSessionRefresh(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	validUser := db.User{ID: userID, Email: "test@example.com"}

	serve := func(t *testing.T, refresher *stubRefresher, req *http.Request) (*httptest.ResponseRecorder, bool) {
		t.Helper()
		reached := false
		h := middleware.Authenticate(&stubFetcher{user: validUser}, authTestSecret, middleware.WithSessionRefresh(refresher))(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, ok := middleware.UserFromContext(r.Context())
				reached = ok && user.ID == userID
				w.WriteHeader(http.StatusOK)
			}),
		)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr, reached
	}

	for _, tc := range []struct {
		name   string
		access string
		htmx   bool
	}{
		{"missing access token", "", false},
		{"expired access token", makeTestToken(t, userID, -time.Second), false},
		{"expired access token on an HTMX request", makeTestToken(t, userID, -time.Second), true},
		{"malformed access token", "not-a-jwt", false},
	} {
		t.Run(tc.name+" is renewed and goes through", func(t *testing.T) {
			t.Parallel()
			refresher := &stubRefresher{token: makeTestToken(t, userID, 15*time.Minute), ok: true}
			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			if tc.access != "" {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: tc.access})
			}
			if tc.htmx {
				req.Header.Set("HX-Request", "true")
			}
			rr, reached := serve(t, refresher, req)
			if rr.Code != http.StatusOK || !reached {
				t.Fatalf("got %d (handler reached with user: %v), want 200 through the handler", rr.Code, reached)
			}
			if refresher.calls != 1 {
				t.Fatalf("refresher called %d times, want 1", refresher.calls)
			}
			if c := rr.Result().Cookies(); len(c) != 1 || c[0].Name != "access_token" {
				t.Fatalf("response cookies = %v, want the renewed access_token", c)
			}
		})
	}

	t.Run("valid access token is not renewed", func(t *testing.T) {
		t.Parallel()
		refresher := &stubRefresher{ok: true}
		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: makeTestToken(t, userID, 15*time.Minute)})
		rr, reached := serve(t, refresher, req)
		if rr.Code != http.StatusOK || !reached || refresher.calls != 0 {
			t.Fatalf("got %d, reached %v, refresher calls %d; want 200 through the handler with no refresh", rr.Code, reached, refresher.calls)
		}
	})

	t.Run("failed renewal redirects to login", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		rr, reached := serve(t, &stubRefresher{}, req)
		if rr.Code != http.StatusSeeOther || reached || rr.Header().Get("Location") != "/login" {
			t.Fatalf("got %d to %q (reached %v), want 303 to /login", rr.Code, rr.Header().Get("Location"), reached)
		}
	})

	t.Run("renewed token that does not validate is turned away", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		rr, reached := serve(t, &stubRefresher{token: "garbage", ok: true}, req)
		if rr.Code != http.StatusSeeOther || reached {
			t.Fatalf("got %d (reached %v), want 303 to /login", rr.Code, reached)
		}
	})
}
