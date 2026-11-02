package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/softsrv/starter/internal/db"
)

var (
	// ErrConcept2NotConnected is returned when the user has no Concept2 connection.
	ErrConcept2NotConnected = errors.New("concept2 logbook is not connected")
	// ErrConcept2NoSealer is returned when tokens would be stored without a sealer.
	// Tokens are never written in plaintext.
	ErrConcept2NoSealer = errors.New("concept2 token sealer is not configured")
)

// TokenSealer seals and opens Concept2 OAuth tokens for storage. The user id
// is bound to every sealed blob, so a blob sealed for one user does not open
// for another.
type TokenSealer interface {
	Seal(plaintext []byte, userID uuid.UUID) ([]byte, error)
	Open(sealed []byte, userID uuid.UUID) ([]byte, error)
}

// Concept2ServiceConfig configures a Concept2Service.
type Concept2ServiceConfig struct {
	// Sealer encrypts tokens before every write. Required to save a connection.
	Sealer TokenSealer
}

// Concept2Service stores each PixelPace user's Concept2 Logbook connection.
// Every method takes the signed-in user's id and every query is keyed by it,
// so acting for one user never touches another user's connection.
type Concept2Service struct {
	q   *db.Queries
	cfg Concept2ServiceConfig
}

// NewConcept2Service constructs a Concept2Service.
func NewConcept2Service(q *db.Queries, cfg Concept2ServiceConfig) *Concept2Service {
	return &Concept2Service{q: q, cfg: cfg}
}

// Concept2Connection is what a user may see of their own connection.
// It never carries the tokens.
type Concept2Connection struct {
	C2UserID       int64
	Username       string
	ConnectedAt    time.Time
	AutoPost       bool
	NeedsReconnect bool
	Timezone       string
}

// Concept2Grant is a newly obtained Concept2 connection to store for a user.
type Concept2Grant struct {
	C2UserID     int64
	Username     string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scope        string
	Timezone     string // IANA name, already validated by the caller; empty means UTC
}

// Status returns userID's connection, or ErrConcept2NotConnected.
func (s *Concept2Service) Status(ctx context.Context, userID uuid.UUID) (Concept2Connection, error) {
	row, err := s.q.GetConcept2Connection(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Concept2Connection{}, ErrConcept2NotConnected
		}
		return Concept2Connection{}, fmt.Errorf("get concept2 connection: %w", err)
	}
	return toConcept2Connection(row), nil
}

// SaveConnection connects (or reconnects) userID's Concept2 account. Both
// tokens are sealed with the user id before the write; a reconnect overwrites
// the previous tokens, account and timezone and clears needs_reconnect.
func (s *Concept2Service) SaveConnection(ctx context.Context, userID uuid.UUID, g Concept2Grant) (Concept2Connection, error) {
	if s.cfg.Sealer == nil {
		return Concept2Connection{}, ErrConcept2NoSealer
	}
	access, err := s.cfg.Sealer.Seal([]byte(g.AccessToken), userID)
	if err != nil {
		return Concept2Connection{}, fmt.Errorf("seal concept2 access token: %w", err)
	}
	refresh, err := s.cfg.Sealer.Seal([]byte(g.RefreshToken), userID)
	if err != nil {
		return Concept2Connection{}, fmt.Errorf("seal concept2 refresh token: %w", err)
	}
	tz := strings.TrimSpace(g.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	row, err := s.q.UpsertConcept2Connection(ctx, db.UpsertConcept2ConnectionParams{
		UserID:          userID,
		C2UserID:        g.C2UserID,
		C2Username:      g.Username,
		AccessTokenEnc:  access,
		RefreshTokenEnc: refresh,
		ExpiresAt:       pgtype.Timestamptz{Time: g.ExpiresAt, Valid: true},
		Scope:           g.Scope,
		Timezone:        tz,
	})
	if err != nil {
		return Concept2Connection{}, fmt.Errorf("upsert concept2 connection: %w", err)
	}
	return toConcept2Connection(row), nil
}

// Disconnect deletes userID's connection. It is idempotent: disconnecting a
// user who is not connected succeeds.
func (s *Concept2Service) Disconnect(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.q.DeleteConcept2Connection(ctx, userID); err != nil {
		return fmt.Errorf("delete concept2 connection: %w", err)
	}
	return nil
}

func toConcept2Connection(row db.Concept2Connection) Concept2Connection {
	return Concept2Connection{
		C2UserID:       row.C2UserID,
		Username:       row.C2Username,
		ConnectedAt:    row.ConnectedAt.Time,
		AutoPost:       row.AutoPost,
		NeedsReconnect: row.NeedsReconnect,
		Timezone:       row.Timezone,
	}
}
