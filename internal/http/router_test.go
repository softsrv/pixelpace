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

	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
)

const routerTestJWTSecret = "router-test-secret-at-least-32-bytes-long!!"

type routerUserFetcher struct {
	user db.User
}

func (f routerUserFetcher) GetUserByID(context.Context, uuid.UUID) (db.User, error) {
	return f.user, nil
}

func newDashboardTestRenderer(t *testing.T) *handlers.TemplateRenderer {
	t.Helper()
	fsys := fstest.MapFS{
		"templates/base.html": &fstest.MapFile{Data: []byte(`{{define "base.html"}}<!doctype html><html><body>{{block "content" .}}{{end}}</body></html>{{end}}`)},
		"templates/dashboard.html": &fstest.MapFile{Data: []byte(`{{define "content"}}<section id="rowing-machine-card" data-dev-mode="{{if .DevMode}}true{{else}}false{{end}}">{{.User.Email}}</section>{{end}}`)},
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
