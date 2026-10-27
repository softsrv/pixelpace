package handlers_test

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
)

const testJWTSecret = "handler-test-secret-at-least-32-bytes-long!!"

// testTemplateFS is a minimal in-memory template set mirroring the real layout:
// a base.html layout with an overridable "content" block, plus the partials and
// pages the handlers reference. Bodies are intentionally trivial — the tests
// assert on behavior (cookies, redirects, status codes), not markup.
var testTemplateFS = fstest.MapFS{
	"templates/base.html": &fstest.MapFile{Data: []byte(
		`{{define "base.html"}}<!doctype html><html><body>{{block "content" .}}{{end}}</body></html>{{end}}`,
	)},
	"templates/partials/error.html": &fstest.MapFile{Data: []byte(
		`{{define "partials/error.html"}}error:{{.Error}}{{end}}`,
	)},
	"templates/partials/flash.html": &fstest.MapFile{Data: []byte(
		`{{define "partials/flash.html"}}flash:{{.Message}}{{end}}`,
	)},
	"templates/partials/session-list.html": &fstest.MapFile{Data: []byte(
		`{{define "partials/session-list.html"}}sessions:{{len .Sessions}}{{end}}`,
	)},
	"templates/auth/login.html": &fstest.MapFile{Data: []byte(
		`{{define "content"}}login{{end}}`,
	)},
	"templates/auth/register.html": &fstest.MapFile{Data: []byte(
		`{{define "content"}}register{{end}}`,
	)},
	"templates/auth/verify-email.html": &fstest.MapFile{Data: []byte(
		`{{define "content"}}verify{{end}}`,
	)},
	"templates/race.html": &fstest.MapFile{Data: []byte(
		`{{define "content"}}race:{{.Race.Label}}{{range .Lanes}}|{{.Label}}{{if .IsSelf}}*{{end}}{{end}}{{end}}`,
	)},
	"templates/lobby.html": &fstest.MapFile{Data: []byte(
		`{{define "content"}}lobby:{{.JoinURL}}|types:{{range .RaceTypes}}{{.Label}},{{end}}` +
			`|friends:{{range .Friends}}{{.Name}}{{if .Invited}}+invited{{end}}{{if .Joined}}+joined{{end}},{{end}}|{{template "lobby-panel" .}}{{end}}` +
			`{{define "lobby-panel"}}panel:{{.Lobby.RaceLabel}}{{range .Lobby.Participants}}|{{.Name}}{{if .Ready}}+ready{{end}}{{end}}` +
			`{{if .Lobby.CanStart}}|can-start{{end}}{{end}}` +
			`{{define "invite-sent"}}invited{{end}}`,
	)},
	"templates/dashboard.html": &fstest.MapFile{Data: []byte(
		`{{define "content"}}dashboard{{end}}` +
			`{{define "race-card"}}race-card:{{range .RaceTypes}}{{.Label}},{{end}}{{end}}` +
			`{{define "room-invites"}}invites:{{range .Invites}}{{.Inviter.Name}}/{{.RaceLabel}},{{end}}{{end}}`,
	)},
	"templates/room-unavailable.html": &fstest.MapFile{Data: []byte(
		`{{define "content"}}unavailable:{{.Message}}{{end}}`,
	)},
}

// serveAuthenticated runs endpoint behind the real auth middleware as user.
func serveAuthenticated(t *testing.T, user db.User, endpoint http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	token, err := auth.IssueAccessToken(user.ID, user.Email, testJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
	rr := httptest.NewRecorder()
	middleware.Authenticate(&handlerUserFetcher{user: user}, testJWTSecret)(endpoint).ServeHTTP(rr, req)
	return rr
}

// newTestRenderer builds a TemplateRenderer over testTemplateFS the same way
// main.go builds the production renderer.
func newTestRenderer(t *testing.T) *handlers.TemplateRenderer {
	t.Helper()
	base, err := template.ParseFS(testTemplateFS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base template: %v", err)
	}
	if _, err := base.ParseFS(testTemplateFS, "templates/partials/*.html"); err != nil {
		t.Fatalf("parse partials: %v", err)
	}
	return handlers.NewTemplateRenderer(base, testTemplateFS, "templates")
}
