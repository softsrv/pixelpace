package http

import (
	"context"
	"errors"
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

type friendRouterDB struct {
	db.DBTX
	t      *testing.T
	actor  uuid.UUID
	called bool
}

func (d *friendRouterDB) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	d.called = true
	if len(args) != 1 || args[0] != d.actor {
		d.t.Fatalf("list query arguments = %v, want authenticated user %s", args, d.actor)
	}
	return nil, errors.New("database unavailable")
}

func TestFriendRoutesAuthenticate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actor := uuid.Must(uuid.NewV7())
	// Friends require authentication, not email verification.
	user := db.User{ID: actor, Email: "friends@example.com", EmailVerified: false}
	store := &friendRouterDB{t: t, actor: actor}
	h := NewRouter(ctx, RouterConfig{
		Queries: routerUserFetcher{user: user}, JWTSecret: routerTestJWTSecret,
		FriendSvc: app.NewFriendService(db.New(store), nil, app.FriendServiceConfig{Cooldown: 10 * 24 * time.Hour}),
	})
	token, err := auth.IssueAccessToken(actor, user.Email, routerTestJWTSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct {
		method, path string
		authedStatus int
	}{
		{http.MethodPost, "/friends/requests", 400},
		{http.MethodPost, "/friends/requests/bad-id/accept", 400},
		{http.MethodPost, "/friends/requests/bad-id/reject", 400},
		{http.MethodGet, "/friends", 500},
	} {
		t.Run(route.path, func(t *testing.T) {
			for _, authenticated := range []bool{false, true} {
				req := httptest.NewRequest(route.method, route.path, nil)
				want := http.StatusSeeOther
				if authenticated {
					req.AddCookie(&http.Cookie{Name: "access_token", Value: token.AccessToken})
					want = route.authedStatus
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				if rr.Code != want {
					t.Fatalf("authenticated=%v: status = %d, want %d", authenticated, rr.Code, want)
				}
				if !authenticated && rr.Header().Get("Location") != "/login" {
					t.Fatalf("redirect = %q", rr.Header().Get("Location"))
				}
			}
		})
	}
	if !store.called {
		t.Fatal("list route never reached FriendService")
	}
}
