package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/softsrv/starter/internal/db"
)

// RaceTypeOption is a race type the lobby offers.
type RaceTypeOption struct {
	ID    uuid.UUID
	Label string
}

// RoomLobby is one participant's view of a room before its race starts.
type RoomLobby struct {
	RoomID       uuid.UUID
	Status       string
	JoinCode     string
	RaceTypeID   uuid.UUID
	RaceLabel    string
	Participants []RoomRosterEntry
	// InvitedIDs are the users already holding an invite to this room.
	InvitedIDs map[uuid.UUID]bool
	IsHost     bool
	SelfReady  bool
	// CanStart reports whether StartRace would accept the host's request now.
	CanStart bool
}

// RoomInvite is a pending invitation as its invitee sees it.
type RoomInvite struct {
	ID        uuid.UUID
	RoomID    uuid.UUID
	RaceLabel string
	Inviter   PublicPlayer
}

// RaceTypeOptions lists the race types a lobby can pick. Only fixed-distance
// races can be raced on the track today.
func (s *RoomService) RaceTypeOptions(ctx context.Context) ([]RaceTypeOption, error) {
	rows, err := s.q.ListDistanceRaceTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list race types: %w", err)
	}
	options := make([]RaceTypeOption, 0, len(rows))
	for _, row := range rows {
		options = append(options, RaceTypeOption{ID: row.ID, Label: row.Label})
	}
	return options, nil
}

// Lobby returns the room as the caller sees it. A caller who is not in the
// room gets ErrParticipantNotFound.
func (s *RoomService) Lobby(ctx context.Context, callerUserID, roomID uuid.UUID) (RoomLobby, error) {
	room, err := s.q.GetRoomLobby(ctx, roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RoomLobby{}, ErrRoomNotFound
		}
		return RoomLobby{}, fmt.Errorf("get room lobby: %w", err)
	}
	rows, err := s.q.ListRoomRoster(ctx, roomID)
	if err != nil {
		return RoomLobby{}, fmt.Errorf("list room roster: %w", err)
	}
	lobby := RoomLobby{
		RoomID: room.ID, Status: room.Status, JoinCode: room.JoinCode,
		RaceTypeID: room.RaceTypeID, RaceLabel: room.RaceLabel,
		IsHost: callerUserID == room.HostUserID, InvitedIDs: map[uuid.UUID]bool{},
	}
	member := false
	guestsReady := true
	for _, row := range rows {
		host := row.ID == room.HostUserID
		lobby.Participants = append(lobby.Participants, RoomRosterEntry{
			PublicPlayer: NewPublicPlayer(row.ID, row.DisplayName), Ready: row.Ready, Host: host,
		})
		if row.ID == callerUserID {
			member = true
			lobby.SelfReady = row.Ready
		}
		if !host && !row.Ready {
			guestsReady = false
		}
	}
	if !member {
		return RoomLobby{}, ErrParticipantNotFound
	}
	count := len(lobby.Participants)
	lobby.CanStart = lobby.IsHost && room.Status == "waiting" && guestsReady && count >= 2 && count <= maxRoomParticipants
	invited, err := s.q.ListRoomInviteeIDs(ctx, roomID)
	if err != nil {
		return RoomLobby{}, fmt.Errorf("list room invites: %w", err)
	}
	for _, id := range invited {
		lobby.InvitedIDs[id] = true
	}
	return lobby, nil
}

// ChangeRaceType lets the host pick another fixed-distance race while the room
// waits. Everyone agreed to the old race, so every ready flag is cleared.
func (s *RoomService) ChangeRaceType(ctx context.Context, callerUserID, roomID, raceTypeID uuid.UUID) error {
	raceType, err := s.q.GetRaceType(ctx, raceTypeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidRaceType
		}
		return fmt.Errorf("get race type: %w", err)
	}
	if raceType.Kind != "distance" {
		return ErrInvalidRaceType
	}
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if callerUserID != room.HostUserID {
			return ErrNotHost
		}
		if room.Status != "waiting" {
			return ErrRoomNotJoinable
		}
		if room.RaceTypeID == raceTypeID {
			return nil
		}
		if err := qtx.UpdateRoomRaceType(ctx, db.UpdateRoomRaceTypeParams{ID: roomID, RaceTypeID: raceTypeID}); err != nil {
			return fmt.Errorf("update race type: %w", err)
		}
		if err := qtx.ResetRoomReadiness(ctx, roomID); err != nil {
			return fmt.Errorf("reset readiness: %w", err)
		}
		return nil
	})
}

// MarkUnready clears only the caller's own ready flag in a waiting room.
func (s *RoomService) MarkUnready(ctx context.Context, userID, roomID uuid.UUID) error {
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if room.Status != "waiting" {
			return ErrRoomNotJoinable
		}
		if _, err := getRoomParticipant(ctx, qtx, roomID, userID); err != nil {
			return err
		}
		if err := qtx.MarkRoomParticipantUnready(ctx, db.MarkRoomParticipantUnreadyParams{RoomID: roomID, UserID: userID}); err != nil {
			return fmt.Errorf("mark participant unready: %w", err)
		}
		return nil
	})
}

// InviteFriend invites one of the inviter's friends to a waiting room the
// inviter is in. Inviting the same friend again is a no-op.
func (s *RoomService) InviteFriend(ctx context.Context, inviterUserID, roomID, inviteeUserID uuid.UUID) error {
	if inviterUserID == inviteeUserID {
		return ErrForbidden
	}
	return s.withLockedRoom(ctx, roomID, func(qtx *db.Queries, room db.Room) error {
		if room.Status != "waiting" {
			return ErrRoomNotJoinable
		}
		if _, err := getRoomParticipant(ctx, qtx, roomID, inviterUserID); err != nil {
			return err
		}
		a, b := canonicalFriendPair(inviterUserID, inviteeUserID)
		friends, err := qtx.FriendshipExists(ctx, db.FriendshipExistsParams{UserIDA: a, UserIDB: b})
		if err != nil {
			return fmt.Errorf("check friendship: %w", err)
		}
		if !friends {
			return ErrForbidden
		}
		joined, err := qtx.RoomParticipantExists(ctx, db.RoomParticipantExistsParams{RoomID: roomID, UserID: inviteeUserID})
		if err != nil {
			return fmt.Errorf("check participant: %w", err)
		}
		if joined {
			return ErrAlreadyParticipant
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate invite id: %w", err)
		}
		if err := qtx.InsertRoomInvite(ctx, db.InsertRoomInviteParams{
			ID: id, RoomID: roomID, InviterUserID: inviterUserID, InviteeUserID: inviteeUserID,
		}); err != nil {
			return fmt.Errorf("insert invite: %w", err)
		}
		return nil
	})
}

// PendingInvites lists the invites a user can still act on, newest first.
func (s *RoomService) PendingInvites(ctx context.Context, userID uuid.UUID) ([]RoomInvite, error) {
	rows, err := s.q.ListPendingRoomInvites(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list room invites: %w", err)
	}
	invites := make([]RoomInvite, 0, len(rows))
	for _, row := range rows {
		invites = append(invites, RoomInvite{
			ID: row.ID, RoomID: row.RoomID, RaceLabel: row.RaceLabel,
			Inviter: NewPublicPlayer(row.InviterID, row.InviterDisplayName),
		})
	}
	return invites, nil
}

// AcceptInvite joins the invitee to the invite's room and consumes the invite.
// It returns the room id; an invitee who already joined is not an error.
func (s *RoomService) AcceptInvite(ctx context.Context, userID, inviteID uuid.UUID) (uuid.UUID, error) {
	invite, err := s.q.GetRoomInviteForInvitee(ctx, db.GetRoomInviteForInviteeParams{ID: inviteID, InviteeUserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrRoomNotFound
		}
		return uuid.Nil, fmt.Errorf("get room invite: %w", err)
	}
	if _, err := s.JoinRoom(ctx, userID, invite.JoinCode); err != nil && !errors.Is(err, ErrAlreadyParticipant) {
		return uuid.Nil, err
	}
	if _, err := s.q.DeleteRoomInvite(ctx, db.DeleteRoomInviteParams{ID: inviteID, InviteeUserID: userID}); err != nil {
		return uuid.Nil, fmt.Errorf("delete room invite: %w", err)
	}
	return invite.RoomID, nil
}

// DeclineInvite discards one of the user's own invites.
func (s *RoomService) DeclineInvite(ctx context.Context, userID, inviteID uuid.UUID) error {
	n, err := s.q.DeleteRoomInvite(ctx, db.DeleteRoomInviteParams{ID: inviteID, InviteeUserID: userID})
	if err != nil {
		return fmt.Errorf("delete room invite: %w", err)
	}
	if n == 0 {
		return ErrRoomNotFound
	}
	return nil
}
