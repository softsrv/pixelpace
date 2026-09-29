//go:build integration

package app_test

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/app"
	"github.com/softsrv/starter/internal/db"
)

const friendCooldown = 10 * 24 * time.Hour

func testFriendService(t *testing.T, pool *pgxpool.Pool, now time.Time) *app.FriendService {
	t.Helper()
	return app.NewFriendService(db.New(pool), pool, app.FriendServiceConfig{
		Cooldown: friendCooldown,
		Now:      func() time.Time { return now },
	})
}

func friendTestID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	return id
}

func friendTestUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := friendTestID(t)
	_, err := db.New(pool).CreateUser(ctx, db.CreateUserParams{ID: id, Email: id.String() + "@example.com", PasswordHash: "unused"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		// These foreign keys do not cascade; delete dependants before the user.
		for _, query := range []string{
			"DELETE FROM friend_requests WHERE sender_id = $1 OR recipient_id = $1",
			"DELETE FROM friendships WHERE user_id_a = $1 OR user_id_b = $1",
			"DELETE FROM users WHERE id = $1",
		} {
			if _, err := pool.Exec(ctx, query, id); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return id
}

func seedFriendDecision(t *testing.T, pool *pgxpool.Pool, sender, recipient uuid.UUID, status string, at time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO friend_requests (id, sender_id, recipient_id, status, decided_at) VALUES ($1, $2, $3, $4, $5)",
		friendTestID(t), sender, recipient, status, at)
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}
}

func TestIntegration_FriendCooldownBoundary(t *testing.T) {
	pool := testDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	svc := testFriendService(t, pool, now)
	for _, tc := range []struct {
		name string
		age  time.Duration
		want error
	}{
		{"one microsecond under", friendCooldown - time.Microsecond, app.ErrCooldownActive},
		{"exactly ten days", friendCooldown, nil},
		{"after ten days", friendCooldown + time.Microsecond, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := friendTestUser(t, pool), friendTestUser(t, pool)
			seedFriendDecision(t, pool, a, b, "rejected", now.Add(-tc.age))
			// Older rejection must not hide the latest; accepted rows must not
			// participate in MAX(decided_at), even when they are more recent.
			seedFriendDecision(t, pool, a, b, "rejected", now.Add(-2*friendCooldown))
			seedFriendDecision(t, pool, a, b, "accepted", now)
			request, err := svc.Send(context.Background(), a, b)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Send = %v, want %v", err, tc.want)
			}
			if tc.want == nil {
				if request.Status != "pending" || request.SenderID != a || request.RecipientID != b || request.ID.Version() != 7 {
					t.Fatalf("unexpected request: %+v", request)
				}
			} else {
				var count int
				if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM friend_requests WHERE sender_id=$1 AND recipient_id=$2 AND status='pending'", a, b).Scan(&count); err != nil || count != 0 {
					t.Fatalf("blocked send left pending rows = %d (%v)", count, err)
				}
			}
		})
	}
}

func TestIntegration_FriendCooldownAsymmetric(t *testing.T) {
	pool := testDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	svc := testFriendService(t, pool, now)
	a, b := friendTestUser(t, pool), friendTestUser(t, pool)
	seedFriendDecision(t, pool, a, b, "rejected", now)
	if _, err := svc.Send(context.Background(), a, b); !errors.Is(err, app.ErrCooldownActive) {
		t.Fatalf("forward Send = %v", err)
	}
	if _, err := svc.Send(context.Background(), b, a); err != nil {
		t.Fatalf("reverse Send = %v", err)
	}
}

func TestIntegration_FriendSendDuplicate(t *testing.T) {
	pool := testDB(t)
	svc := testFriendService(t, pool, time.Now())
	ctx := context.Background()
	a, b := friendTestUser(t, pool), friendTestUser(t, pool)
	request, err := svc.Send(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.New(pool).GetFriendRequest(ctx, request.ID)
	if err != nil || stored.Status != "pending" || stored.SenderID != a || stored.RecipientID != b || stored.DecidedAt.Valid {
		t.Fatalf("stored request = %+v (%v)", stored, err)
	}
	if _, err := svc.Send(ctx, a, b); !errors.Is(err, app.ErrRequestPending) {
		t.Fatalf("duplicate = %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM friend_requests WHERE sender_id=$1 AND recipient_id=$2 AND status='pending'", a, b).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending count = %d (%v)", count, err)
	}
}

func TestIntegration_FriendConcurrentSends(t *testing.T) {
	pool := testDB(t)
	svc := testFriendService(t, pool, time.Now())
	a, b := friendTestUser(t, pool), friendTestUser(t, pool)
	const n = 8
	start := make(chan struct{})
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Send(context.Background(), a, b)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, app.ErrRequestPending) {
			t.Errorf("Send = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful sends = %d", successes)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM friend_requests WHERE sender_id=$1 AND recipient_id=$2 AND status='pending'", a, b).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending count = %d (%v)", count, err)
	}
}

func TestIntegration_FriendAcceptAndList(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := testFriendService(t, pool, time.Now())
	ids := []uuid.UUID{friendTestUser(t, pool), friendTestUser(t, pool), friendTestUser(t, pool)}
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })
	middle := ids[1]
	for _, other := range []uuid.UUID{ids[0], ids[2]} {
		request, err := svc.Send(ctx, middle, other)
		if err != nil {
			t.Fatal(err)
		}
		opposite, err := svc.Send(ctx, other, middle)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Accept(ctx, middle, request.ID); !errors.Is(err, app.ErrNotRecipient) {
			t.Fatalf("non-recipient Accept = %v", err)
		}
		friends, err := svc.ListFriends(ctx, middle)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range friends {
			if id == other {
				t.Fatal("unauthorized accept created friendship")
			}
		}
		if err := svc.Accept(ctx, other, request.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []uuid.UUID{request.ID, opposite.ID} {
			stored, err := db.New(pool).GetFriendRequest(ctx, id)
			if err != nil || stored.Status != "accepted" || !stored.DecidedAt.Valid {
				t.Fatalf("decision = %+v (%v)", stored, err)
			}
		}
		var a, b, friendshipID uuid.UUID
		if err := pool.QueryRow(ctx, "SELECT id, user_id_a, user_id_b FROM friendships WHERE (user_id_a=$1 AND user_id_b=$2) OR (user_id_a=$2 AND user_id_b=$1)", middle, other).Scan(&friendshipID, &a, &b); err != nil {
			t.Fatal(err)
		}
		if bytes.Compare(a[:], b[:]) >= 0 || friendshipID.Version() != 7 {
			t.Fatalf("noncanonical friendship: %s %s %s", friendshipID, a, b)
		}
		var pending int
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM friend_requests WHERE status='pending' AND ((sender_id=$1 AND recipient_id=$2) OR (sender_id=$2 AND recipient_id=$1))", middle, other).Scan(&pending); err != nil || pending != 0 {
			t.Fatalf("pending = %d (%v)", pending, err)
		}
		for _, pair := range [][2]uuid.UUID{{middle, other}, {other, middle}} {
			if _, err := svc.Send(ctx, pair[0], pair[1]); !errors.Is(err, app.ErrAlreadyFriends) {
				t.Fatalf("already friends Send = %v", err)
			}
		}
		if err := svc.Accept(ctx, other, request.ID); !errors.Is(err, app.ErrNoSuchRequest) {
			t.Fatalf("repeated Accept = %v", err)
		}
		if err := svc.Reject(ctx, other, request.ID); !errors.Is(err, app.ErrNoSuchRequest) {
			t.Fatalf("reject accepted request = %v", err)
		}
	}
	friends, err := svc.ListFriends(ctx, middle)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(friends, func(i, j int) bool { return bytes.Compare(friends[i][:], friends[j][:]) < 0 })
	if len(friends) != 2 || friends[0] != ids[0] || friends[1] != ids[2] {
		t.Fatalf("friends = %v", friends)
	}
}

func TestIntegration_FriendReject(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := testFriendService(t, pool, time.Now())
	a, b := friendTestUser(t, pool), friendTestUser(t, pool)
	request, err := svc.Send(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Reject(ctx, a, request.ID); !errors.Is(err, app.ErrNotRecipient) {
		t.Fatalf("non-recipient Reject = %v", err)
	}
	stored, err := db.New(pool).GetFriendRequest(ctx, request.ID)
	if err != nil || stored.Status != "pending" || stored.DecidedAt.Valid {
		t.Fatalf("unauthorized mutation: %+v (%v)", stored, err)
	}
	if err := svc.Reject(ctx, b, request.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = db.New(pool).GetFriendRequest(ctx, request.ID)
	if err != nil || stored.Status != "rejected" || !stored.DecidedAt.Valid {
		t.Fatalf("rejection = %+v (%v)", stored, err)
	}
	friends, err := svc.ListFriends(ctx, a)
	if err != nil || len(friends) != 0 {
		t.Fatalf("friends after reject = %v (%v)", friends, err)
	}
	if _, err := svc.Send(ctx, a, b); !errors.Is(err, app.ErrCooldownActive) {
		t.Fatalf("Send after reject = %v", err)
	}
	for _, decide := range []func(context.Context, uuid.UUID, uuid.UUID) error{svc.Accept, svc.Reject} {
		if err := decide(ctx, b, friendTestID(t)); !errors.Is(err, app.ErrNoSuchRequest) {
			t.Fatalf("missing request = %v", err)
		}
		if err := decide(ctx, b, request.ID); !errors.Is(err, app.ErrNoSuchRequest) {
			t.Fatalf("already decided = %v", err)
		}
	}
}

func TestIntegration_FriendAcceptRollback(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	svc := testFriendService(t, pool, time.Now())
	a, b := friendTestUser(t, pool), friendTestUser(t, pool)
	if bytes.Compare(a[:], b[:]) > 0 {
		a, b = b, a
	}
	request, err := svc.Send(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	// Force the friendship insert to fail after the request update.
	if err := db.New(pool).CreateFriendship(ctx, db.CreateFriendshipParams{ID: friendTestID(t), UserIDA: a, UserIDB: b}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Accept(ctx, b, request.ID); err == nil {
		t.Fatal("expected duplicate friendship error")
	}
	stored, err := db.New(pool).GetFriendRequest(ctx, request.ID)
	if err != nil || stored.Status != "pending" || stored.DecidedAt.Valid {
		t.Fatalf("request update escaped rollback: %+v (%v)", stored, err)
	}
}
