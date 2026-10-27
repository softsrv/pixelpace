-- name: GetRaceType :one
SELECT * FROM race_types WHERE id = $1;

-- name: InsertRoom :exec
INSERT INTO rooms (id, race_type_id, host_user_id, status, join_code)
VALUES ($1, $2, $3, 'waiting', $4);

-- name: InsertRoomParticipant :exec
INSERT INTO room_participants (id, room_id, user_id, ready)
VALUES ($1, $2, $3, false);

-- name: GetActiveRoomByJoinCode :one
SELECT * FROM rooms
WHERE join_code = $1 AND status NOT IN ('finished', 'dissolved')
FOR UPDATE;

-- name: GetRoomForUpdate :one
SELECT * FROM rooms WHERE id = $1 FOR UPDATE;

-- name: RoomParticipantExists :one
SELECT EXISTS (SELECT 1 FROM room_participants WHERE room_id = $1 AND user_id = $2);

-- name: CountRoomParticipants :one
SELECT count(*) FROM room_participants WHERE room_id = $1;

-- name: GetRoomParticipant :one
SELECT * FROM room_participants WHERE room_id = $1 AND user_id = $2 LIMIT 1;

-- name: DeleteRoomParticipant :exec
DELETE FROM room_participants WHERE room_id = $1 AND user_id = $2;

-- name: GetRemainingRoomParticipant :one
SELECT user_id FROM room_participants WHERE room_id = $1 ORDER BY joined_at, id LIMIT 1;

-- name: ReassignRoomHost :exec
UPDATE rooms SET host_user_id = $2 WHERE id = $1;

-- name: DissolveRoom :exec
UPDATE rooms SET status = 'dissolved' WHERE id = $1;

-- name: MarkRoomParticipantReady :exec
UPDATE room_participants SET ready = true WHERE room_id = $1 AND user_id = $2;

-- name: MarkRoomParticipantUnready :exec
UPDATE room_participants SET ready = false WHERE room_id = $1 AND user_id = $2;

-- name: ResetRoomReadiness :exec
UPDATE room_participants SET ready = false WHERE room_id = $1;

-- The host starts the race, so only the other participants must be ready.
-- name: RoomHasUnreadyGuests :one
SELECT EXISTS (
    SELECT 1 FROM room_participants
    WHERE room_id = $1 AND user_id <> $2 AND NOT ready
);

-- name: StartRoomCountdown :exec
UPDATE rooms SET status = 'counting_down' WHERE id = $1;

-- name: BeginRoomRace :exec
UPDATE rooms SET status = 'in_progress' WHERE id = $1;

-- name: ResetRoomCountdown :exec
UPDATE rooms SET status = 'waiting', race_starts_at = NULL WHERE id = $1 AND status = 'counting_down';

-- name: ResetRoomEquipmentReadiness :exec
UPDATE room_participants SET equipment_ready_at = NULL WHERE room_id = $1;

-- name: ListRoomRoster :many
SELECT u.id, u.display_name, rp.ready
FROM room_participants rp JOIN users u ON u.id = rp.user_id
WHERE rp.room_id = $1
ORDER BY rp.joined_at, rp.id;

-- name: GetRoomRaceID :one
SELECT id FROM races WHERE room_id = $1 LIMIT 1;

-- name: ListDistanceRaceTypes :many
SELECT * FROM race_types WHERE kind = 'distance' ORDER BY target_value;

-- name: UpdateRoomRaceType :exec
UPDATE rooms SET race_type_id = $2 WHERE id = $1;

-- name: GetRoomLobby :one
SELECT r.id, r.status, r.join_code, r.host_user_id, r.race_type_id, rt.label AS race_label
FROM rooms r JOIN race_types rt ON rt.id = r.race_type_id
WHERE r.id = $1;

-- name: InsertRoomInvite :exec
INSERT INTO room_invites (id, room_id, inviter_user_id, invitee_user_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (room_id, invitee_user_id) DO NOTHING;

-- name: ListRoomInviteeIDs :many
SELECT invitee_user_id FROM room_invites WHERE room_id = $1;

-- Invites stay listed only while their room is an open lobby the invitee has
-- not already joined.
-- name: ListPendingRoomInvites :many
SELECT ri.id, ri.room_id, rt.label AS race_label, u.id AS inviter_id, u.display_name AS inviter_display_name
FROM room_invites ri
JOIN rooms r ON r.id = ri.room_id
JOIN race_types rt ON rt.id = r.race_type_id
JOIN users u ON u.id = ri.inviter_user_id
WHERE ri.invitee_user_id = $1
  AND r.status = 'waiting'
  AND NOT EXISTS (
      SELECT 1 FROM room_participants rp
      WHERE rp.room_id = ri.room_id AND rp.user_id = ri.invitee_user_id
  )
ORDER BY ri.created_at DESC, ri.id;

-- name: GetRoomInviteForInvitee :one
SELECT ri.room_id, r.join_code
FROM room_invites ri JOIN rooms r ON r.id = ri.room_id
WHERE ri.id = $1 AND ri.invitee_user_id = $2;

-- name: DeleteRoomInvite :execrows
DELETE FROM room_invites WHERE id = $1 AND invitee_user_id = $2;
