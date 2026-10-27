//go:build integration

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/softsrv/starter/internal/db"
	"github.com/softsrv/starter/internal/realtime"
)

// 2000m, seeded alongside roomTestID (500m) by the catalog migration.
var roomTest2000mID = uuid.MustParse("01950000-0000-7000-8000-000000000003")

// roomIntegrationFriends records a friendship between a and b.
func roomIntegrationFriends(t *testing.T, pool *pgxpool.Pool, a, b uuid.UUID) {
	t.Helper()
	a, b = canonicalFriendPair(a, b)
	if err := db.New(pool).CreateFriendship(context.Background(), db.CreateFriendshipParams{ID: uuid.Must(uuid.NewV7()), UserIDA: a, UserIDB: b}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM friendships WHERE user_id_a = $1 AND user_id_b = $2", a, b); err != nil {
			t.Error(err)
		}
	})
}

// The host starts once every guest is ready, without readying up themselves.
// Changing the race clears every ready flag; guests can withdraw readiness.
func TestRoomIntegrationLobbyReadinessAndRaceType(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, guest := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	roomID, code := roomIntegrationCreate(t, pool, host)
	s := NewRoomService(db.New(pool), pool)
	if _, err := s.JoinRoom(ctx, guest, code); err != nil {
		t.Fatal(err)
	}

	lobby, err := s.Lobby(ctx, host, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if !lobby.IsHost || lobby.CanStart || lobby.JoinCode != code || lobby.RaceLabel != "500m" || len(lobby.Participants) != 2 {
		t.Fatalf("host lobby = %+v", lobby)
	}
	if err := s.MarkReady(ctx, guest, roomID); err != nil {
		t.Fatal(err)
	}
	if lobby, _ = s.Lobby(ctx, host, roomID); !lobby.CanStart {
		t.Fatalf("host lobby with every guest ready cannot start: %+v", lobby)
	}
	if lobby, _ = s.Lobby(ctx, guest, roomID); lobby.IsHost || lobby.CanStart || !lobby.SelfReady {
		t.Fatalf("guest lobby = %+v", lobby)
	}

	if err := s.ChangeRaceType(ctx, guest, roomID, roomTest2000mID); !errors.Is(err, ErrNotHost) {
		t.Fatalf("guest race type change = %v", err)
	}
	if err := s.ChangeRaceType(ctx, host, roomID, uuid.MustParse("01950000-0000-7000-8000-000000000006")); !errors.Is(err, ErrInvalidRaceType) {
		t.Fatalf("timed race type = %v, want ErrInvalidRaceType", err)
	}
	if err := s.ChangeRaceType(ctx, host, roomID, roomTest2000mID); err != nil {
		t.Fatal(err)
	}
	if lobby, _ = s.Lobby(ctx, host, roomID); lobby.RaceLabel != "2000m" || lobby.CanStart {
		t.Fatalf("lobby after race change = %+v; want 2000m and guests to ready up again", lobby)
	}
	if err := s.StartRace(ctx, host, roomID); !errors.Is(err, ErrNotAllReady) {
		t.Fatalf("start after race change = %v", err)
	}

	if err := s.MarkReady(ctx, guest, roomID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUnready(ctx, guest, roomID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRace(ctx, host, roomID); !errors.Is(err, ErrNotAllReady) {
		t.Fatalf("start after guest withdrew = %v", err)
	}
	if err := s.MarkReady(ctx, guest, roomID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRace(ctx, host, roomID); err != nil {
		t.Fatalf("start with host not ready = %v", err)
	}
	if err := s.ChangeRaceType(ctx, host, roomID, roomTestID); !errors.Is(err, ErrRoomNotJoinable) {
		t.Fatalf("race type change after start = %v", err)
	}
	if _, err := s.Lobby(ctx, roomIntegrationUser(t, pool), roomID); !errors.Is(err, ErrParticipantNotFound) {
		t.Fatalf("outsider lobby = %v", err)
	}
	options, err := s.RaceTypeOptions(ctx)
	if err != nil || len(options) != 5 || options[0].Label != "500m" || options[4].Label != "10000m" {
		t.Fatalf("race type options = %+v, %v; want the five distance races by length", options, err)
	}
}

// Participants invite friends; invitees see the invite until they act on it or
// the room stops waiting, and accepting joins the room.
func TestRoomIntegrationInvites(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	host, friend, stranger, other := roomIntegrationUser(t, pool), roomIntegrationUser(t, pool), roomIntegrationUser(t, pool), roomIntegrationUser(t, pool)
	roomIntegrationFriends(t, pool, host, friend)
	roomIntegrationFriends(t, pool, host, other)
	roomID, _ := roomIntegrationCreate(t, pool, host)
	s := NewRoomService(db.New(pool), pool)

	if err := s.InviteFriend(ctx, host, roomID, stranger); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invite non-friend = %v", err)
	}
	if err := s.InviteFriend(ctx, stranger, roomID, host); !errors.Is(err, ErrParticipantNotFound) {
		t.Fatalf("invite from outside the room = %v", err)
	}
	for i := 0; i < 2; i++ { // inviting twice is a no-op
		if err := s.InviteFriend(ctx, host, roomID, friend); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InviteFriend(ctx, host, roomID, other); err != nil {
		t.Fatal(err)
	}
	lobby, err := s.Lobby(ctx, host, roomID)
	if err != nil || !lobby.InvitedIDs[friend] || !lobby.InvitedIDs[other] || len(lobby.InvitedIDs) != 2 {
		t.Fatalf("invited = %v, %v", lobby.InvitedIDs, err)
	}

	invites, err := s.PendingInvites(ctx, friend)
	if err != nil || len(invites) != 1 || invites[0].RoomID != roomID || invites[0].RaceLabel != "500m" || invites[0].Inviter.UserID != host || invites[0].Inviter.Name == "" {
		t.Fatalf("pending invites = %+v, %v", invites, err)
	}
	if _, err := s.AcceptInvite(ctx, stranger, invites[0].ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("accept someone else's invite = %v", err)
	}
	joined, err := s.AcceptInvite(ctx, friend, invites[0].ID)
	if err != nil || joined != roomID {
		t.Fatalf("accept = %s, %v", joined, err)
	}
	if lobby, err = s.Lobby(ctx, friend, roomID); err != nil || len(lobby.Participants) != 2 {
		t.Fatalf("friend lobby after accept = %+v, %v", lobby, err)
	}
	if invites, _ = s.PendingInvites(ctx, friend); len(invites) != 0 {
		t.Fatalf("invites after accept = %+v", invites)
	}
	if err := s.InviteFriend(ctx, host, roomID, friend); !errors.Is(err, ErrAlreadyParticipant) {
		t.Fatalf("invite a participant = %v", err)
	}

	otherInvites, err := s.PendingInvites(ctx, other)
	if err != nil || len(otherInvites) != 1 {
		t.Fatalf("other invites = %+v, %v", otherInvites, err)
	}
	if err := s.MarkReady(ctx, friend, roomID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRace(ctx, host, roomID); err != nil {
		t.Fatal(err)
	}
	if invites, _ = s.PendingInvites(ctx, other); len(invites) != 0 {
		t.Fatalf("invites to a started room = %+v", invites)
	}
	if _, err := s.AcceptInvite(ctx, other, otherInvites[0].ID); !errors.Is(err, ErrRoomNotJoinable) {
		t.Fatalf("accept invite to a started room = %v", err)
	}
	if err := s.DeclineInvite(ctx, other, otherInvites[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeclineInvite(ctx, other, otherInvites[0].ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("decline twice = %v", err)
	}
}

// roomIntegrationStartedRoom starts a room with a host and n ready guests.
func roomIntegrationStartedRoom(t *testing.T, pool *pgxpool.Pool, guests int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	roomID, host, guest := roomIntegrationReadyRoom(t, pool)
	users := []uuid.UUID{host, guest}
	s := NewRoomService(db.New(pool), pool)
	room, err := db.New(pool).GetRoomForUpdate(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < guests; i++ {
		user := roomIntegrationUser(t, pool)
		if _, err := s.JoinRoom(ctx, user, room.JoinCode); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkReady(ctx, user, roomID); err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
	}
	if err := s.StartRace(ctx, host, roomID); err != nil {
		t.Fatal(err)
	}
	// Registered after the extra users, so it runs before they are deleted.
	t.Cleanup(func() {
		for _, sql := range []string{
			"DELETE FROM race_participants WHERE race_id IN (SELECT id FROM races WHERE room_id = $1)",
			"DELETE FROM races WHERE room_id = $1",
		} {
			if _, err := pool.Exec(context.Background(), sql, roomID); err != nil {
				t.Error(err)
			}
		}
	})
	return roomID, users
}

// Begin waits for equipment, then drops racers who never connected, hands the
// room to a connected racer if the host was dropped, and starts the race at
// the scheduled time with only the connected racers.
func TestRaceIntegrationBeginDropsRacersWithoutEquipment(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	roomID, users := roomIntegrationStartedRoom(t, pool, 3)
	host, connected, late, absent := users[0], users[1], users[2], users[3]
	q := db.New(pool)

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var sleeps []time.Duration
	svc := NewRaceService(q, pool, realtime.NewFake(), RaceServiceConfig{
		EquipmentTimeout: time.Second, EquipmentPollInterval: 250 * time.Millisecond, CountdownDuration: 10 * time.Second,
		Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			now = now.Add(d)
			if len(sleeps) == 2 {
				// One racer connects while Begin is still waiting.
				if err := roomIntegrationRaceService(q, pool).MarkEquipmentReady(ctx, roomID, late); err != nil {
					t.Error(err)
				}
			}
			return nil
		},
	})
	if err := svc.MarkEquipmentReady(ctx, roomID, connected); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkEquipmentReady(ctx, roomID, connected); err != nil {
		t.Fatalf("repeat equipment ready = %v", err)
	}
	view, err := svc.RoomRace(ctx, roomID, host)
	if err != nil || view.RaceStartsAt != nil || len(view.Racers) != 4 || !view.Racers[1].EquipmentReady || view.Racers[0].EquipmentReady {
		t.Fatalf("view while connecting = %+v, %v", view, err)
	}

	race, err := svc.Begin(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	// Four polls exhaust the one-second wait, then the ten-second countdown.
	want := []time.Duration{250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond, 10 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	}
	scheduled := time.Date(2026, 10, 26, 12, 0, 11, 0, time.UTC)
	if !race.StartedAt.Time.Equal(scheduled) {
		t.Fatalf("race started at %v, want the scheduled %v", race.StartedAt.Time, scheduled)
	}
	room, err := q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.Status != "in_progress" || !room.RaceStartsAt.Time.Equal(scheduled) {
		t.Fatalf("room = %+v, %v", room, err)
	}
	if room.HostUserID != connected {
		t.Fatalf("host = %s, want the first connected racer %s after the host was dropped", room.HostUserID, connected)
	}
	rows, err := pool.Query(ctx, "SELECT user_id FROM race_participants WHERE race_id = $1 ORDER BY id", race.ID)
	if err != nil {
		t.Fatal(err)
	}
	var racers []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		racers = append(racers, id)
	}
	if len(racers) != 2 || racers[0] != connected || racers[1] != late {
		t.Fatalf("racers = %v, want only the connected %s and %s", racers, connected, late)
	}
	for _, dropped := range []uuid.UUID{host, absent} {
		if _, err := svc.RoomRace(ctx, roomID, dropped); !errors.Is(err, ErrParticipantNotFound) {
			t.Fatalf("dropped racer's view = %v, want ErrParticipantNotFound", err)
		}
		if err := svc.MarkEquipmentReady(ctx, roomID, dropped); !errors.Is(err, ErrParticipantNotFound) {
			t.Fatalf("dropped racer's equipment ready = %v", err)
		}
	}
}

// Begin stops waiting as soon as every racer is connected.
func TestRaceIntegrationBeginStartsWhenEveryoneIsReady(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	roomID, users := roomIntegrationStartedRoom(t, pool, 1)
	q := db.New(pool)
	var sleeps []time.Duration
	svc := NewRaceService(q, pool, realtime.NewFake(), RaceServiceConfig{
		CountdownDuration: 10 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			if len(sleeps) == 1 {
				if err := roomIntegrationRaceService(q, pool).MarkEquipmentReady(ctx, roomID, users[1]); err != nil {
					t.Error(err)
				}
			}
			return nil
		},
	})
	if err := svc.MarkEquipmentReady(ctx, roomID, users[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Begin(ctx, roomID); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) != 2 || sleeps[0] != 500*time.Millisecond || sleeps[1] != 10*time.Second {
		t.Fatalf("sleeps = %v, want one default poll then the countdown", sleeps)
	}
}

// A room where nobody connected is dissolved instead of raced.
func TestRaceIntegrationBeginWithNobodyReadyDissolvesRoom(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	roomID, _ := roomIntegrationStartedRoom(t, pool, 1)
	q := db.New(pool)
	if _, err := roomIntegrationRaceService(q, pool).Begin(ctx, roomID); !errors.Is(err, ErrNoRacersReady) {
		t.Fatalf("begin with nobody ready = %v", err)
	}
	room, err := q.GetRoomForUpdate(ctx, roomID)
	if err != nil || room.Status != "dissolved" {
		t.Fatalf("room = %+v, %v; want dissolved", room, err)
	}
	var races int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM races WHERE room_id = $1", roomID).Scan(&races); err != nil || races != 0 {
		t.Fatalf("races = %d, %v", races, err)
	}
}

// Equipment can only be reported for a started room the caller is in.
func TestRaceIntegrationMarkEquipmentReadyGuards(t *testing.T) {
	pool := roomIntegrationDB(t)
	ctx := context.Background()
	roomID, host, _ := roomIntegrationReadyRoom(t, pool)
	svc := roomIntegrationRaceService(db.New(pool), pool)
	if err := svc.MarkEquipmentReady(ctx, roomID, host); !errors.Is(err, ErrRaceNotStarted) {
		t.Fatalf("equipment ready in the lobby = %v", err)
	}
	if err := svc.MarkEquipmentReady(ctx, roomID, roomIntegrationUser(t, pool)); !errors.Is(err, ErrParticipantNotFound) {
		t.Fatalf("outsider equipment ready = %v", err)
	}
	if err := svc.MarkEquipmentReady(ctx, uuid.Must(uuid.NewV7()), host); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("missing room equipment ready = %v", err)
	}
}
