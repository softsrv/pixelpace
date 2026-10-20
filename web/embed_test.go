package web

import (
	"html/template"
	"io/fs"
	"strings"
	"testing"
	"time"
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

func extractTagClassTokens(htmlStr, tag string) ([]string, bool) {
	openTagPrefix := "<" + tag
	tagIdx := strings.Index(htmlStr, openTagPrefix)
	if tagIdx == -1 {
		return nil, false
	}
	endTagIdx := strings.Index(htmlStr[tagIdx:], ">")
	if endTagIdx == -1 {
		return nil, false
	}
	tagContent := htmlStr[tagIdx : tagIdx+endTagIdx]
	classPrefix := `class="`
	classIdx := strings.Index(tagContent, classPrefix)
	if classIdx == -1 {
		return nil, false
	}
	classValStart := classIdx + len(classPrefix)
	quoteIdx := strings.Index(tagContent[classValStart:], `"`)
	if quoteIdx == -1 {
		return nil, false
	}
	classVal := tagContent[classValStart : classValStart+quoteIdx]
	return strings.Fields(classVal), true
}

func containsToken(tokens []string, want string) bool {
	for _, tok := range tokens {
		if tok == want {
			return true
		}
	}
	return false
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

	mainTokens, ok := extractTagClassTokens(out, "main")
	if !ok {
		t.Fatalf("<main> tag or class attribute not found; rendered: %q", out)
	}
	for _, want := range []string{"container", "mx-auto", "max-w-md"} {
		if !containsToken(mainTokens, want) {
			t.Errorf("<main> wrapper does not carry %q; class tokens: %v", want, mainTokens)
		}
	}
	if strings.Contains(out, "max-w-4xl") {
		t.Errorf("rendered output still contains the widened max-w-4xl cap")
	}
}

// TestEveryPageHasFooter verifies CLM-1: every page template parsed into the base set
// (base layout + partials) renders a document where </main> comes before <footer and
// <footer before </body>, the footer text contains "2026 softsrv", <body> tokens
// include flex, flex-col, and min-h-screen, and <main> tokens include flex-1.
func TestEveryPageHasFooter(t *testing.T) {
	base, err := template.ParseFS(FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base.html: %v", err)
	}
	if _, err := base.ParseFS(FS, "templates/partials/*.html"); err != nil {
		t.Fatalf("parse partials: %v", err)
	}

	pages := []struct {
		name     string
		pagePath string
	}{
		{name: "login", pagePath: "templates/auth/login.html"},
		{name: "register", pagePath: "templates/auth/register.html"},
		{name: "forgot-password", pagePath: "templates/auth/forgot-password.html"},
		{name: "reset-password", pagePath: "templates/auth/reset-password.html"},
		{name: "verify-email", pagePath: "templates/auth/verify-email.html"},
		{name: "dashboard", pagePath: "templates/dashboard.html"},
		{name: "profile", pagePath: "templates/profile.html"},
	}

	data := map[string]any{
		"User": map[string]any{
			"Email":         "rower@example.com",
			"EmailVerified": true,
			"CreatedAt":     struct{ Time time.Time }{Time: time.Now()},
		},
		"DevMode": false,
		"Token":   "tok123",
	}

	for _, tc := range pages {
		t.Run(tc.name, func(t *testing.T) {
			clone, err := base.Clone()
			if err != nil {
				t.Fatalf("clone base template: %v", err)
			}
			if _, err := clone.ParseFS(FS, tc.pagePath); err != nil {
				t.Fatalf("parse %s: %v", tc.pagePath, err)
			}

			var buf strings.Builder
			if err := clone.ExecuteTemplate(&buf, "base.html", data); err != nil {
				t.Fatalf("execute base.html with %s: %v", tc.name, err)
			}
			out := buf.String()

			mainEnd := strings.Index(out, "</main>")
			footerStart := strings.Index(out, "<footer")
			bodyEnd := strings.Index(out, "</body>")

			if mainEnd == -1 {
				t.Errorf("%s: rendered output missing </main>", tc.name)
			}
			if footerStart == -1 {
				t.Errorf("%s: rendered output missing <footer", tc.name)
			}
			if bodyEnd == -1 {
				t.Errorf("%s: rendered output missing </body>", tc.name)
			}
			if mainEnd != -1 && footerStart != -1 && bodyEnd != -1 {
				if mainEnd >= footerStart || footerStart >= bodyEnd {
					t.Errorf("%s: expected </main> < <footer < </body>, got mainEnd=%d, footerStart=%d, bodyEnd=%d", tc.name, mainEnd, footerStart, bodyEnd)
				}
			}

			footerEnd := strings.Index(out, "</footer>")
			if footerStart != -1 && footerEnd != -1 && footerEnd > footerStart {
				footerText := out[footerStart:footerEnd]
				if !strings.Contains(footerText, "2026 softsrv") {
					t.Errorf("%s: footer text does not contain \"2026 softsrv\"; footer: %q", tc.name, footerText)
				}
			}

			bodyTokens, ok := extractTagClassTokens(out, "body")
			if !ok {
				t.Errorf("%s: <body> tag or class attribute not found", tc.name)
			} else {
				for _, want := range []string{"flex", "flex-col", "min-h-screen"} {
					if !containsToken(bodyTokens, want) {
						t.Errorf("%s: <body> missing class token %q; body tokens: %v", tc.name, want, bodyTokens)
					}
				}
			}

			mainTokens, ok := extractTagClassTokens(out, "main")
			if !ok {
				t.Errorf("%s: <main> tag or class attribute not found", tc.name)
			} else {
				if !containsToken(mainTokens, "flex-1") {
					t.Errorf("%s: <main> missing class token \"flex-1\"; main tokens: %v", tc.name, mainTokens)
				}
			}
		})
	}
}

// TestOnlySharedLayoutHasDocumentShell verifies CLM-3: walks every templates/**
// file in web.FS (fs.WalkDir) and asserts that only templates/base.html contains
// <!DOCTYPE, <html or <footer (case-insensitive).
func TestOnlySharedLayoutHasDocumentShell(t *testing.T) {
	var checkedCount int
	err := fs.WalkDir(FS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		checkedCount++
		data, err := fs.ReadFile(FS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := strings.ToLower(string(data))
		isBase := path == "templates/base.html"

		for _, token := range []string{"<!doctype", "<html", "<footer"} {
			hasToken := strings.Contains(content, token)
			if isBase {
				if !hasToken {
					t.Errorf("%s missing required document shell token %q", path, token)
				}
			} else {
				if hasToken {
					t.Errorf("%s contains forbidden document shell/footer token %q; only templates/base.html may define it", path, token)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
	if checkedCount == 0 {
		t.Fatalf("no template files checked")
	}
}

// TestPartialsHaveNoFooter verifies CLM-4: builds the real base set the way
// cmd/app/renderer does (base.html plus partials from web.FS). It executes each
// of the error, flash and session-list partials by name with representative data,
// asserts err == nil and non-empty output (so a typo'd name cannot pass vacuously),
// and asserts the output contains neither <footer nor "2026 softsrv".
func TestPartialsHaveNoFooter(t *testing.T) {
	base, err := template.ParseFS(FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base.html: %v", err)
	}
	if _, err := base.ParseFS(FS, "templates/partials/*.html"); err != nil {
		t.Fatalf("parse partials: %v", err)
	}

	partials := []struct {
		name string
		data any
	}{
		{
			name: "partials/error.html",
			data: map[string]any{"Error": "Something went wrong"},
		},
		{
			name: "partials/flash.html",
			data: map[string]any{"Message": "Profile updated successfully"},
		},
		{
			name: "partials/session-list.html",
			data: map[string]any{
				"Sessions": []any{
					map[string]any{
						"ID": "session-1",
						"DeviceName": struct {
							Valid  bool
							String string
						}{Valid: true, String: "Firefox on Linux"},
						"IpAddress":  "192.168.1.100",
						"LastUsedAt": struct{ Time time.Time }{Time: time.Now()},
						"CreatedAt":  struct{ Time time.Time }{Time: time.Now().Add(-24 * time.Hour)},
					},
				},
				"UserID": "user-123",
			},
		},
	}

	for _, tc := range partials {
		t.Run(tc.name, func(t *testing.T) {
			clone, err := base.Clone()
			if err != nil {
				t.Fatalf("clone base template: %v", err)
			}
			var buf strings.Builder
			if err := clone.ExecuteTemplate(&buf, tc.name, tc.data); err != nil {
				t.Fatalf("execute %s: %v", tc.name, err)
			}
			out := buf.String()
			if strings.TrimSpace(out) == "" {
				t.Fatalf("%s produced empty output", tc.name)
			}
			lower := strings.ToLower(out)
			if strings.Contains(lower, "<footer") {
				t.Errorf("%s output contains <footer; output: %q", tc.name, out)
			}
			if strings.Contains(lower, "2026 softsrv") {
				t.Errorf("%s output contains \"2026 softsrv\"; output: %q", tc.name, out)
			}
		})
	}
}
