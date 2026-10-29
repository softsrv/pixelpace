package http

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
)

const routerTestJWTSecret = "router-test-secret-at-least-32-bytes-long!!"

type routerUserFetcher struct {
	user db.User
	err  error
}

func (f routerUserFetcher) GetUserByID(context.Context, uuid.UUID) (db.User, error) {
	if f.err != nil {
		return db.User{}, f.err
	}
	return f.user, nil
}

func newDashboardTestRenderer(t *testing.T) *handlers.TemplateRenderer {
	t.Helper()
	fsys := fstest.MapFS{
		"templates/base.html":      &fstest.MapFile{Data: []byte(`{{define "base.html"}}<!doctype html><html><body>{{block "content" .}}{{end}}</body></html>{{end}}`)},
		"templates/dashboard.html": &fstest.MapFile{Data: []byte(`{{define "content"}}<section id="rowing-machine-card" data-dev-mode="{{if .DevMode}}true{{else}}false{{end}}">{{.User.Email}}</section>{{end}}`)},
		"templates/landing.html":   &fstest.MapFile{Data: []byte(`{{define "content"}}<section id="landing-marker">PixelPace Landing</section>{{end}}`)},
	}
	base, err := template.ParseFS(fsys, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base template: %v", err)
	}
	return handlers.NewTemplateRenderer(base, fsys, "templates")
}

func TestDashboardDevModeFlag(t *testing.T) {
	userID := uuid.New()
	user := db.User{ID: userID, Email: "dev@example.com", EmailVerified: true}
	token, err := auth.IssueAccessToken(userID, user.Email, routerTestJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	tests := []struct {
		name    string
		devMode bool
		want    string
	}{
		{name: "development", devMode: true, want: `data-dev-mode="true"`},
		{name: "production", devMode: false, want: `data-dev-mode="false"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewRouter(context.Background(), RouterConfig{
				Queries:   routerUserFetcher{user: user},
				Renderer:  newDashboardTestRenderer(t),
				JWTSecret: routerTestJWTSecret,
				DevMode:   tt.devMode,
			})
			req := httptest.NewRequest("GET", "/dashboard", nil)
			req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
			rr := httptest.NewRecorder()

			h.ServeHTTP(rr, req)

			if rr.Code != 200 {
				t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Fatalf("dashboard body = %q, want %s", rr.Body.String(), tt.want)
			}
		})
	}
}

func TestRoomRoutesRequireVerifiedUser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	user := db.User{ID: uuid.New(), Email: "unverified@example.com", EmailVerified: false}
	token, err := auth.IssueAccessToken(user.ID, user.Email, routerTestJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	h := NewRouter(ctx, RouterConfig{
		Queries:   routerUserFetcher{user: user},
		Renderer:  newDashboardTestRenderer(t),
		JWTSecret: routerTestJWTSecret,
		RoomSvc:   nil, // Middleware must reject before the room service is used.
	})
	id := uuid.NewString()
	paths := []string{
		"/rooms", "/rooms/join", "/rooms/" + id + "/leave",
		"/rooms/" + id + "/kick", "/rooms/" + id + "/ready", "/rooms/" + id + "/start",
		"GET /rooms/" + id,
	}
	for _, route := range paths {
		method, path := http.MethodPost, route
		if rest, ok := strings.CutPrefix(route, "GET "); ok {
			method, path = http.MethodGet, rest
		}
		for _, authenticated := range []bool{false, true} {
			for _, htmx := range []bool{false, true} {
				name := route + "/anonymous"
				wantLocation := "/login"
				wantStatus := http.StatusUnauthorized
				if authenticated {
					name = route + "/unverified"
					wantLocation = "/verify-email"
					wantStatus = http.StatusForbidden
				}
				if htmx {
					name += "/htmx"
				}
				t.Run(name, func(t *testing.T) {
					req := httptest.NewRequest(method, path, nil)
					if authenticated {
						req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
					}
					locationHeader := "Location"
					if htmx {
						req.Header.Set("HX-Request", "true")
						locationHeader = "HX-Redirect"
					} else {
						wantStatus = http.StatusSeeOther
					}
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, req)
					if rr.Code != wantStatus {
						t.Errorf("status = %d, want %d", rr.Code, wantStatus)
					}
					if got := rr.Header().Get(locationHeader); got != wantLocation {
						t.Errorf("%s = %q, want %q", locationHeader, got, wantLocation)
					}
				})
			}
		}
	}
}

func TestDashboardTemplateDataCarriesDevMode(t *testing.T) {
	data := dashboardTemplateData(db.User{Email: "dev@example.com"}, true)
	if got := data["DevMode"]; got != true {
		t.Fatalf("DevMode = %v, want true", got)
	}
	data = dashboardTemplateData(db.User{Email: "prod@example.com"}, false)
	if got := data["DevMode"]; got != false {
		t.Fatalf("DevMode = %v, want false", got)
	}
}

func TestProfileDisplayNameRouteRequiresVerifiedUser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	user := db.User{ID: uuid.New(), Email: "unverified@example.com", EmailVerified: false}
	token, err := auth.IssueAccessToken(user.ID, user.Email, routerTestJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	h := NewRouter(ctx, RouterConfig{
		Queries:   routerUserFetcher{user: user},
		Renderer:  newDashboardTestRenderer(t),
		JWTSecret: routerTestJWTSecret,
	})

	path := "/profile/display-name"
	for _, authenticated := range []bool{false, true} {
		for _, htmx := range []bool{false, true} {
			name := path + "/anonymous"
			wantLocation := "/login"
			wantStatus := http.StatusUnauthorized
			if authenticated {
				name = path + "/unverified"
				wantLocation = "/verify-email"
				wantStatus = http.StatusForbidden
			}
			if htmx {
				name += "/htmx"
			} else {
				wantStatus = http.StatusSeeOther
			}
			t.Run(name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, path, nil)
				if authenticated {
					req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
				}
				locationHeader := "Location"
				if htmx {
					req.Header.Set("HX-Request", "true")
					locationHeader = "HX-Redirect"
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				if rr.Code != wantStatus {
					t.Errorf("status = %d, want %d", rr.Code, wantStatus)
				}
				if got := rr.Header().Get(locationHeader); got != wantLocation {
					t.Errorf("%s = %q, want %q", locationHeader, got, wantLocation)
				}
			})
		}
	}
}

func TestRouterWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRouter(ctx, RouterConfig{})
	// Wait should return cleanly when no in-flight begins exist.
	r.Wait()
}

func TestRootRoute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	validUser := db.User{ID: uuid.New(), Email: "user@example.com", EmailVerified: true}
	validToken, err := auth.IssueAccessToken(validUser.ID, validUser.Email, routerTestJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue valid access token: %v", err)
	}

	expiredToken, err := auth.IssueAccessToken(validUser.ID, validUser.Email, routerTestJWTSecret, -1*time.Minute)
	if err != nil {
		t.Fatalf("issue expired access token: %v", err)
	}

	wrongSecretToken, err := auth.IssueAccessToken(validUser.ID, validUser.Email, "wrong-jwt-secret-with-minimum-length-32", 15*time.Minute)
	if err != nil {
		t.Fatalf("issue wrong secret access token: %v", err)
	}

	unverifiedUser := db.User{ID: uuid.New(), Email: "unverified@example.com", EmailVerified: false}
	unverifiedToken, err := auth.IssueAccessToken(unverifiedUser.ID, unverifiedUser.Email, routerTestJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue unverified access token: %v", err)
	}

	tests := []struct {
		name         string
		path         string
		cookie       *http.Cookie
		fetcher      *routerUserFetcher
		wantCode     int
		wantLocation string
		wantMarker   bool
		wantCleared  bool
	}{
		{
			name:         "valid token for deleted user -> 200 landing page, cookie cleared",
			path:         "/",
			cookie:       &http.Cookie{Name: "access_token", Value: validToken.AccessToken},
			fetcher:      &routerUserFetcher{err: pgx.ErrNoRows},
			wantCode:     http.StatusOK,
			wantLocation: "",
			wantMarker:   true,
			wantCleared:  true,
		},
		{
			name:         "valid token for unverified user -> 302 to /dashboard",
			path:         "/",
			cookie:       &http.Cookie{Name: "access_token", Value: unverifiedToken.AccessToken},
			fetcher:      &routerUserFetcher{user: unverifiedUser},
			wantCode:     http.StatusFound,
			wantLocation: "/dashboard",
			wantMarker:   false,
		},
		{
			name:         "no cookie -> 200 landing page",
			path:         "/",
			cookie:       nil,
			wantCode:     http.StatusOK,
			wantLocation: "",
			wantMarker:   true,
		},
		{
			name:         "garbage cookie -> 200 landing page",
			path:         "/",
			cookie:       &http.Cookie{Name: "access_token", Value: "garbage-value-not-a-jwt"},
			wantCode:     http.StatusOK,
			wantLocation: "",
			wantMarker:   true,
		},
		{
			name:         "expired token -> 200 landing page",
			path:         "/",
			cookie:       &http.Cookie{Name: "access_token", Value: expiredToken.AccessToken},
			wantCode:     http.StatusOK,
			wantLocation: "",
			wantMarker:   true,
		},
		{
			name:         "wrong secret token -> 200 landing page",
			path:         "/",
			cookie:       &http.Cookie{Name: "access_token", Value: wrongSecretToken.AccessToken},
			wantCode:     http.StatusOK,
			wantLocation: "",
			wantMarker:   true,
		},
		{
			name:         "valid token -> 302 to /dashboard without landing marker",
			path:         "/",
			cookie:       &http.Cookie{Name: "access_token", Value: validToken.AccessToken},
			wantCode:     http.StatusFound,
			wantLocation: "/dashboard",
			wantMarker:   false,
		},
		{
			name:         "GET /nope with no cookie -> 302 to /login without landing marker",
			path:         "/nope",
			cookie:       nil,
			wantCode:     http.StatusFound,
			wantLocation: "/login",
			wantMarker:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := routerUserFetcher{user: validUser}
			if tt.fetcher != nil {
				fetcher = *tt.fetcher
			}
			h := NewRouter(ctx, RouterConfig{
				Queries:   fetcher,
				Renderer:  newDashboardTestRenderer(t),
				JWTSecret: routerTestJWTSecret,
			})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
			if loc := rr.Header().Get("Location"); loc != tt.wantLocation {
				t.Errorf("Location = %q, want %q", loc, tt.wantLocation)
			}
			body := rr.Body.String()
			hasMarker := strings.Contains(body, `id="landing-marker"`)
			if hasMarker != tt.wantMarker {
				t.Errorf("landing marker present = %v, want %v; body = %q", hasMarker, tt.wantMarker, body)
			}
			cleared := false
			for _, c := range rr.Result().Cookies() {
				if c.Name == "access_token" && c.MaxAge < 0 {
					cleared = true
				}
			}
			if cleared != tt.wantCleared {
				t.Errorf("access_token cleared = %v, want %v", cleared, tt.wantCleared)
			}
		})
	}
}
