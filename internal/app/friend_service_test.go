package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
)

type friendTestDB struct {
	db.DBTX
	row func(...any) error
}

type friendTestRow struct{ scan func(...any) error }

func (r friendTestRow) Scan(dest ...any) error { return r.scan(dest...) }
func (d friendTestDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return friendTestRow{scan: d.row}
}

type friendTestBeginner struct{ pgxBeginner }

func TestNewFriendService(t *testing.T) {
	q := db.New(friendTestDB{})
	pool := &friendTestBeginner{}
	now := time.Now()
	cfg := FriendServiceConfig{Cooldown: time.Hour, Now: func() time.Time { return now }}
	s := NewFriendService(q, pool, cfg)
	if s.q != q || s.pool != pool || s.cfg.Cooldown != time.Hour || !s.cfg.Now().Equal(now) {
		t.Fatal("constructor did not preserve dependencies")
	}
	s = NewFriendService(q, pool, FriendServiceConfig{})
	before := time.Now()
	got := s.cfg.Now()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatal("default clock is not time.Now")
	}
}

func TestFriendSendUniqueViolation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"pending index", &pgconn.PgError{Code: "23505", ConstraintName: "idx_friend_requests_pending"}, ErrRequestPending},
		{"other index", &pgconn.PgError{Code: "23505", ConstraintName: "friend_requests_pkey"}, nil},
		{"other error", errors.New("database unavailable"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			q := db.New(friendTestDB{row: func(dest ...any) error {
				calls++
				switch calls {
				case 1:
					*dest[0].(*bool) = false
					return nil
				case 2:
					return pgx.ErrNoRows
				case 3:
					*dest[0].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
					return nil
				default:
					return fmt.Errorf("wrapped: %w", tc.err)
				}
			}})
			s := NewFriendService(q, nil, FriendServiceConfig{Cooldown: time.Hour})
			_, err := s.Send(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
			want := tc.want
			if want == nil {
				want = tc.err
			}
			if !errors.Is(err, want) || calls != 4 {
				t.Fatalf("Send error = %v, want %v; calls = %d", err, want, calls)
			}
		})
	}
}

func TestFriendSendSelfRefused(t *testing.T) {
	s := NewFriendService(nil, nil, FriendServiceConfig{})
	id := uuid.Must(uuid.NewV7())
	if _, err := s.Send(context.Background(), id, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Send to self = %v", err)
	}
}
