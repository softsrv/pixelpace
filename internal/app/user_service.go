package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/users"
)

// UserService handles user-related business logic.
type UserService struct {
	q *db.Queries
}

// NewUserService constructs a UserService.
func NewUserService(q *db.Queries) *UserService {
	return &UserService{q: q}
}

// GetByID fetches a user by ID.
func (s *UserService) GetByID(ctx context.Context, id uuid.UUID) (db.User, error) {
	user, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, ErrUserNotFound
		}
		return db.User{}, fmt.Errorf("get user by id: %w", err)
	}
	return user, nil
}

// SetDisplayName sets the user's public display name, stored exactly as typed
// after trimming surrounding whitespace. Validation runs before any write, so a
// refused name leaves the stored value untouched. A name already held by
// another account (compared ignoring case, enforced by a unique index on
// lower(display_name)) returns ErrDisplayNameTaken; the index makes this hold
// for concurrent claims too. Validation failures return the users.ErrDisplayName*
// sentinels unwrapped so callers can match them with errors.Is.
func (s *UserService) SetDisplayName(ctx context.Context, id uuid.UUID, name string) error {
	name = strings.TrimSpace(name)
	if err := users.ValidateDisplayName(name); err != nil {
		return err
	}
	err := s.q.SetUserDisplayName(ctx, db.SetUserDisplayNameParams{
		ID:          id,
		DisplayName: pgtype.Text{String: name, Valid: true},
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDisplayNameTaken
		}
		return fmt.Errorf("set display name: %w", err)
	}
	return nil
}

// ListSessions returns all active refresh tokens for a user.
func (s *UserService) ListSessions(ctx context.Context, userID uuid.UUID) ([]db.RefreshToken, error) {
	sessions, err := s.q.ListActiveRefreshTokensByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

// DeleteAccount permanently deletes the user and all associated data via cascade.
func (s *UserService) DeleteAccount(ctx context.Context, userID uuid.UUID) error {
	if err := s.q.DeleteUserByID(ctx, userID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}

// RevokeSession revokes a specific refresh token, enforcing ownership.
// It returns the revoked token so the caller can determine whether it was the
// caller's own active session.
func (s *UserService) RevokeSession(ctx context.Context, userID, tokenID uuid.UUID) (db.RefreshToken, error) {
	rt, err := s.q.GetRefreshTokenByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RefreshToken{}, ErrTokenNotFound
		}
		return db.RefreshToken{}, fmt.Errorf("get session: %w", err)
	}

	// Authorization check: the token must belong to the requesting user.
	if rt.UserID != userID {
		return db.RefreshToken{}, ErrForbidden
	}

	return rt, s.q.RevokeRefreshToken(ctx, tokenID)
}
