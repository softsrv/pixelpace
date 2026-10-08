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

type stubQuickMatchService struct {
	enqueueFn func(context.Context, uuid.UUID, uuid.UUID) error
	leaveFn   func(context.Context, uuid.UUID, uuid.UUID) error
	matchFn   func(context.Context, uuid.UUID, uuid.UUID) (string, error)
}

func (s *stubQuickMatchService) Enqueue(ctx context.Context, user, race uuid.UUID) error {
	return s.enqueueFn(ctx, user, race)
}
func (s *stubQuickMatchService) Leave(ctx context.Context, user, race uuid.UUID) error {
	return s.leaveFn(ctx, user, race)
}
func (s *stubQuickMatchService) Match(ctx context.Context, user, race uuid.UUID) (string, error) {
	return s.matchFn(ctx, user, race)
}

func TestQuickMatchHandlers(t *testing.T) {
	for _, op := range []struct {
		name    string
		handler func(*handlers.QuickMatchHandler, http.ResponseWriter, *http.Request)
		status  int
	}{
		{"enqueue", (*handlers.QuickMatchHandler).Enqueue, http.StatusCreated},
		{"leave", (*handlers.QuickMatchHandler).Leave, http.StatusNoContent},
		{"match", (*handlers.QuickMatchHandler).Match, http.StatusCreated},
	} {
		for _, tc := range []struct {
			name          string
			authenticated bool
			body          string
			err           error
			code          string
			status        int
		}{
			{"success", true, "valid", nil, "ABC234", op.status},
			{"waiting", true, "valid", nil, "", op.status},
			{"conflict", true, "valid", fmt.Errorf("wrapped: %w", app.ErrAlreadyQueued), "", http.StatusConflict},
			{"invalid race", true, "valid", app.ErrInvalidRaceType, "", http.StatusConflict},
			{"not queued", true, "valid", app.ErrNotQueued, "", http.StatusNotFound},
			{"internal", true, "valid", errors.New("private database detail"), "", http.StatusInternalServerError},
			{"unauthorized", false, "valid", nil, "", http.StatusUnauthorized},
			{"bad uuid", true, "race_type_id=nope", nil, "", http.StatusBadRequest},
			{"bad form", true, "%zz", nil, "", http.StatusBadRequest},
		} {
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				userID, raceID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
				calls := 0
				record := func(_ context.Context, user, race uuid.UUID) error {
					calls++
					if user != userID || race != raceID {
						t.Errorf("identity = %v/%v", user, race)
					}
					return tc.err
				}
				svc := &stubQuickMatchService{enqueueFn: record, leaveFn: record, matchFn: func(ctx context.Context, user, race uuid.UUID) (string, error) {
					return tc.code, record(ctx, user, race)
				}}
				h := handlers.NewQuickMatchHandler(svc)
				var endpoint http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { op.handler(h, w, r) })
				body := tc.body
				if body == "valid" {
					body = url.Values{"race_type_id": {raceID.String()}, "user_id": {uuid.NewString()}}.Encode()
				}
				req := httptest.NewRequest(http.MethodPost, "/quick-match", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if tc.authenticated {
					user := db.User{ID: userID, Email: "match@example.com", EmailVerified: true}
					token, err := auth.IssueAccessToken(userID, user.Email, testJWTSecret, time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
					endpoint = middleware.Authenticate(&handlerUserFetcher{user: user}, testJWTSecret)(endpoint)
				}
				rr := httptest.NewRecorder()
				endpoint.ServeHTTP(rr, req)
				want := tc.status
				if op.name == "match" && tc.name == "waiting" {
					want = http.StatusAccepted
				}
				if rr.Code != want {
					t.Fatalf("status %d, want %d: %s", rr.Code, want, rr.Body.String())
				}
				wantCalls := 0
				if tc.authenticated && tc.body == "valid" {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("calls %d, want %d", calls, wantCalls)
				}
				if op.name == "match" && tc.name == "success" {
					var result map[string]string
					if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result["join_code"] != tc.code || rr.Header().Get("Content-Type") != "application/json" {
						t.Fatalf("response %v", result)
					}
				}
				if strings.Contains(rr.Body.String(), "private database detail") {
					t.Fatal("leaked error")
				}
			})
		}
	}
}
