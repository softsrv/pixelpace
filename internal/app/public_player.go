package app

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// PublicPlayer is the one view used to describe a player to another player.
// It deliberately has no email field: nothing private can ride along.
type PublicPlayer struct {
	UserID uuid.UUID `json:"user_id"`
	Name   string    `json:"name"`
}

// NewPublicPlayer returns the player's name when they have one. Otherwise the
// name is a deterministic placeholder, "Racer " plus a digest of every byte of
// the id (UUIDv7 leads with a timestamp, so a prefix would collide). The space
// means a placeholder can never pass users.ValidateDisplayName, so no player
// can claim one as a real name.
func NewPublicPlayer(id uuid.UUID, name pgtype.Text) PublicPlayer {
	if name.Valid && name.String != "" {
		return PublicPlayer{UserID: id, Name: name.String}
	}
	sum := sha256.Sum256(id[:])
	return PublicPlayer{UserID: id, Name: "Racer " + hex.EncodeToString(sum[:5])}
}
