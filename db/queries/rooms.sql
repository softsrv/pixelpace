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

-- name: RoomHasUnreadyParticipants :one
SELECT EXISTS (SELECT 1 FROM room_participants WHERE room_id = $1 AND NOT ready);

-- name: StartRoomCountdown :exec
UPDATE rooms SET status = 'counting_down' WHERE id = $1;

-- name: BeginRoomRace :exec
UPDATE rooms SET status = 'in_progress' WHERE id = $1;
