package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/auth"
	"github.com/softsrv/starter/internal/db"
)

type leaderboardRouterDB struct {
	db.DBTX
	t        *testing.T
	raceType uuid.UUID
	racer    uuid.UUID
	calls    int
}

func (d *leaderboardRouterDB) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	d.calls++
	if len(args) != 3 || args[0] != d.raceType {
		d.t.Fatalf("leaderboard query arguments = %v", args)
	}
	return &leaderboardRouterRows{racer: d.racer}, nil
}

type leaderboardRouterRows struct {
	pgx.Rows
	racer uuid.UUID
	done  bool
}

func (r *leaderboardRouterRows) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}
func (r *leaderboardRouterRows) Scan(dest ...any) error {
	*dest[0].(*uuid.UUID) = r.racer
	*dest[1].(*int32) = 1200
	return nil
}
func (r *leaderboardRouterRows) Close()     {}
func (r *leaderboardRouterRows) Err() error { return nil }

type leaderboardRouterFriends struct {
	actor, friend uuid.UUID
	t             *testing.T
}

func (f leaderboardRouterFriends) ListFriends(_ context.Context, caller uuid.UUID) ([]uuid.UUID, error) {
	if caller != f.actor {
		f.t.Fatalf("caller = %s, want %s", caller, f.actor)
	}
	return []uuid.UUID{f.friend}, nil
}

func TestLeaderboardRoutesAuthenticate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actor, racer, raceType := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	user := db.User{ID: actor, Email: "leaderboard@example.com", EmailVerified: false}
	store := &leaderboardRouterDB{t: t, raceType: raceType, racer: racer}
	h := NewRouter(ctx, RouterConfig{
		Queries: routerUserFetcher{user: user}, JWTSecret: routerTestJWTSecret,
		LeaderboardSvc: app.NewLeaderboardService(db.New(store), leaderboardRouterFriends{actor: actor, friend: racer, t: t}, app.LeaderboardServiceConfig{}),
	})
	token, err := auth.IssueAccessToken(actor, user.Email, routerTestJWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"global", "friends"} {
		for _, cookie := range []string{"", "invalid", token.AccessToken} {
			before := store.calls
			req := httptest.NewRequest(http.MethodGet, "/leaderboard?scope="+scope+"&race_type="+raceType.String(), nil)
			if cookie != "" {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: cookie})
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if cookie != token.AccessToken {
				if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" || store.calls != before {
					t.Fatalf("unauthenticated: status=%d headers=%v calls=%d", rr.Code, rr.Header(), store.calls)
				}
				continue
			}
			var got []db.LeaderboardPersonalBestRow
			if rr.Code != http.StatusOK || rr.Header().Get("Location") != "" || store.calls != before+1 {
				t.Fatalf("authenticated: status=%d headers=%v calls=%d", rr.Code, rr.Header(), store.calls)
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].UserID != racer || got[0].ElapsedMilliseconds != 1200 {
				t.Fatalf("ranking = %s (%v)", rr.Body, err)
			}
		}
	}
}
