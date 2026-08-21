package handlers_test

import (
	"html/template"
	"testing"
	"testing/fstest"

	"github.com/softsrv/starter/internal/http/handlers"
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
