package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/softsrv/starter/internal/db"
)

// FriendServiceConfig holds the friendship policy and clock.
type FriendServiceConfig struct {
	Cooldown time.Duration
	// Now defaults to time.Now; injection allows deterministic boundary tests.
	Now func() time.Time
}

// FriendService handles friend requests and canonical friendships.
type FriendService struct {
	q    *db.Queries
	pool pgxBeginner
	cfg  FriendServiceConfig
}

// NewFriendService constructs a FriendService with all dependencies injected.
func NewFriendService(q *db.Queries, pool pgxBeginner, cfg FriendServiceConfig) *FriendService {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &FriendService{q: q, pool: pool, cfg: cfg}
}

// Send creates a pending request, respecting the sender's rejection cooldown.
func (s *FriendService) Send(ctx context.Context, senderID, recipientID uuid.UUID) (db.FriendRequest, error) {
	if senderID == recipientID {
		return db.FriendRequest{}, ErrForbidden
	}
	a, b := canonicalFriendPair(senderID, recipientID)
	exists, err := s.q.FriendshipExists(ctx, db.FriendshipExistsParams{UserIDA: a, UserIDB: b})
	if err != nil {
		return db.FriendRequest{}, fmt.Errorf("check friendship: %w", err)
	}
	if exists {
		return db.FriendRequest{}, ErrAlreadyFriends
	}
	_, err = s.q.GetPendingFriendRequest(ctx, db.GetPendingFriendRequestParams{SenderID: senderID, RecipientID: recipientID})
	if err == nil {
		return db.FriendRequest{}, ErrRequestPending
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.FriendRequest{}, fmt.Errorf("get pending friend request: %w", err)
	}
	latest, err := s.q.LatestFriendRequestRejection(ctx, db.LatestFriendRequestRejectionParams{SenderID: senderID, RecipientID: recipientID})
	if err != nil {
		return db.FriendRequest{}, fmt.Errorf("get friend request cooldown: %w", err)
	}
	if latest.Valid && s.cfg.Now().Sub(latest.Time) < s.cfg.Cooldown {
		return db.FriendRequest{}, ErrCooldownActive
	}
	id, err := uuid.NewV7()
	if err != nil {
		return db.FriendRequest{}, fmt.Errorf("generate friend request id: %w", err)
	}
	request, err := s.q.CreateFriendRequest(ctx, db.CreateFriendRequestParams{ID: id, SenderID: senderID, RecipientID: recipientID})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_friend_requests_pending" {
			return db.FriendRequest{}, ErrRequestPending
		}
		return db.FriendRequest{}, fmt.Errorf("create friend request: %w", err)
	}
	return request, nil
}

// Accept decides a request and creates the friendship atomically.
func (s *FriendService) Accept(ctx context.Context, actingUserID, requestID uuid.UUID) error {
	request, err := s.recipientRequest(ctx, actingUserID, requestID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.ErrorContext(ctx, "rollback friend acceptance", "error", err)
		}
	}()
	qtx := s.q.WithTx(tx)
	if err := decideFriendRequest(ctx, qtx, request.ID, "accepted"); err != nil {
		return err
	}
	a, b := canonicalFriendPair(request.SenderID, request.RecipientID)
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate friendship id: %w", err)
	}
	if err := qtx.CreateFriendship(ctx, db.CreateFriendshipParams{ID: id, UserIDA: a, UserIDB: b}); err != nil {
		return fmt.Errorf("create friendship: %w", err)
	}
	opposite, err := qtx.GetPendingFriendRequest(ctx, db.GetPendingFriendRequestParams{SenderID: request.RecipientID, RecipientID: request.SenderID})
	if err == nil {
		// Acceptance resolves both directions without starting a rejection cooldown.
		if err := decideFriendRequest(ctx, qtx, opposite.ID, "accepted"); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get opposite friend request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Reject starts the directional cooldown without creating a friendship.
func (s *FriendService) Reject(ctx context.Context, actingUserID, requestID uuid.UUID) error {
	request, err := s.recipientRequest(ctx, actingUserID, requestID)
	if err != nil {
		return err
	}
	return decideFriendRequest(ctx, s.q, request.ID, "rejected")
}

func (s *FriendService) recipientRequest(ctx context.Context, actingUserID, requestID uuid.UUID) (db.FriendRequest, error) {
	request, err := s.q.GetFriendRequest(ctx, requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.FriendRequest{}, ErrNoSuchRequest
	}
	if err != nil {
		return db.FriendRequest{}, fmt.Errorf("get friend request: %w", err)
	}
	if request.RecipientID != actingUserID {
		return db.FriendRequest{}, ErrNotRecipient
	}
	if request.Status != "pending" {
		return db.FriendRequest{}, ErrNoSuchRequest
	}
	return request, nil
}

func decideFriendRequest(ctx context.Context, q *db.Queries, id uuid.UUID, status string) error {
	_, err := q.DecideFriendRequest(ctx, db.DecideFriendRequestParams{ID: id, Status: status})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSuchRequest
	}
	if err != nil {
		return fmt.Errorf("decide friend request: %w", err)
	}
	return nil
}

func canonicalFriendPair(a, b uuid.UUID) (uuid.UUID, uuid.UUID) {
	if bytes.Compare(a[:], b[:]) > 0 {
		return b, a
	}
	return a, b
}

// ListFriends returns the other user in each friendship involving userID.
func (s *FriendService) ListFriends(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	friends, err := s.q.ListFriends(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	return friends, nil
}
