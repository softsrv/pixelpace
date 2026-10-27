package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
)

type stubRoomRaceViewer struct {
	roomRaceFn func(ctx context.Context, roomID, userID uuid.UUID) (app.RoomRace, error)
	readyFn    func(ctx context.Context, roomID, userID uuid.UUID) error
}

func (s *stubRoomRaceViewer) RoomRace(ctx context.Context, roomID, userID uuid.UUID) (app.RoomRace, error) {
	return s.roomRaceFn(ctx, roomID, userID)
}

func (s *stubRoomRaceViewer) MarkEquipmentReady(ctx context.Context, roomID, userID uuid.UUID) error {
	return s.readyFn(ctx, roomID, userID)
}

type raceEndpoint int

const (
	racePageEndpoint raceEndpoint = iota
	raceStatusEndpoint
	raceReadyEndpoint
)

func (e raceEndpoint) String() string {
	return [...]string{"page", "status", "ready"}[e]
}

func serveRacePage(t *testing.T, viewer *stubRoomRaceViewer, user db.User, pathID string, endpoint raceEndpoint) *httptest.ResponseRecorder {
	t.Helper()
	h := handlers.NewRacePageHandler(viewer, newTestRenderer(t), false)
	method, path, fn := http.MethodGet, "/rooms/"+pathID+"/race", h.Page
	switch endpoint {
	case raceStatusEndpoint:
		path, fn = path+"/status", h.Status
	case raceReadyEndpoint:
		method, path, fn = http.MethodPost, path+"/ready", h.Ready
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
		want     string
	}{
		{"connecting", nil, nil, `{"race_starts_at":null,"race_id":null}`},
		{"counting down", &startsAt, nil, `{"race_starts_at":"2026-10-26T12:00:10Z","race_id":null}`},
		{"racing", &startsAt, &raceID, `{"race_starts_at":"2026-10-26T12:00:10Z","race_id":"` + raceID.String() + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viewer := &stubRoomRaceViewer{roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) {
				return app.RoomRace{RoomID: roomID, RaceID: tc.raceID, RaceStartsAt: tc.startsAt, Racers: racers}, nil
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
		{errors.New("private database failure"), http.StatusInternalServerError},
	} {
		for _, endpoint := range []raceEndpoint{racePageEndpoint, raceStatusEndpoint, raceReadyEndpoint} {
			if endpoint == racePageEndpoint && errors.Is(tc.err, app.ErrRaceNotStarted) {
				continue // redirects to the lobby; covered above
			}
			t.Run(fmt.Sprintf("%v/%s", tc.err, endpoint), func(t *testing.T) {
				fail := fmt.Errorf("wrapped: %w", tc.err)
				viewer := &stubRoomRaceViewer{
					roomRaceFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRace, error) { return app.RoomRace{}, fail },
					readyFn:    func(context.Context, uuid.UUID, uuid.UUID) error { return fail },
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
	}
	for _, endpoint := range []raceEndpoint{racePageEndpoint, raceStatusEndpoint, raceReadyEndpoint} {
		rr := serveRacePage(t, viewer, user, "not-a-uuid", endpoint)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", endpoint, rr.Code)
		}
	}
}
