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
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/http/handlers"
)

type stubLeaderboardService struct {
	listFn func(context.Context, string, string, uuid.UUID, uuid.UUID) ([]app.LeaderboardRow, error)
}

func (s *stubLeaderboardService) Leaderboard(ctx context.Context, scope, window string, raceType, caller uuid.UUID) ([]app.LeaderboardRow, error) {
	return s.listFn(ctx, scope, window, raceType, caller)
}

func TestLeaderboardAuthenticatedActor(t *testing.T) {
	actor, raceType, racer := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	want := []app.LeaderboardRow{{UserID: racer, Name: "Speedy Racer", ElapsedMilliseconds: 1200}}
	for _, tc := range []struct{ query, scope, window string }{
		{"", "global", "all-time"},
		{"&scope=global&window=month", "global", "month"},
		{"&scope=friends&window=week", "friends", "week"},
	} {
		t.Run(tc.scope+"/"+tc.window, func(t *testing.T) {
			called := false
			h := handlers.NewLeaderboardHandler(&stubLeaderboardService{listFn: func(_ context.Context, scope, window string, gotType, caller uuid.UUID) ([]app.LeaderboardRow, error) {
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
			var got []app.LeaderboardRow
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("response = %s (%v)", rr.Body, err)
			}
			var raw []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil || len(raw) != 1 || raw[0]["name"] != "Speedy Racer" {
				t.Fatalf("json row missing name field: %s", rr.Body)
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
		h := handlers.NewLeaderboardHandler(&stubLeaderboardService{listFn: func(context.Context, string, string, uuid.UUID, uuid.UUID) ([]app.LeaderboardRow, error) {
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

func TestLeaderboardNoEmailInResponse(t *testing.T) {
	actor := uuid.Must(uuid.NewV7())
	racer1, racer2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	racer1Email := fmt.Sprintf("%s@example.com", racer1)
	racer2Email := fmt.Sprintf("%s@corp.test", racer2)
	actorEmail := fmt.Sprintf("%s@actor.io", actor)
	forbiddenEmails := []string{racer1Email, racer2Email, actorEmail}

	rows := []app.LeaderboardRow{
		{UserID: racer1, Name: "Speedy", ElapsedMilliseconds: 1000},
		{UserID: racer2, Name: app.NewPublicPlayer(racer2, pgtype.Text{}).Name, ElapsedMilliseconds: 2000},
	}

	h := handlers.NewLeaderboardHandler(&stubLeaderboardService{
		listFn: func(_ context.Context, scope, window string, raceType, caller uuid.UUID) ([]app.LeaderboardRow, error) {
			return rows, nil
		},
	})

	for _, scope := range []string{"global", "friends"} {
		t.Run("scope="+scope, func(t *testing.T) {
			path := fmt.Sprintf("/leaderboard?race_type=%s&scope=%s", uuid.Must(uuid.NewV7()), scope)
			protected, req := authedRequest(t, h.Leaderboard, actor, http.MethodGet, path)
			rr := httptest.NewRecorder()
			protected.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			bodyStr := rr.Body.String()
			for _, email := range forbiddenEmails {
				if strings.Contains(bodyStr, email) {
					t.Fatalf("response leaked email %s: %s", email, bodyStr)
				}
			}
			if strings.Contains(bodyStr, "@") {
				t.Fatalf("response contains '@': %s", bodyStr)
			}
			var decoded []app.LeaderboardRow
			if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("failed to decode: %v", err)
			}
			if len(decoded) == 0 {
				t.Fatalf("expected at least one row, got 0")
			}
		})
	}
}

func TestLeaderboardRowReflection(t *testing.T) {
	rowType := reflect.TypeOf(app.LeaderboardRow{})
	if rowType == reflect.TypeOf(db.User{}) {
		t.Fatalf("LeaderboardRow must not be db.User")
	}
	for i := 0; i < rowType.NumField(); i++ {
		field := rowType.Field(i)
		if strings.EqualFold(field.Name, "email") {
			t.Fatalf("LeaderboardRow has forbidden field %s", field.Name)
		}
		jsonTag := field.Tag.Get("json")
		tagName := strings.Split(jsonTag, ",")[0]
		if strings.EqualFold(tagName, "email") {
			t.Fatalf("LeaderboardRow has forbidden json tag %s", tagName)
		}
	}
}
