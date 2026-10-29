package web

import (
	"html/template"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
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

// TestRacePageRendersTrackDataAndScripts renders the REAL embedded base.html +
// race.html and asserts the race view receives what race.mjs reads: the
// viewer, target distance, status URL, race ID and lane roster, with the
// vendored PixiJS scripts loading before the race module.
func TestRacePageRendersTrackDataAndScripts(t *testing.T) {
	render := func(data map[string]any) string {
		t.Helper()
		tmpl, err := template.ParseFS(FS, "templates/base.html", "templates/race.html")
		if err != nil {
			t.Fatalf("parse race page: %v", err)
		}
		var buf strings.Builder
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Fatalf("execute race page: %v", err)
		}
		return buf.String()
	}
	raceID := uuid.MustParse("01950000-0000-7000-8000-0000000000aa")
	selfID := uuid.MustParse("01950000-0000-7000-8000-0000000000bb")
	rivalID := uuid.MustParse("01950000-0000-7000-8000-0000000000cc")
	roomID := uuid.MustParse("01950000-0000-7000-8000-0000000000dd")
	data := map[string]any{
		"User":    map[string]any{"ID": selfID, "Email": "rower@example.com"},
		"DevMode": true,
		"Race":    map[string]any{"RoomID": roomID, "RaceID": &raceID, "Label": "2000m", "TargetMillimeters": int64(2000000)},
		"Lanes": []map[string]any{
			{"UserID": rivalID, "Label": "Ada", "IsSelf": false},
			{"UserID": selfID, "Label": "Me", "IsSelf": true},
		},
	}

	out := render(data)

	for _, want := range []string{
		`data-self-id="` + selfID.String() + `"`,
		`data-target-mm="2000000"`,
		`data-status-url="/rooms/` + roomID.String() + `/race/status"`,
		`data-race-id="` + raceID.String() + `"`,
		`data-ready-url="/rooms/` + roomID.String() + `/race/ready"`,
		`data-lobby-url="/rooms/` + roomID.String() + `/lobby"`,
		`<li data-user-id="` + rivalID.String() + `" data-self="false">Ada</li>`,
		`<li data-user-id="` + selfID.String() + `" data-self="true">Me</li>`,
		`id="rowing-machine-card" class="card bg-base-100 shadow-xl" data-dev-mode="true"`,
		`id="bt-connect-btn"`,
		`id="bt-metrics"`,
		`data-metric="elapsedTime"`,
		`data-metric="distance"`,
		`data-metric="pace"`,
		`data-metric="strokeRate"`,
		`data-metric="power"`,
		`data-metric="calories"`,
		`data-metric="strokeCount"`,
		`data-metric="heartRate"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered race page missing %q", want)
		}
	}
	if strings.Contains(out, `id="bt-rower-canvas"`) {
		t.Errorf("race page should not render the dashboard rower canvas")
	}
	scripts := []string{"/static/js/app.js", "/static/js/vendor/pixi/pixi.min.js", "/static/js/vendor/pixi/unsafe-eval.min.js", "/static/js/race.mjs"}
	last := -1
	for _, src := range scripts {
		idx := strings.Index(out, `src="`+src+`"`)
		if idx <= last {
			t.Fatalf("script %s missing or out of order; want order %v", src, scripts)
		}
		last = idx
	}

	data["Race"].(map[string]any)["RaceID"] = (*uuid.UUID)(nil)
	if !strings.Contains(render(data), `data-race-id=""`) {
		t.Errorf("counting-down page should render an empty race ID")
	}
}

// TestSimulatorPanelRendersOnlyInDevMode renders the REAL dashboard and race
// pages. In development the race page shows the PM5 simulator panel (with its
// controls and defaults) right after #bt-connection-info and before
// #bt-metrics, while the dashboard shows no simulator element at all but still
// marks the card data-dev-mode="true". In production neither page renders it.
func TestSimulatorPanelRendersOnlyInDevMode(t *testing.T) {
	raceID := uuid.MustParse("01950000-0000-7000-8000-0000000000aa")
	selfID := uuid.MustParse("01950000-0000-7000-8000-0000000000bb")
	roomID := uuid.MustParse("01950000-0000-7000-8000-0000000000dd")
	pages := []struct {
		name     string
		page     string
		simPanel bool
		data     func(devMode bool) map[string]any
	}{
		{name: "dashboard", page: "templates/dashboard.html", simPanel: false, data: func(devMode bool) map[string]any {
			return map[string]any{
				"User":    map[string]any{"Email": "rower@example.com", "EmailVerified": true},
				"DevMode": devMode,
			}
		}},
		{name: "race", page: "templates/race.html", simPanel: true, data: func(devMode bool) map[string]any {
			return map[string]any{
				"User":    map[string]any{"ID": selfID, "Email": "rower@example.com"},
				"DevMode": devMode,
				"Race":    map[string]any{"RoomID": roomID, "RaceID": &raceID, "Label": "2000m", "TargetMillimeters": int64(2000000)},
				"Lanes":   []map[string]any{{"UserID": selfID, "Label": "Me", "IsSelf": true}},
			}
		}},
	}
	for _, tc := range pages {
		t.Run(tc.name, func(t *testing.T) {
			render := func(devMode bool) string {
				t.Helper()
				tmpl, err := template.ParseFS(FS, "templates/base.html", tc.page)
				if err != nil {
					t.Fatalf("parse %s: %v", tc.page, err)
				}
				var buf strings.Builder
				if err := tmpl.ExecuteTemplate(&buf, "base.html", tc.data(devMode)); err != nil {
					t.Fatalf("execute %s: %v", tc.page, err)
				}
				return buf.String()
			}

			dev := render(true)
			if !tc.simPanel {
				if !strings.Contains(dev, `data-dev-mode="true"`) {
					t.Errorf("dev-mode %s page should mark the card data-dev-mode=true", tc.name)
				}
				for _, banned := range []string{"Simulator (development only)", "sim-panel", "sim-spm", "sim-pace", "sim-start", "data-sim-control"} {
					if strings.Contains(dev, banned) {
						t.Errorf("dev-mode %s page renders %q", tc.name, banned)
					}
				}
			} else {
				for _, want := range []string{
					`Simulator (development only)`,
					`id="sim-panel"`,
					`data-sim-control="panel"`,
					`id="sim-spm" type="number" min="0" max="60" step="1" value="26"`,
					`data-sim-control="spm"`,
					`id="sim-pace" type="text" value="2:12"`,
					`data-sim-control="pace"`,
					`data-sim-control="start">Stop rowing</button>`,
					`id="sim-start"`,
				} {
					if !strings.Contains(dev, want) {
						t.Errorf("dev-mode %s page missing %q", tc.name, want)
					}
				}
				info := strings.Index(dev, `id="bt-connection-info"`)
				panel := strings.Index(dev, `id="sim-panel"`)
				metrics := strings.Index(dev, `id="bt-metrics"`)
				if info == -1 || panel <= info {
					t.Errorf("%s: #sim-panel should follow #bt-connection-info (info=%d panel=%d)", tc.name, info, panel)
				}
				if metrics == -1 || metrics <= panel {
					t.Errorf("%s: #sim-panel should precede #bt-metrics (panel=%d metrics=%d)", tc.name, panel, metrics)
				}
			}

			prod := render(false)
			if !strings.Contains(prod, `data-dev-mode="false"`) {
				t.Errorf("production %s page should mark the card data-dev-mode=false", tc.name)
			}
			for _, banned := range []string{"Simulator (development only)", "sim-panel", "sim-spm", "sim-pace", "sim-start", "data-sim-control"} {
				if strings.Contains(prod, banned) {
					t.Errorf("production %s page renders %q", tc.name, banned)
				}
			}
		})
	}
}

// TestLobbyPageRendersHostControlsAndLivePanel renders the REAL lobby.html with
// the handler's data types and checks the wiring HTMX and app.js rely on.
func TestLobbyPageRendersHostControlsAndLivePanel(t *testing.T) {
	roomID := uuid.MustParse("01950000-0000-7000-8000-0000000000dd")
	hostID := uuid.MustParse("01950000-0000-7000-8000-0000000000bb")
	guestID := uuid.MustParse("01950000-0000-7000-8000-0000000000cc")
	friendID := uuid.MustParse("01950000-0000-7000-8000-0000000000ee")
	typeA := uuid.MustParse("01950000-0000-7000-8000-000000000001")
	typeB := uuid.MustParse("01950000-0000-7000-8000-000000000003")
	render := func(isHost, canStart, selfReady bool) string {
		t.Helper()
		tmpl, err := template.ParseFS(FS, "templates/base.html", "templates/lobby.html")
		if err != nil {
			t.Fatalf("parse lobby page: %v", err)
		}
		lobby := app.RoomLobby{
			RoomID: roomID, Status: "waiting", JoinCode: "ABC234", RaceTypeID: typeB, RaceLabel: "2000m",
			IsHost: isHost, CanStart: canStart, SelfReady: selfReady,
			Participants: []app.RoomRosterEntry{
				{PublicPlayer: app.PublicPlayer{UserID: hostID, Name: "Hosty"}, Host: true},
				{PublicPlayer: app.PublicPlayer{UserID: guestID, Name: "Guesty"}, Ready: true},
			},
		}
		var buf strings.Builder
		err = tmpl.ExecuteTemplate(&buf, "base.html", map[string]any{
			"User":      map[string]any{"Email": "host@example.com"},
			"Lobby":     lobby,
			"RaceTypes": []app.RaceTypeOption{{ID: typeA, Label: "500m"}, {ID: typeB, Label: "2000m"}},
			"Friends":   []map[string]any{{"UserID": friendID, "Name": "Friendly", "Invited": false, "Joined": false}},
			"JoinURL":   "https://pixelpace.test/join/ABC234",
		})
		if err != nil {
			t.Fatalf("execute lobby page: %v", err)
		}
		return buf.String()
	}

	host := render(true, true, false)
	for _, want := range []string{
		`hx-post="/rooms/` + roomID.String() + `/race-type"`,
		`<option value="` + typeB.String() + `" selected>2000m</option>`,
		`value="https://pixelpace.test/join/ABC234"`,
		`data-copy-target="#join-url"`,
		`>ABC234</span>`,
		`data-dialog-open="#invite-dialog"`,
		`hx-post="/rooms/` + roomID.String() + `/invites"`,
		`name="user_id" value="` + friendID.String() + `"`,
		`hx-get="/rooms/` + roomID.String() + `/lobby/panel" hx-trigger="every 2s, room-updated from:body"`,
		`hx-post="/rooms/` + roomID.String() + `/start"`,
		`hx-post="/rooms/` + roomID.String() + `/leave"`,
		`Guesty`,
	} {
		if !strings.Contains(host, want) {
			t.Errorf("host lobby missing %q", want)
		}
	}
	if regexpDisabledStart.MatchString(host) {
		t.Errorf("host lobby start button is disabled though the race can start")
	}
	if !regexpDisabledStart.MatchString(render(true, false, false)) {
		t.Errorf("host lobby start button should be disabled until guests are ready")
	}

	guest := render(false, false, false)
	if strings.Contains(guest, "/race-type") || strings.Contains(guest, "/start") {
		t.Errorf("guest lobby shows host controls")
	}
	if !strings.Contains(guest, `hx-post="/rooms/`+roomID.String()+`/ready"`) {
		t.Errorf("guest lobby missing the ready button")
	}
	if !strings.Contains(render(false, false, true), `hx-post="/rooms/`+roomID.String()+`/unready"`) {
		t.Errorf("ready guest lobby missing the not-ready button")
	}
}

var regexpDisabledStart = regexp.MustCompile(`id="lobby-start"[^>]*\sdisabled`)

// TestDashboardRaceFragmentsRender executes the REAL dashboard fragments the
// race card loads, with the handler's data types.
func TestDashboardRaceFragmentsRender(t *testing.T) {
	tmpl, err := template.ParseFS(FS, "templates/base.html", "templates/dashboard.html")
	if err != nil {
		t.Fatalf("parse dashboard: %v", err)
	}
	typeID := uuid.MustParse("01950000-0000-7000-8000-000000000003")
	inviteID := uuid.MustParse("01950000-0000-7000-8000-0000000000ff")
	var card, invites strings.Builder
	if err := tmpl.ExecuteTemplate(&card, "race-card", map[string]any{"RaceTypes": []app.RaceTypeOption{{ID: typeID, Label: "2000m"}}}); err != nil {
		t.Fatalf("execute race-card: %v", err)
	}
	for _, want := range []string{`hx-post="/rooms"`, `<option value="` + typeID.String() + `">2000m</option>`, `hx-post="/rooms/join"`, `name="join_code"`, `hx-get="/invites"`} {
		if !strings.Contains(card.String(), want) {
			t.Errorf("race card missing %q", want)
		}
	}
	if err := tmpl.ExecuteTemplate(&invites, "room-invites", map[string]any{"Invites": []app.RoomInvite{
		{ID: inviteID, RaceLabel: "500m", Inviter: app.PublicPlayer{Name: "Ada"}},
	}}); err != nil {
		t.Fatalf("execute room-invites: %v", err)
	}
	for _, want := range []string{"<strong>Ada</strong> invited you to a 500m race.", `hx-post="/invites/` + inviteID.String() + `/accept"`, `hx-post="/invites/` + inviteID.String() + `/decline"`} {
		if !strings.Contains(invites.String(), want) {
			t.Errorf("invites missing %q in %q", want, invites.String())
		}
	}
}
