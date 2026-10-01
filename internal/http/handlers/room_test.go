package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
	"github.com/softsrv/starter/internal/http/middleware"
)

// Each function field lets a test control the corresponding service operation.
type stubRoomService struct {
	createFn func(context.Context, uuid.UUID, uuid.UUID) (string, error)
	joinFn   func(context.Context, uuid.UUID, string) error
	leaveFn  func(context.Context, uuid.UUID, uuid.UUID) error
	kickFn   func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	readyFn  func(context.Context, uuid.UUID, uuid.UUID) error
	startFn  func(context.Context, uuid.UUID, uuid.UUID) error
}

func (s *stubRoomService) CreateRoom(ctx context.Context, userID, raceTypeID uuid.UUID) (string, error) {
	return s.createFn(ctx, userID, raceTypeID)
}

func (s *stubRoomService) JoinRoom(ctx context.Context, userID uuid.UUID, code string) error {
	return s.joinFn(ctx, userID, code)
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
		createFn: func(_ context.Context, userID, raceID uuid.UUID) (string, error) {
			return "ABC234", record(roomCall{operation: "create", userID: userID, raceID: raceID})
		},
		joinFn: func(_ context.Context, userID uuid.UUID, code string) error {
			return record(roomCall{operation: "join", userID: userID, code: code})
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
		{"join", (*handlers.RoomHandler).Join, "/rooms/join", url.Values{"join_code": {" ABC234 "}}, roomCall{operation: "join", code: "ABC234"}, http.StatusNoContent},
		{"leave", (*handlers.RoomHandler).Leave, "/rooms/" + roomID.String() + "/leave", url.Values{}, roomCall{operation: "leave", roomID: roomID}, http.StatusNoContent},
		{"kick", (*handlers.RoomHandler).Kick, "/rooms/" + roomID.String() + "/kick", url.Values{"target_user_id": {targetID.String()}}, roomCall{operation: "kick", roomID: roomID, targetID: targetID}, http.StatusNoContent},
		{"ready", (*handlers.RoomHandler).MarkReady, "/rooms/" + roomID.String() + "/ready", url.Values{}, roomCall{operation: "ready", roomID: roomID}, http.StatusNoContent},
		{"start", (*handlers.RoomHandler).Start, "/rooms/" + roomID.String() + "/start", url.Values{}, roomCall{operation: "start", roomID: roomID}, http.StatusNoContent},
	}
}

func serveRoomRequest(t *testing.T, op roomOperation, svc *stubRoomService, userID uuid.UUID, body, pathID string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	h := handlers.NewRoomHandler(svc, nil, false)
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
			if op.name == "create" {
				if got := rr.Header().Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
				var body struct {
					JoinCode string `json:"join_code"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode create response: %v", err)
				}
				if body.JoinCode != "ABC234" {
					t.Errorf("join code = %q, want ABC234", body.JoinCode)
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
