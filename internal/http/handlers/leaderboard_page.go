package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/http/middleware"
)

// presetOptioner lists the presets a leaderboard can be shown for.
type presetOptioner interface {
	PresetOptions(ctx context.Context) ([]app.PresetOption, error)
}

var _ presetOptioner = (*app.RoomService)(nil)

// LeaderboardPageHandler serves the leaderboards page.
type LeaderboardPageHandler struct {
	leaderboard leaderboardServicer
	presets     presetOptioner
	renderer    *TemplateRenderer
}

// NewLeaderboardPageHandler constructs a LeaderboardPageHandler.
func NewLeaderboardPageHandler(leaderboard leaderboardServicer, presets presetOptioner, renderer *TemplateRenderer) *LeaderboardPageHandler {
	return &LeaderboardPageHandler{leaderboard: leaderboard, presets: presets, renderer: renderer}
}

// LeaderboardFilter is one choice in a row of filter tabs.
type LeaderboardFilter struct {
	Label  string
	URL    string
	Active bool
}

// LeaderboardTabs is a labelled row of filter tabs. Big marks the mode row.
type LeaderboardTabs struct {
	Label   string
	Big     bool
	Filters []LeaderboardFilter
}

// LeaderboardEntry is one ranked racer as the page shows them.
type LeaderboardEntry struct {
	Rank int
	Name string
	Time string
	Pace string
	You  bool
}

// LeaderboardBoard is everything the board region renders.
type LeaderboardBoard struct {
	Mode      LeaderboardTabs
	Distance  LeaderboardTabs
	Scope     LeaderboardTabs
	Period    LeaderboardTabs
	RaceLabel string
	TimeTrial bool
	Friends   bool
	Window    string
	Entries   []LeaderboardEntry
	// You is the caller's entry, when they are ranked.
	You *LeaderboardEntry
}

type leaderboardChoice struct{ value, label string }

var (
	leaderboardModes   = []leaderboardChoice{{"race", "Races"}, {"time-trial", "Time trials"}}
	leaderboardScopes  = []leaderboardChoice{{"global", "Everyone"}, {"friends", "Friends"}}
	leaderboardWindows = []leaderboardChoice{{"all-time", "All-time"}, {"month", "Month"}, {"week", "Week"}}
)

// leaderboardQuery is the page's selected filters.
type leaderboardQuery struct {
	preset              uuid.UUID
	mode, scope, window string
}

func (q leaderboardQuery) url() string {
	v := url.Values{}
	v.Set("race_type", q.preset.String())
	v.Set("mode", q.mode)
	v.Set("scope", q.scope)
	v.Set("window", q.window)
	return "/leaderboards?" + v.Encode()
}

// choose returns value when it is one of choices, else the first choice.
func choose(value string, choices []leaderboardChoice) string {
	for _, c := range choices {
		if c.value == value {
			return value
		}
	}
	return choices[0].value
}

// Page renders the leaderboards page for the race_type, mode, scope and
// window query parameters. Missing or unknown values fall back to the first
// preset, races, everyone and all-time, so any link lands on a board. An
// htmx request gets only the board region, which carries its own filters.
func (h *LeaderboardPageHandler) Page(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	options, err := h.presets.PresetOptions(r.Context())
	if err == nil && len(options) == 0 {
		err = errors.New("no presets")
	}
	if err != nil {
		leaderboardPageError(w, r, err)
		return
	}
	params := r.URL.Query()
	selected := options[0]
	if id, err := uuid.Parse(params.Get("race_type")); err == nil {
		for _, o := range options {
			if o.ID == id {
				selected = o
			}
		}
	}
	q := leaderboardQuery{
		preset: selected.ID,
		mode:   choose(params.Get("mode"), leaderboardModes),
		scope:  choose(params.Get("scope"), leaderboardScopes),
		window: choose(params.Get("window"), leaderboardWindows),
	}
	rows, err := h.leaderboard.Leaderboard(r.Context(), q.scope, q.window, q.mode, q.preset, user.ID)
	if err != nil {
		leaderboardPageError(w, r, err)
		return
	}

	board := LeaderboardBoard{
		RaceLabel: selected.Label,
		TimeTrial: q.mode == "time-trial",
		Friends:   q.scope == "friends",
		Window:    q.window,
		Entries:   make([]LeaderboardEntry, 0, len(rows)),
	}
	board.Distance = LeaderboardTabs{Label: "Distance"}
	for _, o := range options {
		next := q
		next.preset = o.ID
		board.Distance.Filters = append(board.Distance.Filters, LeaderboardFilter{Label: o.Label, URL: next.url(), Active: o.ID == q.preset})
	}
	board.Mode = LeaderboardTabs{Label: "Mode", Big: true, Filters: filters(leaderboardModes, q.mode, func(v string) string { next := q; next.mode = v; return next.url() })}
	board.Scope = LeaderboardTabs{Label: "Racers", Filters: filters(leaderboardScopes, q.scope, func(v string) string { next := q; next.scope = v; return next.url() })}
	board.Period = LeaderboardTabs{Label: "Period", Filters: filters(leaderboardWindows, q.window, func(v string) string { next := q; next.window = v; return next.url() })}

	for i, row := range rows {
		// Equal times share a rank (1, 1, 3), as on a race results board.
		rank := i + 1
		if i > 0 && row.ElapsedMilliseconds == rows[i-1].ElapsedMilliseconds {
			rank = board.Entries[i-1].Rank
		}
		board.Entries = append(board.Entries, LeaderboardEntry{
			Rank: rank,
			Name: row.Name,
			Time: formatDuration(int64(row.ElapsedMilliseconds)),
			Pace: formatPace(int64(row.ElapsedMilliseconds), selected.TargetMillimeters),
			You:  row.UserID == user.ID,
		})
	}
	for i := range board.Entries {
		if board.Entries[i].You {
			board.You = &board.Entries[i]
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("HX-Request") == "true" {
		h.renderer.Fragment(w, http.StatusOK, "leaderboards.html", "leaderboard-board", board)
		return
	}
	h.renderer.Page(w, http.StatusOK, "leaderboards.html", map[string]any{"User": user, "Board": board})
}

func leaderboardPageError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "leaderboards page", "error", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func filters(choices []leaderboardChoice, active string, link func(string) string) []LeaderboardFilter {
	out := make([]LeaderboardFilter, len(choices))
	for i, c := range choices {
		out[i] = LeaderboardFilter{Label: c.label, URL: link(c.value), Active: c.value == active}
	}
	return out
}

// formatDuration renders milliseconds as m:ss.t, matching the race results.
func formatDuration(ms int64) string {
	if ms < 0 {
		return ""
	}
	tenths := ms / 100
	seconds := tenths / 10
	return fmt.Sprintf("%d:%02d.%d", seconds/60, seconds%60, tenths%10)
}

// formatPace renders the average time per 500m as m:ss.t, matching the race
// results, or "—" when it cannot be computed.
func formatPace(elapsedMs, distanceMm int64) string {
	if elapsedMs <= 0 || distanceMm <= 0 {
		return "—"
	}
	// pace (ms per 500m) = elapsed * 500m / distance; round to nearest tenth of a second.
	paceMs := float64(elapsedMs) * 500000 / float64(distanceMm)
	tenths := int64(paceMs/100 + 0.5)
	seconds := tenths / 10
	return fmt.Sprintf("%d:%02d.%d", seconds/60, seconds%60, tenths%10)
}
