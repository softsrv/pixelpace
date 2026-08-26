package web

import (
	"html/template"
	"strings"
	"testing"
)

// TestDashboardStatValuesContainOverflowGuardClasses renders the REAL embedded
// base.html + dashboard.html (via the production web.FS embed, not a mock FS)
// through Go's html/template engine and asserts that every one of the 8
// rowing-stats "stat-value" tiles carries the overflow-guard utility classes
// (whitespace-normal break-words min-w-0) added to fix narrow-viewport
// overflow, and that the #bt-metrics grid keeps its responsive reflow classes
// (grid gap-3 sm:grid-cols-2 lg:grid-cols-4). Reverting the class additions on
// the stat-value divs, or the grid's responsive classes, turns this test red.
func TestDashboardStatValuesContainOverflowGuardClasses(t *testing.T) {
	base, err := template.ParseFS(FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base.html: %v", err)
	}
	tmpl, err := base.ParseFS(FS, "templates/dashboard.html")
	if err != nil {
		t.Fatalf("parse dashboard.html: %v", err)
	}

	var buf strings.Builder
	data := map[string]any{
		"User":    map[string]any{"Email": "rower@example.com", "EmailVerified": true},
		"DevMode": false,
	}
	if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute base.html with dashboard content: %v", err)
	}
	out := buf.String()

	metricIDs := []string{
		"bt-metric-elapsed-time",
		"bt-metric-distance",
		"bt-metric-pace",
		"bt-metric-stroke-rate",
		"bt-metric-power",
		"bt-metric-calories",
		"bt-metric-stroke-count",
		"bt-metric-heart-rate",
	}

	for _, id := range metricIDs {
		idx := strings.Index(out, `id="`+id+`"`)
		if idx == -1 {
			t.Fatalf("rendered output missing stat-value element id=%q", id)
		}
		// Look at the class attribute on this same element (a short window
		// right after the id, well within the div's own opening tag).
		end := idx + 400
		if end > len(out) {
			end = len(out)
		}
		window := out[idx:end]
		for _, want := range []string{"whitespace-normal", "break-words", "min-w-0"} {
			if !strings.Contains(window, want) {
				t.Errorf("stat-value %q missing overflow-guard class %q; element region: %q", id, want, window)
			}
		}
	}

	if !strings.Contains(out, `id="bt-metrics" class="hidden grid gap-3 sm:grid-cols-2 lg:grid-cols-4"`) {
		t.Errorf("#bt-metrics grid lost its responsive reflow classes (grid gap-3 sm:grid-cols-2 lg:grid-cols-4); rendered: %q", out)
	}
}

// TestMainLayoutUsesNarrowContentColumn renders the REAL embedded base.html
// (via web.FS, not a mock) and asserts the shared <main> content wrapper caps
// at max-w-md, and that no per-page override re-widens it to max-w-4xl.
// Re-introducing the base.html max-w-4xl cap turns this test red.
func TestMainLayoutUsesNarrowContentColumn(t *testing.T) {
	base, err := template.ParseFS(FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base.html: %v", err)
	}
	tmpl, err := base.ParseFS(FS, "templates/dashboard.html")
	if err != nil {
		t.Fatalf("parse dashboard.html: %v", err)
	}

	var buf strings.Builder
	data := map[string]any{
		"User":    map[string]any{"Email": "rower@example.com", "EmailVerified": true},
		"DevMode": false,
	}
	if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute base.html with dashboard content: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `<main class="container mx-auto px-4 py-8 max-w-md">`) {
		t.Errorf("<main> wrapper does not carry max-w-md; rendered: %q", out)
	}
	if strings.Contains(out, "max-w-4xl") {
		t.Errorf("rendered output still contains the widened max-w-4xl cap")
	}
}
