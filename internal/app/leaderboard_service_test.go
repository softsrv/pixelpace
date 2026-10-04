package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
)

type leaderboardDB struct {
	db.DBTX
	queryFn func(context.Context, string, ...any) (pgx.Rows, error)
}

func (d leaderboardDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return d.queryFn(ctx, query, args...)
}

type leaderboardFriends struct {
	listFn func(context.Context, uuid.UUID) ([]uuid.UUID, error)
}

func (f leaderboardFriends) ListFriends(ctx context.Context, caller uuid.UUID) ([]uuid.UUID, error) {
	return f.listFn(ctx, caller)
}

func TestLeaderboardServiceFilters(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	actor, friend, raceType := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	queryErr := errors.New("query stopped")
	for _, scope := range []string{"global", "friends"} {
		for _, tc := range []struct {
			window string
			bound  pgtype.Timestamptz
		}{
			{"all-time", pgtype.Timestamptz{}},
			{"month", pgtype.Timestamptz{Time: time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC), Valid: true}},
			{"week", pgtype.Timestamptz{Time: time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC), Valid: true}},
		} {
			t.Run(scope+"/"+tc.window, func(t *testing.T) {
				resolved, queried := false, false
				friends := leaderboardFriends{listFn: func(_ context.Context, caller uuid.UUID) ([]uuid.UUID, error) {
					resolved = true
					if caller != actor {
						t.Fatalf("caller = %s", caller)
					}
					return []uuid.UUID{friend}, nil
				}}
				store := leaderboardDB{queryFn: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
					queried = true
					if len(args) != 3 || args[0] != raceType || args[1] != tc.bound {
						t.Fatalf("args = %v", args)
					}
					var wantFriends []uuid.UUID
					if scope == "friends" {
						wantFriends = []uuid.UUID{friend}
					}
					if !reflect.DeepEqual(args[2], wantFriends) {
						t.Fatalf("friends = %v, want %v", args[2], wantFriends)
					}
					return nil, queryErr
				}}
				svc := app.NewLeaderboardService(db.New(store), friends, app.LeaderboardServiceConfig{Now: func() time.Time { return now }})
				_, err := svc.Leaderboard(context.Background(), scope, tc.window, raceType, actor)
				if !queried || resolved != (scope == "friends") || !errors.Is(err, queryErr) {
					t.Fatalf("queried=%v resolved=%v err=%v", queried, resolved, err)
				}
			})
		}
	}
}

func TestLeaderboardServiceEmptyFriends(t *testing.T) {
	for _, ids := range [][]uuid.UUID{nil, {}} {
		queried := false
		store := leaderboardDB{queryFn: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
			queried = true
			got := args[2].([]uuid.UUID)
			if got == nil || len(got) != 0 {
				t.Fatalf("empty friends = %#v", got)
			}
			return nil, errors.New("query stopped")
		}}
		svc := app.NewLeaderboardService(db.New(store), leaderboardFriends{listFn: func(context.Context, uuid.UUID) ([]uuid.UUID, error) { return ids, nil }}, app.LeaderboardServiceConfig{})
		if _, err := svc.Leaderboard(context.Background(), "friends", "all-time", uuid.Nil, uuid.Nil); err == nil || !queried {
			t.Fatalf("queried=%v err=%v", queried, err)
		}
	}
}

func TestLeaderboardServiceInvalidFiltersAndResolverError(t *testing.T) {
	svc := app.NewLeaderboardService(nil, nil, app.LeaderboardServiceConfig{})
	for _, tc := range []struct{ scope, window string }{{"unknown", "week"}, {"global", "unknown"}} {
		if _, err := svc.Leaderboard(context.Background(), tc.scope, tc.window, uuid.Nil, uuid.Nil); !errors.Is(err, app.ErrInvalidLeaderboardFilter) {
			t.Fatalf("err = %v", err)
		}
	}
	cause := errors.New("friends unavailable")
	svc = app.NewLeaderboardService(nil, leaderboardFriends{listFn: func(context.Context, uuid.UUID) ([]uuid.UUID, error) { return nil, cause }}, app.LeaderboardServiceConfig{})
	if _, err := svc.Leaderboard(context.Background(), "friends", "week", uuid.Nil, uuid.Nil); !errors.Is(err, cause) {
		t.Fatalf("err = %v", err)
	}
}

func TestLeaderboardServiceDefaultClock(t *testing.T) {
	before := time.Now().UTC().Add(-7 * 24 * time.Hour)
	called := false
	store := leaderboardDB{queryFn: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
		called = true
		bound := args[1].(pgtype.Timestamptz)
		after := time.Now().UTC().Add(-7 * 24 * time.Hour)
		if !bound.Valid || bound.Time.Before(before) || bound.Time.After(after) {
			t.Fatalf("bound = %v, want between %v and %v", bound, before, after)
		}
		return nil, errors.New("query stopped")
	}}
	svc := app.NewLeaderboardService(db.New(store), nil, app.LeaderboardServiceConfig{})
	if _, err := svc.Leaderboard(context.Background(), "global", "week", uuid.Nil, uuid.Nil); err == nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
