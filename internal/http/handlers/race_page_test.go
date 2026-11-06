package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/web"
)

type stubRoomRaceViewer struct {
	roomRaceFn func(ctx context.Context, roomID, userID uuid.UUID) (app.RoomRace, error)
	readyFn    func(ctx context.Context, roomID, userID uuid.UUID) error
	resultsFn  func(ctx context.Context, roomID, userID uuid.UUID) (app.RaceResults, error)
}

func (s *stubRoomRaceViewer) RoomRace(ctx context.Context, roomID, userID uuid.UUID) (app.RoomRace, error) {
	return s.roomRaceFn(ctx, roomID, userID)
}

func (s *stubRoomRaceViewer) MarkEquipmentReady(ctx context.Context, roomID, userID uuid.UUID) error {
	return s.readyFn(ctx, roomID, userID)
}

func (s *stubRoomRaceViewer) Results(ctx context.Context, roomID, userID uuid.UUID) (app.RaceResults, error) {
	return s.resultsFn(ctx, roomID, userID)
}

type raceEndpoint int

const (
	racePageEndpoint raceEndpoint = iota
	raceStatusEndpoint
	raceReadyEndpoint
	raceResultsEndpoint
)

func (e raceEndpoint) String() string {
	return [...]string{"page", "status", "ready", "results"}[e]
}

func serveRacePage(t *testing.T, viewer *stubRoomRaceViewer, user db.User, pathID string, endpoint raceEndpoint) *httptest.ResponseRecorder {
	t.Helper()
	return serveRaceWith(t, newTestRenderer(t), viewer, user, pathID, endpoint)
}

// newProductionRenderer builds the renderer over the embedded templates the
// way main.go does, so a test sees the markup players get.
func newProductionRenderer(t *testing.T) *handlers.TemplateRenderer {
	t.Helper()
	base, err := template.ParseFS(web.FS, "templates/base.html")
	if err != nil {
		t.Fatalf("parse base template: %v", err)
	}
	if _, err := base.ParseFS(web.FS, "templates/partials/*.html"); err != nil {
		t.Fatalf("parse partials: %v", err)
	}
	return handlers.NewTemplateRenderer(base, web.FS, "templates")
}

func serveRaceWith(t *testing.T, renderer *handlers.TemplateRenderer, viewer *stubRoomRaceViewer, user db.User, pathID string, endpoint raceEndpoint) *httptest.ResponseRecorder {
	t.Helper()
	h := handlers.NewRacePageHandler(viewer, renderer, false)
	method, path, fn := http.MethodGet, "/rooms/"+pathID+"/race", h.Page
	switch endpoint {
	case raceStatusEndpoint:
		path, fn = path+"/status", h.Status
	case raceReadyEndpoint:
		method, path, fn = http.MethodPost, path+"/ready", h.Ready
	case raceResultsEndpoint:
		path, fn = path+"/results", h.Results
	}
	req := httptest.NewRequest(method, path, nil)
	req.SetPathValue("id", pathID)
	return serveAuthenticated(t, user, http.HandlerFunc(fn), req)
}

func racer(id uuid.UUID, name string, ready bool) app.RaceRacer {
	return app.RaceRacer{PublicPlayer: app.PublicPlayer{UserID: id, Name: name}, EquipmentReady: ready}
}

func TestRacePageRendersLanesInParticipantOrder(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID, first, last := uuid.New(), uuid.New(), uuid.New()
	viewer := &stubRoomRaceViewer{roomRaceFn: func(_ context.Context, gotRoom, gotUser uuid.UUID) (app.RoomRace, error) {
		if gotRoom != roomID || gotUser != user.ID {
			t.Errorf("RoomRace(%s, %s), want (%s, %s)", gotRoom, gotUser, roomID, user.ID)
		}
		return app.RoomRace{RoomID: roomID, Label: "2000m", TargetMillimeters: 2000000, Racers: []app.RaceRacer{
			racer(first, "Ada", true), racer(user.ID, "Me", false), racer(last, "Grace", false),
		}}, nil
	}}

	rr := serveRacePage(t, viewer, user, roomID.String(), racePageEndpoint)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	// Racers are labelled by their public names, never by email.
	if want := "race:2000m|Ada|Me*|Grace"; !strings.Contains(rr.Body.String(), want) {
		t.Errorf("body = %q, want it to contain %q", rr.Body.String(), want)
	}
}

func TestRacePageForRoomStillInLobbyRedirectsToLobby(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID := uuid.New()
	viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{}, app.ErrRaceNotStarted
	}}

	rr := serveRacePage(t, viewer, user, roomID.String(), racePageEndpoint)

	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/rooms/"+roomID.String()+"/lobby" {
		t.Fatalf("status = %d, Location = %q; want 303 to the lobby", rr.Code, rr.Header().Get("Location"))
	}
}

func TestRacePageStatusReportsReadinessStartAndRace(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID, raceID, rival := uuid.New(), uuid.New(), uuid.New()
	startsAt := time.Date(2026, 10, 26, 12, 0, 10, 0, time.UTC)
	racers := []app.RaceRacer{racer(user.ID, "Me", true), racer(rival, "Ada", false)}
	for _, tc := range []struct {
		name     string
		startsAt *time.Time
		raceID   *uuid.UUID
		finished bool
		want     string
	}{
		{"connecting", nil, nil, false, `{"race_starts_at":null,"race_id":null,"finished":false}`},
		{"counting down", &startsAt, nil, false, `{"race_starts_at":"2026-10-26T12:00:10Z","race_id":null,"finished":false}`},
		{"racing", &startsAt, &raceID, false, `{"race_starts_at":"2026-10-26T12:00:10Z","race_id":"` + raceID.String() + `","finished":false}`},
		{"finished", &startsAt, &raceID, true, `{"race_starts_at":"2026-10-26T12:00:10Z","race_id":"` + raceID.String() + `","finished":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
				return app.RoomRace{RoomID: roomID, RaceID: tc.raceID, RaceStartsAt: tc.startsAt, Racers: racers, Finished: tc.finished}, nil
			}}

			rr := serveRacePage(t, viewer, user, roomID.String(), raceStatusEndpoint)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if got := rr.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body %q: %v", rr.Body.String(), err)
			}
			var want map[string]any
			_ = json.Unmarshal([]byte(tc.want), &want)
			for k, v := range want {
				if _, present := got[k]; !present {
					t.Errorf("%s missing from status body %s", k, rr.Body.String())
				}
				if fmt.Sprint(got[k]) != fmt.Sprint(v) {
					t.Errorf("%s = %v, want %v", k, got[k], v)
				}
			}
			if _, err := time.Parse(time.RFC3339Nano, fmt.Sprint(got["server_time"])); err != nil {
				t.Errorf("server_time = %v, want an RFC 3339 time", got["server_time"])
			}
			wantRacers := `[{"user_id":"` + user.ID.String() + `","name":"Me","equipment_ready":true},` +
				`{"user_id":"` + rival.String() + `","name":"Ada","equipment_ready":false}]`
			gotRacers, _ := json.Marshal(got["racers"])
			var a, b any
			_ = json.Unmarshal(gotRacers, &a)
			_ = json.Unmarshal([]byte(wantRacers), &b)
			if fmt.Sprint(a) != fmt.Sprint(b) {
				t.Errorf("racers = %s, want %s", gotRacers, wantRacers)
			}
			if strings.Contains(rr.Body.String(), "@") {
				t.Errorf("status leaked an email: %s", rr.Body.String())
			}
		})
	}
}

func TestRacePageSoloWithoutPacerShowsFirstTimeNoteAndNullStatusPacer(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID := uuid.New()
	viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{
			RoomID: roomID, Label: "2000m", TargetMillimeters: 2000000,
			Mode:   "time_trial",
			Racers: []app.RaceRacer{racer(user.ID, "Me", true)},
			Pacer:  nil,
		}, nil
	}}

	// 1-entrant race: note is present, no data-pacer element
	rr := serveRaceWith(t, newProductionRenderer(t), viewer, user, roomID.String(), racePageEndpoint)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	wantNote := "No personal best yet: set your first time."
	if !strings.Contains(body, wantNote) {
		t.Errorf("page body missing %q; body=%q", wantNote, body)
	}
	if !strings.Contains(body, `id="race-pacer-note"`) {
		t.Errorf("page body missing id=\"race-pacer-note\"; body=%q", body)
	}
	if strings.Contains(body, "data-pacer") {
		t.Errorf("page body unexpectedly contains data-pacer: %q", body)
	}
	if strings.Contains(body, "—") {
		t.Errorf("page body without a pacer unexpectedly contains a pace label '—': %q", body)
	}

	// Status returns JSON with pacer null
	statusRR := serveRacePage(t, viewer, user, roomID.String(), raceStatusEndpoint)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", statusRR.Code)
	}
	var statusData map[string]any
	if err := json.Unmarshal(statusRR.Body.Bytes(), &statusData); err != nil {
		t.Fatalf("unmarshal status JSON: %v", err)
	}
	pacerVal, ok := statusData["pacer"]
	if !ok {
		t.Errorf("pacer key missing from status JSON: %s", statusRR.Body.String())
	}
	if pacerVal != nil {
		t.Errorf("pacer = %v, want null", pacerVal)
	}

	// 1-entrant race in mode 'race': note is absent
	oneRacerRaceViewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{
			RoomID: roomID, Label: "2000m", TargetMillimeters: 2000000,
			Mode:   "race",
			Racers: []app.RaceRacer{racer(user.ID, "Me", true)},
			Pacer:  nil,
		}, nil
	}}
	rrOne := serveRaceWith(t, newProductionRenderer(t), oneRacerRaceViewer, user, roomID.String(), racePageEndpoint)
	if rrOne.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rrOne.Code, rrOne.Body.String())
	}
	if strings.Contains(rrOne.Body.String(), "set your first time") {
		t.Errorf("1-entrant mode=race unexpectedly has note: %q", rrOne.Body.String())
	}
	if strings.Contains(rrOne.Body.String(), `id="race-pacer-note"`) {
		t.Errorf("1-entrant mode=race unexpectedly has id=\"race-pacer-note\": %q", rrOne.Body.String())
	}

	// 1-entrant session in mode 'warmup': note is absent, pacer is null in status
	warmupViewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{
			RoomID:               roomID,
			Label:                "Warmup",
			Mode:                 app.ModeWarmup,
			DurationMilliseconds: 300000,
			Racers:               []app.RaceRacer{racer(user.ID, "Me", true)},
			Pacer:                nil,
		}, nil
	}}
	rrWarmup := serveRaceWith(t, newProductionRenderer(t), warmupViewer, user, roomID.String(), racePageEndpoint)
	if rrWarmup.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rrWarmup.Code, rrWarmup.Body.String())
	}
	if strings.Contains(rrWarmup.Body.String(), "set your first time") {
		t.Errorf("warmup unexpectedly has note: %q", rrWarmup.Body.String())
	}
	if strings.Contains(rrWarmup.Body.String(), `id="race-pacer-note"`) {
		t.Errorf("warmup unexpectedly has id=\"race-pacer-note\": %q", rrWarmup.Body.String())
	}
	if strings.Contains(rrWarmup.Body.String(), "data-pacer") {
		t.Errorf("warmup unexpectedly has data-pacer: %q", rrWarmup.Body.String())
	}
	// CLM-6: the warmup page carries its mode and duration and no track.
	for _, want := range []string{`data-mode="warmup"`, `data-duration-ms="300000"`, `id="warmup-clock"`, `id="warmup-distance"`, `id="warmup-pace"`, `id="warmup-stroke-rate"`} {
		if !strings.Contains(rrWarmup.Body.String(), want) {
			t.Errorf("warmup page missing %s; body=%q", want, rrWarmup.Body.String())
		}
	}
	for _, unwanted := range []string{`id="race-track"`, `id="race-lanes"`, "pixi.min.js", "lanes</span>"} {
		if strings.Contains(rrWarmup.Body.String(), unwanted) {
			t.Errorf("warmup page unexpectedly contains %s; body=%q", unwanted, rrWarmup.Body.String())
		}
	}
	warmupStatusRR := serveRacePage(t, warmupViewer, user, roomID.String(), raceStatusEndpoint)
	if warmupStatusRR.Code != http.StatusOK {
		t.Fatalf("warmup status code = %d, want 200", warmupStatusRR.Code)
	}
	var warmupStatusData map[string]any
	if err := json.Unmarshal(warmupStatusRR.Body.Bytes(), &warmupStatusData); err != nil {
		t.Fatalf("unmarshal warmup status JSON: %v", err)
	}
	if p, ok := warmupStatusData["pacer"]; !ok || p != nil {
		t.Errorf("warmup status pacer = %v, want null (ok=%v)", p, ok)
	}

	// 2-entrant race: note is absent
	rival := uuid.New()
	twoRacerViewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{
			RoomID: roomID, Label: "2000m", TargetMillimeters: 2000000,
			Mode:   "race",
			Racers: []app.RaceRacer{racer(user.ID, "Me", true), racer(rival, "Rival", true)},
			Pacer:  nil,
		}, nil
	}}
	rrTwo := serveRaceWith(t, newProductionRenderer(t), twoRacerViewer, user, roomID.String(), racePageEndpoint)
	if rrTwo.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rrTwo.Code)
	}
	if strings.Contains(rrTwo.Body.String(), "set your first time") {
		t.Errorf("2-entrant race unexpectedly has note: %q", rrTwo.Body.String())
	}
	if strings.Contains(rrTwo.Body.String(), "data-pacer") {
		t.Errorf("2-entrant race unexpectedly has a pacer lane: %q", rrTwo.Body.String())
	}
}

func TestRacePageWithPacerRendersPacerLaneAndStatusPacerJSON(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID := uuid.New()
	viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{
			RoomID: roomID, Label: "2000m", TargetMillimeters: 2000000,
			Racers: []app.RaceRacer{racer(user.ID, "Me", true)},
			Pacer:  &app.RacePacer{ElapsedMilliseconds: 110000},
		}, nil
	}}

	rr := serveRaceWith(t, newProductionRenderer(t), viewer, user, roomID.String(), racePageEndpoint)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	wantPacerLI := `<li data-user-id="pacer" data-pacer="true" data-pacer-ms="110000">0:27.5</li>`
	if !strings.Contains(body, wantPacerLI) {
		t.Errorf("page body missing %q; body=%q", wantPacerLI, body)
	}
	if strings.Contains(body, "Your best") {
		t.Errorf("page body still names the pacer 'Your best'; body=%q", body)
	}
	if !strings.Contains(body, "2 lanes") {
		t.Errorf("page body missing lane-count tag counting 2 lanes; body=%q", body)
	}
	if strings.Contains(body, "set your first time") {
		t.Errorf("page body unexpectedly contains note 'set your first time'; body=%q", body)
	}

	// Status returns JSON with pacer.elapsed_milliseconds == 110000
	statusRR := serveRacePage(t, viewer, user, roomID.String(), raceStatusEndpoint)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", statusRR.Code)
	}
	var statusData struct {
		Pacer *struct {
			ElapsedMilliseconds int64 `json:"elapsed_milliseconds"`
		} `json:"pacer"`
	}
	if err := json.Unmarshal(statusRR.Body.Bytes(), &statusData); err != nil {
		t.Fatalf("unmarshal status JSON: %v", err)
	}
	if statusData.Pacer == nil {
		t.Fatalf("status JSON pacer is nil; body=%s", statusRR.Body.String())
	}
	if statusData.Pacer.ElapsedMilliseconds != 110000 {
		t.Errorf("pacer.elapsed_milliseconds = %d, want 110000", statusData.Pacer.ElapsedMilliseconds)
	}
}

func TestRacePageReadyMarksCallersEquipment(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID := uuid.New()
	var calls []uuid.UUID
	viewer := &stubRoomRaceViewer{readyFn: func(_ context.Context, gotRoom, gotUser uuid.UUID) error {
		calls = append(calls, gotRoom, gotUser)
		return nil
	}}

	rr := serveRacePage(t, viewer, user, roomID.String(), raceReadyEndpoint)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if len(calls) != 2 || calls[0] != roomID || calls[1] != user.ID {
		t.Fatalf("MarkEquipmentReady calls = %v, want (%s, %s)", calls, roomID, user.ID)
	}
}

func TestRacePageServiceErrors(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID := uuid.New()
	for _, tc := range []struct {
		err    error
		status int
	}{
		{app.ErrRoomNotFound, http.StatusNotFound},
		{app.ErrParticipantNotFound, http.StatusNotFound},
		{app.ErrRaceNotStarted, http.StatusConflict},
		{app.ErrInvalidRaceType, http.StatusConflict},
		{app.ErrSessionNotConnected, http.StatusGone},
		{errors.New("private database failure"), http.StatusInternalServerError},
	} {
		for _, endpoint := range []raceEndpoint{racePageEndpoint, raceStatusEndpoint, raceReadyEndpoint, raceResultsEndpoint} {
			if endpoint == racePageEndpoint && errors.Is(tc.err, app.ErrRaceNotStarted) {
				continue // redirects to the lobby; covered above
			}
			if endpoint == racePageEndpoint && errors.Is(tc.err, app.ErrSessionNotConnected) {
				continue // renders the not-connected page; covered below
			}
			t.Run(fmt.Sprintf("%v/%s", tc.err, endpoint), func(t *testing.T) {
				fail := fmt.Errorf("wrapped: %w", tc.err)
				viewer := &stubRoomRaceViewer{
					roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) { return app.RoomRace{}, fail },
					readyFn:    func(context.Context, uuid.UUID, uuid.UUID) error { return fail },
					resultsFn:  func(context.Context, uuid.UUID, uuid.UUID) (app.RaceResults, error) { return app.RaceResults{}, fail },
				}

				rr := serveRacePage(t, viewer, user, roomID.String(), endpoint)

				if rr.Code != tc.status {
					t.Errorf("status = %d, want %d", rr.Code, tc.status)
				}
				if strings.Contains(rr.Body.String(), "private database failure") {
					t.Errorf("response leaked internal details: %q", rr.Body.String())
				}
			})
		}
	}
}

func TestRacePageRejectsInvalidRoomID(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	viewer := &stubRoomRaceViewer{
		roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
			t.Fatal("unexpected service call")
			return app.RoomRace{}, nil
		},
		readyFn: func(context.Context, uuid.UUID, uuid.UUID) error {
			t.Fatal("unexpected service call")
			return nil
		},
		resultsFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RaceResults, error) {
			t.Fatal("unexpected service call")
			return app.RaceResults{}, nil
		},
	}
	for _, endpoint := range []raceEndpoint{racePageEndpoint, raceStatusEndpoint, raceReadyEndpoint, raceResultsEndpoint} {
		rr := serveRacePage(t, viewer, user, "not-a-uuid", endpoint)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", endpoint, rr.Code)
		}
	}
}

// The page a reloading player gets carries the room's finished state, and the
// FINISH! modal markup the race view opens, rendered from the real template.
func TestRacePageRendersFinishModalAndFinishedState(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID, raceID := uuid.New(), uuid.New()
	for _, finished := range []bool{false, true} {
		t.Run(fmt.Sprintf("finished=%v", finished), func(t *testing.T) {
			viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
				return app.RoomRace{RoomID: roomID, RaceID: &raceID, Label: "2000m", TargetMillimeters: 2000000,
					Racers: []app.RaceRacer{racer(user.ID, "Me", true)}, Finished: finished}, nil
			}}

			rr := serveRaceWith(t, newProductionRenderer(t), viewer, user, roomID.String(), racePageEndpoint)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			for _, want := range []string{
				`<dialog id="race-finish-modal" class="modal">`,
				`FINISH!`,
				`<th>Pace / 500m</th>`,
				`data-results-url="/rooms/` + roomID.String() + `/race/results"`,
				`data-dashboard-url="/dashboard"`,
				fmt.Sprintf(`data-finished="%v"`, finished),
			} {
				if !strings.Contains(body, want) {
					t.Errorf("race page missing %q", want)
				}
			}
		})
	}
}

// CLM-7: Rendering a finished warmup carries the finished state and summary modal markup.
func TestRacePageRendersFinishedWarmupSummary(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID, raceID := uuid.New(), uuid.New()
	viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{
			RoomID:               roomID,
			RaceID:               &raceID,
			Label:                "Warmup",
			Mode:                 app.ModeWarmup,
			DurationMilliseconds: 300000,
			Racers:               []app.RaceRacer{racer(user.ID, "Me", true)},
			Finished:             true,
		}, nil
	}}

	rr := serveRaceWith(t, newProductionRenderer(t), viewer, user, roomID.String(), racePageEndpoint)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`<dialog id="race-finish-modal" class="modal">`,
		`FINISH!`,
		`<th>Pace / 500m</th>`,
		`data-mode="warmup"`,
		`data-finished="true"`,
		`data-results-url="/rooms/` + roomID.String() + `/race/results"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("race page missing %q", want)
		}
	}
}

func TestRacePageResultsReturnsResultsJSON(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID, raceID, rival := uuid.New(), uuid.New(), uuid.New()
	rank, elapsed, pace := 1, int64(400000), int64(100000)
	viewer := &stubRoomRaceViewer{resultsFn: func(_ context.Context, gotRoom, gotUser uuid.UUID) (app.RaceResults, error) {
		if gotRoom != roomID || gotUser != user.ID {
			t.Errorf("Results(%s, %s), want (%s, %s)", gotRoom, gotUser, roomID, user.ID)
		}
		return app.RaceResults{RaceID: raceID, Finished: true, TargetMillimeters: 2000000, Results: []app.RaceResult{
			{PublicPlayer: app.PublicPlayer{UserID: user.ID, Name: "Me"}, Rank: &rank, Status: "finished",
				ElapsedMilliseconds: &elapsed, DistanceMillimeters: 2000000, PaceMillisecondsPer500m: &pace},
			{PublicPlayer: app.PublicPlayer{UserID: rival, Name: "Ada"}, Status: "dnf", DistanceMillimeters: 750000},
		}}, nil
	}}

	rr := serveRacePage(t, viewer, user, roomID.String(), raceResultsEndpoint)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	want := `{"race_id":"` + raceID.String() + `","finished":true,"target_millimeters":2000000,"results":[` +
		`{"user_id":"` + user.ID.String() + `","name":"Me","rank":1,"status":"finished","elapsed_milliseconds":400000,"distance_millimeters":2000000,"pace_milliseconds_per_500m":100000},` +
		`{"user_id":"` + rival.String() + `","name":"Ada","rank":null,"status":"dnf","elapsed_milliseconds":null,"distance_millimeters":750000,"pace_milliseconds_per_500m":null}]}`
	var got, wantJSON any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	_ = json.Unmarshal([]byte(want), &wantJSON)
	if fmt.Sprint(got) != fmt.Sprint(wantJSON) {
		t.Errorf("body = %s, want %s", rr.Body.String(), want)
	}
	if strings.Contains(rr.Body.String(), "@") {
		t.Errorf("results leaked an email: %s", rr.Body.String())
	}
}

// A racer dropped before the start has no participant row, so race.mjs would
// never run to tell them; the page itself says they are not in the race,
// keeps the 404 status and leaks nothing about the race.
func TestRacePageTellsADroppedRacerTheyWereRemoved(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID := uuid.New()
	viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{}, fmt.Errorf("room race: %w", app.ErrParticipantNotFound)
	}}

	rr := serveRaceWith(t, newProductionRenderer(t), viewer, user, roomID.String(), racePageEndpoint)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"removed from it before the start", `href="/dashboard"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q; body=%q", want, body)
		}
	}
	if strings.Contains(body, "race-finish-modal") || strings.Contains(body, user.Email) {
		t.Errorf("removed racer's page shows the race or an email: %q", body)
	}
}

// A time trial or warmup whose rowing machine never connected was dissolved
// by Begin (ErrNoRacersReady). Its host's race page says so and links back to
// the dashboard, with 200, instead of a track whose countdown never moves, a
// redirect to a lobby that is gone, or a bare error.
func TestRacePageSoloSessionNotConnectedShowsMessageAndDashboardLink(t *testing.T) {
	user := db.User{ID: uuid.New(), Email: "self@example.com", EmailVerified: true}
	roomID := uuid.New()
	viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
		return app.RoomRace{}, fmt.Errorf("room race: %w", app.ErrSessionNotConnected)
	}}

	rr := serveRaceWith(t, newProductionRenderer(t), viewer, user, roomID.String(), racePageEndpoint)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; Location=%q", rr.Code, rr.Header().Get("Location"))
	}
	body := rr.Body.String()
	for _, want := range []string{
		"Rowing machine not connected",
		"Your rowing machine wasn&#39;t connected, so the session didn&#39;t start.",
		`href="/dashboard"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q; body=%q", want, body)
		}
	}
	for _, absent := range []string{`id="race-track"`, "race-finish-modal", "Race unavailable"} {
		if strings.Contains(body, absent) {
			t.Errorf("not-connected page shows %q", absent)
		}
	}

	// race.mjs, already on the page when the room dissolves, polls status: a
	// 410 sends it back to this page.
	status := serveRacePage(t, viewer, user, roomID.String(), raceStatusEndpoint)
	if status.Code != http.StatusGone {
		t.Fatalf("status endpoint = %d, want 410", status.Code)
	}
}
