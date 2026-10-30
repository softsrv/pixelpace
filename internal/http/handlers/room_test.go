package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
)

// Each function field lets a test control the corresponding service operation.
type stubRoomService struct {
	createFn func(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, string, error)
	joinFn   func(context.Context, uuid.UUID, string) (uuid.UUID, error)
	leaveFn  func(context.Context, uuid.UUID, uuid.UUID) error
	kickFn   func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	readyFn  func(context.Context, uuid.UUID, uuid.UUID) error
	startFn  func(context.Context, uuid.UUID, uuid.UUID) error
	rosterFn func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRoster, error)
	resetFn  func(context.Context, uuid.UUID) error
}

func (s *stubRoomService) ResetCountdown(ctx context.Context, roomID uuid.UUID) error {
	return s.resetFn(ctx, roomID)
}

// fakeBeginner records each race begin and returns err.
type fakeBeginner struct {
	mu         sync.Mutex
	rooms      []uuid.UUID
	ctxs       []context.Context
	err        error
	block      bool
	started    chan struct{}
	raceID     uuid.UUID
	runs       []uuid.UUID
	runCtxs    []context.Context
	runErr     error
	runBlock   bool
	runStarted chan struct{}
}

func (b *fakeBeginner) Begin(ctx context.Context, roomID uuid.UUID) (db.Race, error) {
	b.mu.Lock()
	b.rooms = append(b.rooms, roomID)
	b.ctxs = append(b.ctxs, ctx)
	block := b.block
	started := b.started
	err := b.err
	raceID := b.raceID
	b.mu.Unlock()
	if started != nil {
		close(started)
	}
	if block {
		<-ctx.Done()
		return db.Race{}, ctx.Err()
	}
	if err != nil {
		return db.Race{}, err
	}
	if raceID != uuid.Nil {
		return db.Race{ID: raceID, RoomID: roomID}, nil
	}
	return db.Race{ID: uuid.New(), RoomID: roomID}, nil
}

func (b *fakeBeginner) Run(ctx context.Context, raceID uuid.UUID) error {
	b.mu.Lock()
	b.runs = append(b.runs, raceID)
	b.runCtxs = append(b.runCtxs, ctx)
	block := b.runBlock
	started := b.runStarted
	err := b.runErr
	b.mu.Unlock()
	if started != nil {
		close(started)
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (s *stubRoomService) CreateRoom(ctx context.Context, userID, raceTypeID uuid.UUID) (uuid.UUID, string, error) {
	return s.createFn(ctx, userID, raceTypeID)
}

func (s *stubRoomService) JoinRoom(ctx context.Context, userID uuid.UUID, code string) (uuid.UUID, error) {
	return s.joinFn(ctx, userID, code)
}

func (s *stubRoomService) Roster(ctx context.Context, userID, roomID uuid.UUID) (app.RoomRoster, error) {
	return s.rosterFn(ctx, userID, roomID)
}

func (s *stubRoomService) LeaveRoom(ctx context.Context, userID, roomID uuid.UUID) error {
	return s.leaveFn(ctx, userID, roomID)
}

func (s *stubRoomService) KickParticipant(ctx context.Context, userID, roomID, targetID uuid.UUID) error {
	return s.kickFn(ctx, userID, roomID, targetID)
}

func (s *stubRoomService) MarkReady(ctx context.Context, userID, roomID uuid.UUID) error {
	return s.readyFn(ctx, userID, roomID)
}

func (s *stubRoomService) StartRace(ctx context.Context, userID, roomID uuid.UUID) error {
	return s.startFn(ctx, userID, roomID)
}

// stubRoomID is the room id the stub reports for create and join.
var stubRoomID = uuid.MustParse("01950000-0000-7000-8000-0000000000aa")

type roomCall struct {
	operation string
	userID    uuid.UUID
	roomID    uuid.UUID
	raceID    uuid.UUID
	targetID  uuid.UUID
	code      string
}

func recordingRoomService(record func(roomCall) error) *stubRoomService {
	return &stubRoomService{
		createFn: func(_ context.Context, userID, raceID uuid.UUID) (uuid.UUID, string, error) {
			return stubRoomID, "ABC234", record(roomCall{operation: "create", userID: userID, raceID: raceID})
		},
		joinFn: func(_ context.Context, userID uuid.UUID, code string) (uuid.UUID, error) {
			return stubRoomID, record(roomCall{operation: "join", userID: userID, code: code})
		},
		leaveFn: func(_ context.Context, userID, roomID uuid.UUID) error {
			return record(roomCall{operation: "leave", userID: userID, roomID: roomID})
		},
		kickFn: func(_ context.Context, userID, roomID, targetID uuid.UUID) error {
			return record(roomCall{operation: "kick", userID: userID, roomID: roomID, targetID: targetID})
		},
		readyFn: func(_ context.Context, userID, roomID uuid.UUID) error {
			return record(roomCall{operation: "ready", userID: userID, roomID: roomID})
		},
		startFn: func(_ context.Context, userID, roomID uuid.UUID) error {
			return record(roomCall{operation: "start", userID: userID, roomID: roomID})
		},
	}
}

type roomOperation struct {
	name    string
	handler func(*handlers.RoomHandler, http.ResponseWriter, *http.Request)
	path    string
	form    url.Values
	want    roomCall
	status  int
}

func roomOperations() []roomOperation {
	roomID, raceID, targetID := uuid.New(), uuid.New(), uuid.New()
	return []roomOperation{
		{"create", (*handlers.RoomHandler).Create, "/rooms", url.Values{"race_type_id": {raceID.String()}}, roomCall{operation: "create", raceID: raceID}, http.StatusCreated},
		{"join", (*handlers.RoomHandler).Join, "/rooms/join", url.Values{"join_code": {" ABC234 "}}, roomCall{operation: "join", code: "ABC234"}, http.StatusOK},
		{"leave", (*handlers.RoomHandler).Leave, "/rooms/" + roomID.String() + "/leave", url.Values{}, roomCall{operation: "leave", roomID: roomID}, http.StatusNoContent},
		{"kick", (*handlers.RoomHandler).Kick, "/rooms/" + roomID.String() + "/kick", url.Values{"target_user_id": {targetID.String()}}, roomCall{operation: "kick", roomID: roomID, targetID: targetID}, http.StatusNoContent},
		{"ready", (*handlers.RoomHandler).MarkReady, "/rooms/" + roomID.String() + "/ready", url.Values{}, roomCall{operation: "ready", roomID: roomID}, http.StatusNoContent},
		{"start", (*handlers.RoomHandler).Start, "/rooms/" + roomID.String() + "/start", url.Values{}, roomCall{operation: "start", roomID: roomID}, http.StatusNoContent},
	}
}

func serveRoomRequest(t *testing.T, op roomOperation, svc *stubRoomService, userID uuid.UUID, body, pathID string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	h := handlers.NewRoomHandler(context.Background(), svc, &fakeBeginner{}, nil, false)
	defer h.Wait()
	return serveRoomRequestWith(t, h, op, userID, body, pathID, authenticated)
}

func serveRoomRequestWith(t *testing.T, h *handlers.RoomHandler, op roomOperation, userID uuid.UUID, body, pathID string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var endpoint http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { op.handler(h, w, r) })
	req := httptest.NewRequest(http.MethodPost, op.path+"?user_id="+uuid.NewString(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", pathID)
	if authenticated {
		user := db.User{ID: userID, Email: "room@example.com", EmailVerified: true}
		token, err := auth.IssueAccessToken(userID, user.Email, testJWTSecret, 15*time.Minute)
		if err != nil {
			t.Fatalf("issue token: %v", err)
		}
		req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
		endpoint = middleware.Authenticate(&handlerUserFetcher{user: user}, testJWTSecret)(endpoint)
	}
	rr := httptest.NewRecorder()
	endpoint.ServeHTTP(rr, req)
	return rr
}

func TestRoomHandlersSuccessAndAuthenticatedIdentity(t *testing.T) {
	for _, op := range roomOperations() {
		t.Run(op.name, func(t *testing.T) {
			userID := uuid.New()
			want := op.want
			want.userID = userID
			calls := 0
			svc := recordingRoomService(func(got roomCall) error {
				calls++
				if got != want {
					t.Errorf("service call = %+v, want %+v", got, want)
				}
				return nil
			})
			// Client-supplied acting identities must never override the context user.
			op.form.Set("user_id", uuid.NewString())
			op.form.Set("host_user_id", uuid.NewString())
			op.form.Set("caller_user_id", uuid.NewString())
			rr := serveRoomRequest(t, op, svc, userID, op.form.Encode(), op.want.roomID.String(), true)
			if calls != 1 {
				t.Fatalf("service calls = %d, want 1", calls)
			}
			if rr.Code != op.status {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, op.status, rr.Body.String())
			}
			if op.name == "create" || op.name == "join" {
				if got := rr.Header().Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
				var body struct {
					RoomID   string `json:"room_id"`
					JoinCode string `json:"join_code"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode %s response: %v", op.name, err)
				}
				if body.JoinCode != "ABC234" || body.RoomID != stubRoomID.String() {
					t.Errorf("%s response = %+v, want room %s and join code ABC234", op.name, body, stubRoomID)
				}
			} else if rr.Body.Len() != 0 {
				t.Errorf("204 response body = %q, want empty", rr.Body.String())
			}
		})
	}
}

func TestRoomHandlersServiceErrors(t *testing.T) {
	errorsToTest := []struct {
		err    error
		status int
	}{
		{app.ErrRoomNotFound, http.StatusNotFound},
		{app.ErrParticipantNotFound, http.StatusNotFound},
		{app.ErrNotHost, http.StatusForbidden},
		{app.ErrRoomFull, http.StatusConflict},
		{app.ErrAlreadyParticipant, http.StatusConflict},
		{app.ErrNotAllReady, http.StatusConflict},
		{app.ErrInvalidStatusTransition, http.StatusConflict},
		{app.ErrRoomNotJoinable, http.StatusConflict},
		{app.ErrParticipantReady, http.StatusConflict},
		{app.ErrNotEnoughParticipants, http.StatusConflict},
		{app.ErrInvalidRaceType, http.StatusConflict},
		{errors.New("private database failure"), http.StatusInternalServerError},
	}
	for _, op := range roomOperations() {
		for _, tc := range errorsToTest {
			t.Run(op.name+"/"+tc.err.Error(), func(t *testing.T) {
				calls := 0
				svc := recordingRoomService(func(got roomCall) error {
					calls++
					if got.operation != op.name {
						t.Errorf("operation = %s, want %s", got.operation, op.name)
					}
					return fmt.Errorf("wrapped: %w", tc.err)
				})
				rr := serveRoomRequest(t, op, svc, uuid.New(), op.form.Encode(), op.want.roomID.String(), true)
				if calls != 1 {
					t.Fatalf("service calls = %d, want 1", calls)
				}
				if rr.Code != tc.status {
					t.Errorf("status = %d, want %d", rr.Code, tc.status)
				}
				if strings.Contains(rr.Body.String(), "private database failure") || strings.Contains(rr.Body.String(), "ABC234") {
					t.Errorf("error response leaked internal details or success data: %q", rr.Body.String())
				}
			})
		}
	}
}

func TestRoomHandlersRejectInvalidRequests(t *testing.T) {
	for _, op := range roomOperations() {
		t.Run(op.name, func(t *testing.T) {
			svc := recordingRoomService(func(got roomCall) error {
				t.Fatalf("unexpected service call: %+v", got)
				return nil
			})
			rr := serveRoomRequest(t, op, svc, uuid.New(), op.form.Encode(), op.want.roomID.String(), false)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("missing context status = %d, want 401", rr.Code)
			}
			if op.want.roomID != uuid.Nil {
				for _, pathID := range []string{"", "not-a-uuid"} {
					rr = serveRoomRequest(t, op, svc, uuid.New(), op.form.Encode(), pathID, true)
					if rr.Code != http.StatusBadRequest {
						t.Errorf("invalid room ID %q status = %d, want 400", pathID, rr.Code)
					}
				}
			}
			if op.name == "create" || op.name == "join" || op.name == "kick" {
				for _, body := range []string{"", "%zz", "race_type_id=invalid&target_user_id=invalid&join_code=+++"} {
					rr = serveRoomRequest(t, op, svc, uuid.New(), body, op.want.roomID.String(), true)
					if rr.Code != http.StatusBadRequest {
						t.Errorf("invalid form %q status = %d, want 400", body, rr.Code)
					}
				}
			}
		})
	}
}

func serveRosterRequest(t *testing.T, svc *stubRoomService, userID uuid.UUID, pathID string) *httptest.ResponseRecorder {
	t.Helper()
	h := handlers.NewRoomHandler(context.Background(), svc, &fakeBeginner{}, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/rooms/"+pathID, nil)
	req.SetPathValue("id", pathID)
	user := db.User{ID: userID, Email: "room@example.com", EmailVerified: true}
	token, err := auth.IssueAccessToken(userID, user.Email, testJWTSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
	rr := httptest.NewRecorder()
	middleware.Authenticate(&handlerUserFetcher{user: user}, testJWTSecret)(http.HandlerFunc(h.Roster)).ServeHTTP(rr, req)
	return rr
}

func TestRoomRosterMemberGetsParticipantsAndRace(t *testing.T) {
	member, other, roomID, raceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name   string
		raceID *uuid.UUID
		want   string
	}{
		{"before race", nil, "null"},
		{"race begun", &raceID, `"` + raceID.String() + `"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubRoomService{rosterFn: func(_ context.Context, caller, room uuid.UUID) (app.RoomRoster, error) {
				if caller != member || room != roomID {
					t.Errorf("roster call = %s/%s, want %s/%s", caller, room, member, roomID)
				}
				return app.RoomRoster{Participants: []app.RoomRosterEntry{
					{PublicPlayer: app.PublicPlayer{UserID: member, Name: "Rower"}, Ready: true, Host: true},
					{PublicPlayer: app.NewPublicPlayer(other, pgtype.Text{}), Ready: false, Host: false},
				}, RaceID: tc.raceID}, nil
			}}
			rr := serveRosterRequest(t, svc, member, roomID.String())
			if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status = %d, type %q; body=%q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
			}
			var body struct {
				Participants []map[string]any `json:"participants"`
				RaceID       json.RawMessage  `json:"race_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if string(body.RaceID) != tc.want {
				t.Errorf("race_id = %s, want %s", body.RaceID, tc.want)
			}
			if len(body.Participants) != 2 {
				t.Fatalf("participants = %v", body.Participants)
			}
			for i, want := range []map[string]any{
				{"user_id": member.String(), "name": "Rower", "ready": true, "host": true},
				{"user_id": other.String(), "name": app.NewPublicPlayer(other, pgtype.Text{}).Name, "ready": false, "host": false},
			} {
				got := body.Participants[i]
				if len(got) != len(want) {
					t.Errorf("participant %d keys = %v, want exactly %v", i, got, want)
				}
				for k, v := range want {
					if got[k] != v {
						t.Errorf("participant %d %s = %v, want %v", i, k, got[k], v)
					}
				}
			}
			// Each roster user_id is the id the kick route accepts as target_user_id.
			kicked := uuid.Nil
			kickSvc := recordingRoomService(func(c roomCall) error { kicked = c.targetID; return nil })
			kick := roomOperations()[3]
			kick.form = url.Values{"target_user_id": {body.Participants[1]["user_id"].(string)}}
			if rr := serveRoomRequest(t, kick, kickSvc, member, kick.form.Encode(), roomID.String(), true); rr.Code != http.StatusNoContent || kicked != other {
				t.Fatalf("kick by roster id: status %d, target %s, want %s", rr.Code, kicked, other)
			}
		})
	}
}

func TestRoomRosterNonMemberRefused(t *testing.T) {
	for _, err := range []error{app.ErrParticipantNotFound, app.ErrRoomNotFound} {
		t.Run(err.Error(), func(t *testing.T) {
			svc := &stubRoomService{rosterFn: func(context.Context, uuid.UUID, uuid.UUID) (app.RoomRoster, error) {
				return app.RoomRoster{}, fmt.Errorf("wrapped: %w", err)
			}}
			rr := serveRosterRequest(t, svc, uuid.New(), uuid.New().String())
			if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "participants") {
				t.Fatalf("non-member: status %d, body %q", rr.Code, rr.Body.String())
			}
		})
	}
	rr := serveRosterRequest(t, &stubRoomService{}, uuid.New(), "not-a-uuid")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid room id status = %d", rr.Code)
	}
}

// startOperation is the POST /rooms/{id}/start operation for roomID.
func startOperation(roomID uuid.UUID) roomOperation {
	op := roomOperations()[5]
	op.path = "/rooms/" + roomID.String() + "/start"
	op.want.roomID = roomID
	return op
}

// A successful start begins the room's race once, outside the request, on a
// context the finished request does not cancel.
func TestRoomStartBeginsRaceInBackground(t *testing.T) {
	roomID := uuid.New()
	beginner := &fakeBeginner{}
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(context.Context, uuid.UUID) error {
		t.Error("reset after a successful begin")
		return nil
	}
	h := handlers.NewRoomHandler(context.Background(), svc, beginner, nil, false)
	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	h.Wait()
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if len(beginner.rooms) != 1 || beginner.rooms[0] != roomID {
		t.Fatalf("begins = %v, want one for %s", beginner.rooms, roomID)
	}
	// httptest's request context ends with the handler; the begin context must not.
	if err := beginner.ctxs[0].Err(); err != nil {
		t.Fatalf("begin context done after the request: %v", err)
	}
	if len(beginner.runs) != 1 {
		t.Fatalf("runs = %v, want 1 run", beginner.runs)
	}
	if err := beginner.runCtxs[0].Err(); err != nil {
		t.Fatalf("run context done after the request: %v", err)
	}
}

// A refused start (not host, wrong state, not ready) begins no race.
func TestRoomStartRefusedBeginsNoRace(t *testing.T) {
	for _, refusal := range []error{app.ErrNotHost, app.ErrInvalidStatusTransition, app.ErrNotAllReady, errors.New("db down")} {
		t.Run(refusal.Error(), func(t *testing.T) {
			roomID := uuid.New()
			beginner := &fakeBeginner{}
			svc := recordingRoomService(func(roomCall) error { return refusal })
			h := handlers.NewRoomHandler(context.Background(), svc, beginner, nil, false)
			rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
			h.Wait()
			if rr.Code == http.StatusNoContent {
				t.Fatalf("refused start answered 204")
			}
			if len(beginner.rooms) != 0 {
				t.Fatalf("refused start began races: %v", beginner.rooms)
			}
		})
	}
}

type capturedLog struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *capturedLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturedLog) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}
func (c *capturedLog) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capturedLog) WithGroup(string) slog.Handler      { return c }

// A failed begin is logged and the room is returned to waiting.
func TestRoomStartFailedBeginLogsAndResets(t *testing.T) {
	logs := &capturedLog{}
	previous := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(previous) })

	roomID := uuid.New()
	beginErr := errors.New("countdown cancelled")
	beginner := &fakeBeginner{err: beginErr}
	var resets []uuid.UUID
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(ctx context.Context, id uuid.UUID) error {
		if ctx.Err() != nil {
			t.Errorf("reset context already done: %v", ctx.Err())
		}
		resets = append(resets, id)
		return nil
	}
	h := handlers.NewRoomHandler(context.Background(), svc, beginner, nil, false)
	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	h.Wait()
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if len(resets) != 1 || resets[0] != roomID {
		t.Fatalf("resets = %v, want one for %s", resets, roomID)
	}
	if len(beginner.runs) != 0 {
		t.Fatalf("failed begin ran races: %v", beginner.runs)
	}
	logged := false
	for _, r := range logs.records {
		r.Attrs(func(a slog.Attr) bool {
			if r.Level == slog.LevelError && a.Key == "error" && strings.Contains(fmt.Sprint(a.Value.Any()), beginErr.Error()) {
				logged = true
			}
			return true
		})
	}
	if !logged {
		t.Fatalf("begin error not logged at error level: %+v", logs.records)
	}
}

// A lifetime cancellation mid-countdown cancels Begin and resets the room with
// a context that is not cancelled.
func TestRoomStartLifetimeCancelResetsRoom(t *testing.T) {
	roomID := uuid.New()
	beginStarted := make(chan struct{})
	beginner := &fakeBeginner{
		block:   true,
		started: beginStarted,
	}
	var resetCtxErr error
	var resets []uuid.UUID
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(ctx context.Context, id uuid.UUID) error {
		resetCtxErr = ctx.Err()
		resets = append(resets, id)
		return nil
	}

	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	h := handlers.NewRoomHandler(lifetimeCtx, svc, beginner, nil, false)

	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}

	// Wait until Begin is actively running in background.
	<-beginStarted

	// Cancel the handler's lifetime context mid-countdown.
	cancelLifetime()

	// Wait must return once the begin is aborted and the room is reset.
	waitDone := make(chan struct{})
	go func() {
		h.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("h.Wait() timed out after lifetime cancel")
	}

	if len(resets) != 1 || resets[0] != roomID {
		t.Fatalf("resets = %v, want one for %s", resets, roomID)
	}
	if resetCtxErr != nil {
		t.Fatalf("resetCtx was already cancelled when ResetCountdown was called: %v", resetCtxErr)
	}
}

// Successful room actions tell HTMX pages where to go next: create and join
// open the lobby, leaving returns to the dashboard, starting opens the race,
// and other lobby changes refresh the open lobby.
func TestRoomHandlersHTMXNavigation(t *testing.T) {
	for _, op := range roomOperations() {
		t.Run(op.name, func(t *testing.T) {
			roomID := op.want.roomID
			want := map[string][2]string{
				"create": {"HX-Redirect", "/rooms/" + stubRoomID.String() + "/lobby"},
				"join":   {"HX-Redirect", "/rooms/" + stubRoomID.String() + "/lobby"},
				"leave":  {"HX-Redirect", "/dashboard"},
				"kick":   {"HX-Trigger", "room-updated"},
				"ready":  {"HX-Trigger", "room-updated"},
				"start":  {"HX-Redirect", "/rooms/" + roomID.String() + "/race"},
			}[op.name]
			svc := recordingRoomService(func(roomCall) error { return nil })
			rr := serveRoomRequest(t, op, svc, uuid.New(), op.form.Encode(), roomID.String(), true)
			if rr.Code != op.status {
				t.Fatalf("status = %d, want %d", rr.Code, op.status)
			}
			if got := rr.Header().Get(want[0]); got != want[1] {
				t.Errorf("%s = %q, want %q", want[0], got, want[1])
			}
		})
	}
}

// Rejoining a room already joined still leads back to its lobby.
func TestRoomJoinAlreadyParticipantRedirectsToLobby(t *testing.T) {
	op := roomOperations()[1]
	svc := recordingRoomService(func(roomCall) error { return app.ErrAlreadyParticipant })
	rr := serveRoomRequest(t, op, svc, uuid.New(), op.form.Encode(), "", true)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if got, want := rr.Header().Get("HX-Redirect"), "/rooms/"+stubRoomID.String()+"/lobby"; got != want {
		t.Errorf("HX-Redirect = %q, want %q", got, want)
	}
}

// A refused start leaves the host in the lobby.
func TestRoomStartRefusedDoesNotRedirect(t *testing.T) {
	roomID := uuid.New()
	svc := recordingRoomService(func(roomCall) error { return app.ErrNotAllReady })
	rr := serveRoomRequest(t, startOperation(roomID), svc, uuid.New(), "", roomID.String(), true)
	if rr.Code != http.StatusConflict || rr.Header().Get("HX-Redirect") != "" {
		t.Fatalf("status = %d, HX-Redirect = %q; want 409 and no redirect", rr.Code, rr.Header().Get("HX-Redirect"))
	}
}

// A begin that found no racer ready has already dissolved the room, so the
// handler does not try to return it to waiting.
func TestRoomStartNoRacersReadyDoesNotReset(t *testing.T) {
	roomID := uuid.New()
	beginner := &fakeBeginner{err: fmt.Errorf("schedule: %w", app.ErrNoRacersReady)}
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(context.Context, uuid.UUID) error {
		t.Error("reset after no racer was ready")
		return nil
	}
	h := handlers.NewRoomHandler(context.Background(), svc, beginner, nil, false)
	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	h.Wait()
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if len(beginner.runs) != 0 {
		t.Fatalf("no racers ready ran races: %v", beginner.runs)
	}
}

// After a successful Begin, the room handler runs Run with the begun race's ID
// in the same background goroutine. ResetCountdown is never called.
func TestRoomStartRunsRaceAfterBegin(t *testing.T) {
	roomID := uuid.New()
	begunRaceID := uuid.New()
	beginner := &fakeBeginner{raceID: begunRaceID}
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(context.Context, uuid.UUID) error {
		t.Fatal("ResetCountdown called after successful Begin")
		return nil
	}
	h := handlers.NewRoomHandler(context.Background(), svc, beginner, nil, false)
	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	h.Wait()
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if len(beginner.runs) != 1 {
		t.Fatalf("runs = %v, want exactly 1 run", beginner.runs)
	}
	if beginner.runs[0] != begunRaceID {
		t.Fatalf("run raceID = %s, want %s", beginner.runs[0], begunRaceID)
	}
	if len(beginner.rooms) != 1 || beginner.rooms[0] != roomID {
		t.Fatalf("rooms = %v, want [%s]", beginner.rooms, roomID)
	}
	beginCtx := beginner.ctxs[0]
	if beginner.runCtxs[0] != beginCtx {
		t.Fatalf("runCtx = %v, want same context as beginCtx = %v", beginner.runCtxs[0], beginCtx)
	}
}

// A lifetime cancellation mid-run unblocks h.Wait() and never calls ResetCountdown.
func TestRoomStartLifetimeCancelDuringRunWaitsAndReturns(t *testing.T) {
	roomID := uuid.New()
	runStarted := make(chan struct{})
	beginner := &fakeBeginner{
		runBlock:   true,
		runStarted: runStarted,
	}
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(context.Context, uuid.UUID) error {
		t.Fatal("ResetCountdown called after successful Begin")
		return nil
	}

	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	h := handlers.NewRoomHandler(lifetimeCtx, svc, beginner, nil, false)

	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}

	// Wait until Run is actively running in background.
	<-runStarted

	// Cancel the handler's lifetime context while Run is active.
	cancelLifetime()

	// Wait must return once the background goroutine finishes.
	waitDone := make(chan struct{})
	go func() {
		h.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("h.Wait() timed out after lifetime cancel during Run")
	}

	if len(beginner.runs) != 1 {
		t.Fatalf("runs = %v, want 1 run", beginner.runs)
	}
}

// A non-cancel error from Run is logged at Error level, and ResetCountdown is not called.
func TestRoomStartRunErrorLogsAndDoesNotReset(t *testing.T) {
	logs := &capturedLog{}
	previous := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(previous) })

	roomID := uuid.New()
	runErr := errors.New("telemetry stream disconnected")
	beginner := &fakeBeginner{runErr: runErr}
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(context.Context, uuid.UUID) error {
		t.Fatal("ResetCountdown called after Run error")
		return nil
	}

	h := handlers.NewRoomHandler(context.Background(), svc, beginner, nil, false)
	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	h.Wait()
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if len(beginner.runs) != 1 {
		t.Fatalf("runs = %v, want 1 run", beginner.runs)
	}

	logged := false
	for _, r := range logs.records {
		if r.Level == slog.LevelError && r.Message == "race run failed" {
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "error" && strings.Contains(fmt.Sprint(a.Value.Any()), runErr.Error()) {
					logged = true
				}
				return true
			})
		}
	}
	if !logged {
		t.Fatalf("run error not logged at error level: %+v", logs.records)
	}
}

// A context.Canceled error from Run is not logged as an error.
func TestRoomStartRunCancelErrorDoesNotLog(t *testing.T) {
	logs := &capturedLog{}
	previous := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(previous) })

	roomID := uuid.New()
	beginner := &fakeBeginner{runErr: context.Canceled}
	svc := recordingRoomService(func(roomCall) error { return nil })
	svc.resetFn = func(context.Context, uuid.UUID) error {
		t.Fatal("ResetCountdown called after Run cancellation")
		return nil
	}

	h := handlers.NewRoomHandler(context.Background(), svc, beginner, nil, false)
	rr := serveRoomRequestWith(t, h, startOperation(roomID), uuid.New(), "", roomID.String(), true)
	h.Wait()
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}

	for _, r := range logs.records {
		if r.Level == slog.LevelError && r.Message == "race run failed" {
			t.Fatalf("cancellation unexpectedly logged as error: %+v", r)
		}
	}
}

// resumingBeginner lists races left unfinished by an earlier server run.
type resumingBeginner struct {
	fakeBeginner
	unfinished []uuid.UUID
	listErr    error
}

func (b *resumingBeginner) UnfinishedRaces(context.Context) ([]uuid.UUID, error) {
	return b.unfinished, b.listErr
}

// At startup every race left in progress is watched again, on the handler's
// lifetime, so it still finalizes; nothing is begun again.
func TestRoomResumeRacesRunsEveryUnfinishedRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, second := uuid.New(), uuid.New()
	beginner := &resumingBeginner{unfinished: []uuid.UUID{first, second}}
	h := handlers.NewRoomHandler(ctx, recordingRoomService(func(roomCall) error { return nil }), beginner, nil, false)

	if err := h.ResumeRaces(); err != nil {
		t.Fatal(err)
	}
	h.Wait()

	got := map[uuid.UUID]bool{}
	for _, id := range beginner.runs {
		got[id] = true
	}
	if len(beginner.runs) != 2 || !got[first] || !got[second] {
		t.Fatalf("runs = %v, want %s and %s", beginner.runs, first, second)
	}
	if len(beginner.rooms) != 0 {
		t.Fatalf("begins = %v, want none", beginner.rooms)
	}
}

// Resumed runs end with the handler's lifetime, so Wait returns on shutdown.
func TestRoomResumeRacesStopsWithTheHandlerLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	beginner := &resumingBeginner{unfinished: []uuid.UUID{uuid.New()}}
	beginner.runBlock = true
	beginner.runStarted = started
	h := handlers.NewRoomHandler(ctx, recordingRoomService(func(roomCall) error { return nil }), beginner, nil, false)

	if err := h.ResumeRaces(); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	h.Wait()
}

func TestRoomResumeRacesReportsAListError(t *testing.T) {
	listErr := errors.New("db down")
	beginner := &resumingBeginner{listErr: listErr}
	h := handlers.NewRoomHandler(context.Background(), recordingRoomService(func(roomCall) error { return nil }), beginner, nil, false)

	if err := h.ResumeRaces(); !errors.Is(err, listErr) {
		t.Fatalf("err = %v, want %v", err, listErr)
	}
	h.Wait()
	if len(beginner.runs) != 0 {
		t.Fatalf("runs = %v, want none", beginner.runs)
	}
}
