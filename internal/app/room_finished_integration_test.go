//go:build integration

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/realtime"
)

// TestRoomIntegrationFinishedRoomReleasesPlayers tests:
// Once a room is finished, its former participants can create a room and join another waiting room.
// The finished room's join code returns ErrRoomNotFound and can be reused, and invites into it drop off PendingInvites.
func TestRoomIntegrationFinishedRoomReleasesPlayers(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()

	host := roomIntegrationUser(t, pool)
	guest := roomIntegrationUser(t, pool)
	invitee := roomIntegrationUser(t, pool)
	outsider := roomIntegrationUser(t, pool)

	roomIntegrationFriends(t, pool, host, invitee)

	roomSvc := NewRoomService(db.New(pool), pool)

	// Host creates room 1
	room1ID, code1 := roomIntegrationCreate(t, pool, host)

	// Guest joins room 1
	if _, err := roomSvc.JoinRoom(ctx, guest, code1); err != nil {
		t.Fatalf("guest join room 1: %v", err)
	}

	// Host invites friend (invitee)
	if err := roomSvc.InviteFriend(ctx, host, room1ID, invitee); err != nil {
		t.Fatalf("invite friend: %v", err)
	}

	// Verify invitee sees the pending invite
	invites, err := roomSvc.PendingInvites(ctx, invitee)
	if err != nil || len(invites) != 1 {
		t.Fatalf("pending invites before race: len=%d, err=%v", len(invites), err)
	}

	// Host and guest mark equipment ready
	if err := roomSvc.MarkReady(ctx, guest, room1ID); err != nil {
		t.Fatalf("guest ready: %v", err)
	}
	if err := roomSvc.StartRace(ctx, host, room1ID); err != nil {
		t.Fatalf("start race: %v", err)
	}

	// Set equipment ready in room_participants so RaceService.Begin proceeds without delay
	for _, u := range []uuid.UUID{host, guest} {
		if _, err := pool.Exec(ctx, "UPDATE room_participants SET equipment_ready_at = NOW() WHERE room_id = $1 AND user_id = $2", room1ID, u); err != nil {
			t.Fatalf("set equipment ready: %v", err)
		}
	}

	raceSvc := NewRaceService(db.New(pool), pool, realtime.NewFake(), RaceServiceConfig{
		CountdownDuration:  time.Millisecond,
		StalenessThreshold: time.Minute,
		Sleep: func(_ context.Context, _ time.Duration) error {
			return nil
		},
	})

	race, err := raceSvc.Begin(ctx, room1ID)
	if err != nil {
		t.Fatalf("race begin: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM telemetry_samples WHERE race_id = $1", race.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM race_participants WHERE race_id = $1", race.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM races WHERE id = $1", race.ID)
	})

	// Finalize the race (sets races.finished_at and rooms.status='finished')
	if _, err := raceSvc.Finalize(ctx, race.ID); err != nil {
		t.Fatalf("finalize race: %v", err)
	}

	// Verify room is finished
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM rooms WHERE id = $1", room1ID).Scan(&status); err != nil {
		t.Fatalf("get room status: %v", err)
	}
	if status != "finished" {
		t.Fatalf("room status after finalize = %q, want 'finished'", status)
	}

	// Case (a): Former host can create a new room
	room2ID, code2, err := roomSvc.CreateRoom(ctx, host, roomTestID)
	if err != nil {
		t.Fatalf("former host create room: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM rooms WHERE id = $1", room2ID)
	})

	// Case (b): Former guest can join another waiting room
	if _, err := roomSvc.JoinRoom(ctx, guest, code2); err != nil {
		t.Fatalf("former guest join new waiting room: %v", err)
	}

	// Case (c): The finished room's join code returns ErrRoomNotFound
	if _, err := roomSvc.JoinRoom(ctx, outsider, code1); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("join finished room: got %v, want ErrRoomNotFound", err)
	}

	// Case (d): The finished room's join code can be reused
	var codeReuseAttempts int
	roomSvcCustom := &RoomService{
		q:    db.New(pool),
		pool: pool,
		newJoinCode: func() (string, error) {
			codeReuseAttempts++
			return code1, nil
		},
	}
	room3ID, reusedCode, err := roomSvcCustom.CreateRoom(ctx, outsider, roomTestID)
	if err != nil {
		t.Fatalf("create room reusing code: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM rooms WHERE id = $1", room3ID)
	})
	if reusedCode != code1 {
		t.Fatalf("reused code = %q, want %q", reusedCode, code1)
	}

	// Case (e): Invites into the finished room drop off PendingInvites
	invitesAfter, err := roomSvc.PendingInvites(ctx, invitee)
	if err != nil {
		t.Fatalf("pending invites after finish: %v", err)
	}
	for _, inv := range invitesAfter {
		if inv.RoomID == room1ID {
			t.Fatalf("pending invites still includes invite for finished room %s", room1ID)
		}
	}
}
