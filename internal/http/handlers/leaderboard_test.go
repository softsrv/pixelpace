package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
)

type stubLeaderboardService struct {
	listFn func(context.Context, string, string, uuid.UUID, uuid.UUID) ([]db.LeaderboardPersonalBestRow, error)
}

func (s *stubLeaderboardService) Leaderboard(ctx context.Context, scope, window string, raceType, caller uuid.UUID) ([]db.LeaderboardPersonalBestRow, error) {
	return s.listFn(ctx, scope, window, raceType, caller)
}

func TestLeaderboardAuthenticatedActor(t *testing.T) {
	actor, raceType, racer := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	want := []db.LeaderboardPersonalBestRow{{UserID: racer, ElapsedMilliseconds: 1200}}
	for _, tc := range []struct{ query, scope, window string }{
		{"", "global", "all-time"},
		{"&scope=global&window=month", "global", "month"},
		{"&scope=friends&window=week", "friends", "week"},
	} {
		t.Run(tc.scope+"/"+tc.window, func(t *testing.T) {
			called := false
			h := handlers.NewLeaderboardHandler(&stubLeaderboardService{listFn: func(_ context.Context, scope, window string, gotType, caller uuid.UUID) ([]db.LeaderboardPersonalBestRow, error) {
				called = true
				if scope != tc.scope || window != tc.window || gotType != raceType || caller != actor {
					t.Fatalf("arguments = %s, %s, %s, %s", scope, window, gotType, caller)
				}
				return want, nil
			}})
			protected, req := authedRequest(t, h.Leaderboard, actor, http.MethodGet, "/leaderboard?race_type="+raceType.String()+tc.query+"&user_id="+racer.String())
			rr := httptest.NewRecorder()
			protected.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK || !called || rr.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status=%d called=%v headers=%v", rr.Code, called, rr.Header())
			}
			var got []db.LeaderboardPersonalBestRow
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("response = %s (%v)", rr.Body, err)
			}
		})
	}
}

func TestLeaderboardValidationAndAuthentication(t *testing.T) {
	h := handlers.NewLeaderboardHandler(&stubLeaderboardService{}) // Unexpected service calls panic.
	for _, path := range []string{"/leaderboard", "/leaderboard?race_type=bad-id"} {
		protected, req := authedRequest(t, h.Leaderboard, uuid.Must(uuid.NewV7()), http.MethodGet, path)
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid race type: status=%d", rr.Code)
		}
		req.Header.Del("Cookie")
		rr = httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Fatalf("unauthenticated: status=%d headers=%v", rr.Code, rr.Header())
		}
		rr = httptest.NewRecorder()
		h.Leaderboard(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("missing context: status=%d", rr.Code)
		}
	}
}

func TestLeaderboardErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{fmt.Errorf("wrapped: %w", app.ErrInvalidLeaderboardFilter), http.StatusBadRequest},
		{errors.New("private database details"), http.StatusInternalServerError},
	} {
		h := handlers.NewLeaderboardHandler(&stubLeaderboardService{listFn: func(context.Context, string, string, uuid.UUID, uuid.UUID) ([]db.LeaderboardPersonalBestRow, error) {
			return nil, tc.err
		}})
		id := uuid.Must(uuid.NewV7())
		protected, req := authedRequest(t, h.Leaderboard, id, http.MethodGet, "/leaderboard?race_type="+id.String())
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, req)
		if rr.Code != tc.status || strings.Contains(rr.Body.String(), "private database details") {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
		}
	}
}
