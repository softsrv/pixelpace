package handlers_test

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
)

var (
	lbType500  = uuid.MustParse("01950000-0000-7000-8000-000000000001")
	lbType2000 = uuid.MustParse("01950000-0000-7000-8000-000000000003")
)

type stubRaceTypeOptions struct {
	options []app.RaceTypeOption
	err     error
}

func (s stubRaceTypeOptions) RaceTypeOptions(context.Context) ([]app.RaceTypeOption, error) {
	return s.options, s.err
}

var lbOptions = stubRaceTypeOptions{options: []app.RaceTypeOption{
	{ID: lbType500, Label: "500m", TargetMillimeters: 500000},
	{ID: lbType2000, Label: "2000m", TargetMillimeters: 2000000},
}}

type leaderboardCall struct {
	scope, window, mode string
	raceType, caller    uuid.UUID
}

func serveLeaderboardPage(t *testing.T, rows []app.LeaderboardRow, viewer uuid.UUID, target string, htmx bool) (*httptest.ResponseRecorder, leaderboardCall) {
	t.Helper()
	var call leaderboardCall
	svc := &stubLeaderboardService{listFn: func(_ context.Context, scope, window, mode string, raceType, caller uuid.UUID) ([]app.LeaderboardRow, error) {
		call = leaderboardCall{scope, window, mode, raceType, caller}
		return rows, nil
	}}
	h := handlers.NewLeaderboardPageHandler(svc, lbOptions, newProductionRenderer(t))
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	return serveAuthenticated(t, db.User{ID: viewer, Email: "viewer@example.com", EmailVerified: true}, http.HandlerFunc(h.Page), req), call
}

// activeTab finds the label of the selected tab within the nav labelled label.
func activeTab(t *testing.T, body, label string) string {
	t.Helper()
	nav := regexp.MustCompile(`(?s)<nav class="space-y-2" aria-label="` + label + `">(.*?)</nav>`).FindStringSubmatch(body)
	if nav == nil {
		t.Fatalf("no %s tabs in %s", label, body)
	}
	tab := regexp.MustCompile(`aria-current="page">([^<]+)</a>`).FindAllStringSubmatch(nav[1], -1)
	if len(tab) != 1 {
		t.Fatalf("%s tabs: %d active, want 1", label, len(tab))
	}
	return tab[0][1]
}

func TestLeaderboardPageDefaultsAndRendersRanking(t *testing.T) {
	viewer, other, third := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rows := []app.LeaderboardRow{
		{UserID: other, Name: "Speedy", ElapsedMilliseconds: 95000},
		{UserID: viewer, Name: "<Me>", ElapsedMilliseconds: 95000},
		{UserID: third, Name: "Steady", ElapsedMilliseconds: 101234},
	}
	rr, call := serveLeaderboardPage(t, rows, viewer, "/leaderboards", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}
	if call != (leaderboardCall{"global", "all-time", "race", lbType500, viewer}) {
		t.Fatalf("defaults = %+v", call)
	}
	body := rr.Body.String()
	for label, want := range map[string]string{"Mode": "Races", "Distance": "500m", "Racers": "Everyone", "Period": "All-time"} {
		if got := activeTab(t, body, label); got != want {
			t.Errorf("%s tab = %q, want %q", label, got, want)
		}
	}
	for _, want := range []string{
		`<title>Leaderboards</title>`,
		`Race leaderboard`,
		// Ties share a rank; pace is per 500m of the race distance.
		`<tr class="pp-rank-1">`, `<td>1:35.0</td>`, `<td>1:35</td>`,
		`<tr class="pp-rank-1 font-bold" aria-current="true">`,
		`&lt;Me&gt; <span class="pp-you-tag pp-arcade">You</span>`,
		`<tr class="pp-rank-3">`, `<td>1:41.2</td>`, `<td>1:41</td>`,
		`#1 &middot; 1:35.0`,
		`href="/leaderboards"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "<Me>") {
		t.Errorf("racer name is not escaped")
	}
}

func TestLeaderboardPageFiltersAndLinks(t *testing.T) {
	viewer := uuid.Must(uuid.NewV7())
	target := "/leaderboards?race_type=" + lbType2000.String() + "&mode=time-trial&scope=friends&window=week"
	rr, call := serveLeaderboardPage(t, nil, viewer, target, false)
	if call != (leaderboardCall{"friends", "week", "time-trial", lbType2000, viewer}) {
		t.Fatalf("filters = %+v", call)
	}
	body := html.UnescapeString(rr.Body.String())
	for label, want := range map[string]string{"Mode": "Time trials", "Distance": "2000m", "Racers": "Friends", "Period": "Week"} {
		if got := activeTab(t, body, label); got != want {
			t.Errorf("%s tab = %q, want %q", label, got, want)
		}
	}
	// Each tab changes one filter and keeps the rest, and swaps the board in place.
	for _, want := range []string{
		`href="/leaderboards?mode=race&race_type=` + lbType2000.String() + `&scope=friends&window=week" hx-get="/leaderboards?mode=race&race_type=` + lbType2000.String() + `&scope=friends&window=week" hx-target="#leaderboard-board" hx-swap="outerHTML" hx-push-url="true"`,
		`href="/leaderboards?mode=time-trial&race_type=` + lbType500.String() + `&scope=friends&window=week"`,
		`href="/leaderboards?mode=time-trial&race_type=` + lbType2000.String() + `&scope=global&window=week"`,
		`href="/leaderboards?mode=time-trial&race_type=` + lbType2000.String() + `&scope=friends&window=month"`,
		`Time trial leaderboard`,
		`None of your friends have a finished 2000m time trial this week.`,
		`start it on your own`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "<table") || strings.Contains(body, "Your best") {
		t.Errorf("empty board renders a table or a personal best")
	}
}

func TestLeaderboardPageUnknownFiltersFallBack(t *testing.T) {
	viewer := uuid.Must(uuid.NewV7())
	for _, target := range []string{
		"/leaderboards?race_type=not-a-uuid&mode=x&scope=y&window=z",
		"/leaderboards?race_type=" + uuid.Must(uuid.NewV7()).String(),
	} {
		rr, call := serveLeaderboardPage(t, nil, viewer, target, false)
		if rr.Code != http.StatusOK || call != (leaderboardCall{"global", "all-time", "race", lbType500, viewer}) {
			t.Fatalf("%s: status=%d call=%+v", target, rr.Code, call)
		}
	}
}

func TestLeaderboardPageHTMXReturnsBoardOnly(t *testing.T) {
	viewer := uuid.Must(uuid.NewV7())
	rr, _ := serveLeaderboardPage(t, []app.LeaderboardRow{{UserID: viewer, Name: "Me", ElapsedMilliseconds: 1000}}, viewer, "/leaderboards?mode=time-trial", true)
	body := strings.TrimSpace(rr.Body.String())
	if rr.Code != http.StatusOK || !strings.HasPrefix(body, `<section id="leaderboard-board"`) || strings.Contains(body, "<html") || strings.Contains(body, "Hall of fame") {
		t.Fatalf("status=%d body=%s", rr.Code, body)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rr.Header().Get("Cache-Control"))
	}
}

func TestLeaderboardPageErrors(t *testing.T) {
	viewer := uuid.Must(uuid.NewV7())
	user := db.User{ID: viewer, Email: "viewer@example.com", EmailVerified: true}
	failing := &stubLeaderboardService{listFn: func(context.Context, string, string, string, uuid.UUID, uuid.UUID) ([]app.LeaderboardRow, error) {
		return nil, errors.New("private database details")
	}}
	for name, h := range map[string]*handlers.LeaderboardPageHandler{
		"race types":  handlers.NewLeaderboardPageHandler(&stubLeaderboardService{}, stubRaceTypeOptions{err: errors.New("private database details")}, newProductionRenderer(t)),
		"no types":    handlers.NewLeaderboardPageHandler(&stubLeaderboardService{}, stubRaceTypeOptions{}, newProductionRenderer(t)),
		"leaderboard": handlers.NewLeaderboardPageHandler(failing, lbOptions, newProductionRenderer(t)),
	} {
		rr := serveAuthenticated(t, user, http.HandlerFunc(h.Page), httptest.NewRequest(http.MethodGet, "/leaderboards", nil))
		if rr.Code != http.StatusInternalServerError || strings.Contains(rr.Body.String(), "private") {
			t.Errorf("%s: status=%d body=%s", name, rr.Code, rr.Body)
		}
	}
	rr := httptest.NewRecorder()
	handlers.NewLeaderboardPageHandler(nil, nil, nil).Page(rr, httptest.NewRequest(http.MethodGet, "/leaderboards", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing user: status=%d", rr.Code)
	}
}
