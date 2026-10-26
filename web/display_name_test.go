package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
)

func renderTemplate(t *testing.T, page string, data any) string {
	t.Helper()
	base, err := template.ParseFS(FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base.html: %v", err)
	}
	tmpl, err := base.ParseFS(FS, "templates/"+page)
	if err != nil {
		t.Fatalf("parse %s: %v", page, err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute %s: %v", page, err)
	}
	return buf.String()
}

func TestDisplayName_RenderedInAccountCardWhenPresent(t *testing.T) {
	user := db.User{
		ID:            uuid.New(),
		Email:         "rower@example.com",
		EmailVerified: true,
		CreatedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
		DisplayName:   pgtype.Text{String: "RowerOne", Valid: true},
	}

	t.Run("dashboard", func(t *testing.T) {
		out := renderTemplate(t, "dashboard.html", map[string]any{
			"User":    user,
			"DevMode": false,
		})
		if !strings.Contains(out, "RowerOne") {
			t.Fatalf("dashboard output does not contain display name %q", "RowerOne")
		}
	})

	t.Run("profile", func(t *testing.T) {
		out := renderTemplate(t, "profile.html", map[string]any{
			"User": user,
		})
		if !strings.Contains(out, "RowerOne") {
			t.Fatalf("profile output does not contain display name %q", "RowerOne")
		}
	})
}

func TestDisplayName_OmittedWhenNull(t *testing.T) {
	user := db.User{
		ID:            uuid.New(),
		Email:         "rower@example.com",
		EmailVerified: true,
		CreatedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
		DisplayName:   pgtype.Text{Valid: false},
	}

	t.Run("dashboard", func(t *testing.T) {
		out := renderTemplate(t, "dashboard.html", map[string]any{
			"User":    user,
			"DevMode": false,
		})
		if strings.Contains(out, "RowerOne") {
			t.Fatalf("dashboard output unexpectedly contains %q", "RowerOne")
		}
		if strings.Contains(out, `<p class="text-base-content/90 font-medium">`) {
			t.Fatalf("dashboard output contains empty display name element")
		}
	})

	t.Run("profile", func(t *testing.T) {
		out := renderTemplate(t, "profile.html", map[string]any{
			"User": user,
		})
		if strings.Contains(out, "RowerOne") {
			t.Fatalf("profile output unexpectedly contains %q", "RowerOne")
		}
		if strings.Contains(out, `<p class="text-base-content/90 font-medium">`) {
			t.Fatalf("profile output contains empty display name element")
		}
	})
}

func renderContent(t *testing.T, page string, data any) string {
	t.Helper()
	base, err := template.ParseFS(FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base.html: %v", err)
	}
	tmpl, err := base.ParseFS(FS, "templates/"+page)
	if err != nil {
		t.Fatalf("parse %s: %v", page, err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "content", data); err != nil {
		t.Fatalf("execute content for %s: %v", page, err)
	}
	return buf.String()
}

func TestDisplayName_DashboardPrompt(t *testing.T) {
	promptText := "Set a display name so other racers can see who you are."

	t.Run("nameless user renders prompt linking to profile", func(t *testing.T) {
		user := db.User{
			ID:            uuid.New(),
			Email:         "rower@example.com",
			EmailVerified: true,
			CreatedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
			DisplayName:   pgtype.Text{Valid: false},
		}
		data := map[string]any{
			"User":    user,
			"DevMode": false,
		}

		out := renderTemplate(t, "dashboard.html", data)
		if !strings.Contains(out, promptText) {
			t.Fatalf("dashboard output does not contain prompt %q", promptText)
		}
		if !strings.Contains(out, `href="/profile"`) {
			t.Fatalf("dashboard output does not contain link to /profile")
		}

		content := renderContent(t, "dashboard.html", data)
		if !strings.Contains(content, promptText) {
			t.Fatalf("dashboard content does not contain prompt %q", promptText)
		}
		if !strings.Contains(content, `href="/profile"`) {
			t.Fatalf("dashboard content does not contain link to /profile")
		}
	})

	t.Run("named user renders without prompt", func(t *testing.T) {
		user := db.User{
			ID:            uuid.New(),
			Email:         "rower@example.com",
			EmailVerified: true,
			CreatedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
			DisplayName:   pgtype.Text{String: "RowerOne", Valid: true},
		}
		data := map[string]any{
			"User":    user,
			"DevMode": false,
		}

		out := renderTemplate(t, "dashboard.html", data)
		if strings.Contains(out, promptText) {
			t.Fatalf("dashboard output unexpectedly contains prompt %q", promptText)
		}

		content := renderContent(t, "dashboard.html", data)
		if strings.Contains(content, promptText) {
			t.Fatalf("dashboard content unexpectedly contains prompt %q", promptText)
		}
		if strings.Contains(content, `href="/profile"`) {
			t.Fatalf("dashboard content unexpectedly contains link to /profile")
		}
	})
}
