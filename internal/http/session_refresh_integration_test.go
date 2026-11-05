//go:build integration

package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/email"
)

const sessionIdleLifetime = 240 * time.Hour

// sessionFixture is a router backed by the real AuthService and database, and
// a verified user logged in through it.
type sessionFixture struct {
	pool    *pgxpool.Pool
	router  http.Handler
	userID  uuid.UUID
	refresh string
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	queries := db.New(pool)
	authSvc := app.NewAuthService(queries, pool, &email.NoopMailer{}, app.AuthServiceConfig{
		JWTSecret:      routerTestJWTSecret,
		AccessExpiry:   15 * time.Minute,
		RefreshExpiry:  sessionIdleLifetime,
		BCryptCost:     4,
		PasswordMinLen: 8,
		AppBaseURL:     "http://localhost:8080",
		AppName:        "TestApp",
	})

	addr := fmt.Sprintf("session-%s@example.test", uuid.Must(uuid.NewV7()))
	user, err := authSvc.Register(ctx, addr, "password123")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID) })
	if _, err := pool.Exec(ctx, "UPDATE users SET email_verified = true WHERE id = $1", user.ID); err != nil {
		t.Fatal(err)
	}
	tokens, err := authSvc.Login(ctx, addr, "password123", app.DeviceMeta{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	routerCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	router := NewRouter(routerCtx, RouterConfig{
		Queries:   queries,
		AuthSvc:   authSvc,
		Renderer:  newDashboardTestRenderer(t),
		JWTSecret: routerTestJWTSecret,
	})
	return &sessionFixture{pool: pool, router: router, userID: user.ID, refresh: tokens.RefreshToken}
}

// setExpiry moves the stored session's expiry to at.
func (f *sessionFixture) setExpiry(t *testing.T, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		"UPDATE refresh_tokens SET expires_at = $2 WHERE token_hash = $1", auth.HashToken(f.refresh), at); err != nil {
		t.Fatal(err)
	}
}

func (f *sessionFixture) storedExpiry(t *testing.T) time.Time {
	t.Helper()
	var at time.Time
	if err := f.pool.QueryRow(context.Background(),
		"SELECT expires_at FROM refresh_tokens WHERE token_hash = $1", auth.HashToken(f.refresh)).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// get requests path carrying only the refresh_token cookie, as a browser does
// once its access_token cookie has expired.
func (f *sessionFixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: f.refresh})
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func responseCookie(rr *httptest.ResponseRecorder, name, path string) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name && c.Path == path {
			return c
		}
	}
	return nil
}

func near(got, want time.Time) bool {
	d := got.Sub(want)
	return d > -time.Minute && d < time.Minute
}

// An active session with no access token left is renewed on the request
// itself, and its idle expiry slides forward to the full lifetime from now.
func TestIntegration_SessionRefresh_ProtectedRouteRenewsAndSlides(t *testing.T) {
	f := newSessionFixture(t)
	// Last used 9 days ago: one day of its 10-day idle lifetime left.
	f.setExpiry(t, time.Now().Add(24*time.Hour))

	rr := f.get("/dashboard")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /dashboard with only a refresh cookie = %d (Location %q), want 200", rr.Code, rr.Header().Get("Location"))
	}
	access := responseCookie(rr, "access_token", "/")
	if access == nil || access.Value == "" {
		t.Fatal("no renewed access_token cookie")
	}
	if _, err := auth.ValidateAccessToken(access.Value, routerTestJWTSecret); err != nil {
		t.Fatalf("renewed access token does not validate: %v", err)
	}
	wantExpiry := time.Now().Add(sessionIdleLifetime)
	refresh := responseCookie(rr, "refresh_token", "/")
	if refresh == nil || refresh.Value != f.refresh || !near(refresh.Expires, wantExpiry) {
		t.Fatalf("refresh_token cookie = %+v, want the same token at Path=/ expiring in about 10 days", refresh)
	}
	if got := f.storedExpiry(t); !near(got, wantExpiry) {
		t.Fatalf("stored expiry %v, want about 10 days from now (%v)", got, wantExpiry)
	}
}

// A returning user whose session is still alive lands on the dashboard from
// "/" rather than on the landing page.
func TestIntegration_SessionRefresh_RootSendsLiveSessionToDashboard(t *testing.T) {
	f := newSessionFixture(t)

	rr := f.get("/")
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/dashboard" {
		t.Fatalf("GET / with only a refresh cookie = %d to %q, want 302 to /dashboard", rr.Code, rr.Header().Get("Location"))
	}
	if responseCookie(rr, "access_token", "/") == nil {
		t.Fatal("no renewed access_token cookie")
	}
}

// A session unused for longer than the idle lifetime has expired: the user
// is sent to log in and the stale cookies are cleared.
func TestIntegration_SessionRefresh_IdleSessionMustLogIn(t *testing.T) {
	f := newSessionFixture(t)
	f.setExpiry(t, time.Now().Add(-time.Minute))

	rr := f.get("/dashboard")
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("GET /dashboard with an idle-expired session = %d to %q, want 303 to /login", rr.Code, rr.Header().Get("Location"))
	}
	if c := responseCookie(rr, "refresh_token", "/"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("refresh_token cookie = %+v, want it cleared", c)
	}

	if rr := f.get("/"); rr.Code != http.StatusOK {
		t.Fatalf("GET / with an idle-expired session = %d (Location %q), want the 200 landing page", rr.Code, rr.Header().Get("Location"))
	}
}
