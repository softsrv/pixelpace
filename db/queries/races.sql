-- name: BeginRaceRoom :one
UPDATE rooms SET status = 'in_progress'
WHERE id = $1 AND status = 'counting_down'
RETURNING *;

-- mode owns the race's kind; time_trial is kept for older readers and is
-- always derived from it, never passed separately.
-- name: CreateRace :one
INSERT INTO races (id, room_id, race_type_id, started_at, mode, time_trial)
VALUES (@id, @room_id, @race_type_id, @started_at, @mode::text, @mode::text = 'time_trial')
RETURNING *;

-- name: ListRaceRoomParticipants :many
SELECT user_id FROM room_participants WHERE room_id = $1 ORDER BY id;

-- name: CreateRaceParticipant :exec
INSERT INTO race_participants (id, race_id, user_id, status)
VALUES ($1, $2, $3, 'racing');

-- name: SetRaceParticipantMonitor :execrows
-- Records the PM5 a participant rows on, only while they are still racing:
-- once finished or DNF the row's monitor details are fixed.
UPDATE race_participants SET
    pm5_serial_number     = sqlc.narg(serial_number),
    pm5_firmware_version  = sqlc.narg(firmware_version),
    pm5_hardware_revision = sqlc.narg(hardware_revision)
WHERE race_id = sqlc.arg(race_id) AND user_id = sqlc.arg(user_id) AND status = 'racing';

-- name: ListRaceRoster :many
SELECT u.id, u.display_name
FROM race_participants p JOIN users u ON u.id = p.user_id
WHERE p.race_id = $1 ORDER BY p.id;

-- name: LockRace :one
SELECT r.*, rt.kind, rt.target_value, ro.duration_seconds
FROM races r
JOIN race_types rt ON rt.id = r.race_type_id
JOIN rooms ro ON ro.id = r.room_id
WHERE r.id = $1 FOR UPDATE OF r;

-- name: IsRaceParticipant :one
SELECT EXISTS (SELECT 1 FROM race_participants WHERE race_id = $1 AND user_id = $2);

-- name: InsertRaceTelemetry :exec
INSERT INTO telemetry_samples
(id, race_id, user_id, elapsed_milliseconds, distance_millimeters, stroke_rate, power, sampled_at, stroke_count, calories, heart_rate, drag_factor)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ListRaceStandings :many
SELECT p.id, p.user_id, p.status,
       latest.distance_millimeters, latest.elapsed_milliseconds, latest.sampled_at,
       target.elapsed_milliseconds AS target_elapsed_milliseconds,
       target.sampled_at AS target_sampled_at
FROM race_participants p
LEFT JOIN LATERAL (
    SELECT s.distance_millimeters, s.elapsed_milliseconds, s.sampled_at
    FROM telemetry_samples s WHERE s.race_id = p.race_id AND s.user_id = p.user_id
    ORDER BY s.sampled_at DESC, s.id DESC LIMIT 1
) latest ON true
LEFT JOIN LATERAL (
    SELECT s.elapsed_milliseconds, s.sampled_at
    FROM telemetry_samples s
    WHERE s.race_id = p.race_id AND s.user_id = p.user_id
      AND s.distance_millimeters >= $2 AND s.elapsed_milliseconds IS NOT NULL
    ORDER BY s.elapsed_milliseconds ASC, s.sampled_at ASC, s.id ASC LIMIT 1
) target ON true
WHERE p.race_id = $1 ORDER BY p.id;

-- name: MarkRaceParticipantDNF :exec
UPDATE race_participants SET status = 'dnf' WHERE id = $1 AND status = 'racing';

-- name: FinishRaceParticipant :exec
UPDATE race_participants
SET status = 'finished', elapsed_milliseconds = $2, finished_at = $3
WHERE id = $1 AND status <> 'dnf';

-- name: FinishRace :exec
UPDATE races SET finished_at = $2 WHERE id = $1 AND finished_at IS NULL;

-- name: FinishRaceRoom :exec
UPDATE rooms SET status = 'finished' WHERE id = $1 AND status = 'in_progress';

-- Begun races not yet finalized whose room is still racing, for resuming Run.
-- name: ListUnfinishedRaces :many
SELECT r.id
FROM races r JOIN rooms ro ON ro.id = r.room_id
WHERE r.finished_at IS NULL AND ro.status = 'in_progress'
ORDER BY r.id;

-- Every participant with their public name, recorded finish and latest sample.
-- name: ListRaceResults :many
SELECT p.user_id, u.display_name, p.status,
       p.elapsed_milliseconds AS finish_elapsed_milliseconds,
       r.started_at,
       latest.distance_millimeters, latest.sampled_at
FROM race_participants p
JOIN users u ON u.id = p.user_id
JOIN races r ON r.id = p.race_id
LEFT JOIN LATERAL (
    SELECT s.distance_millimeters, s.sampled_at
    FROM telemetry_samples s WHERE s.race_id = p.race_id AND s.user_id = p.user_id
    ORDER BY s.sampled_at DESC, s.id DESC LIMIT 1
) latest ON true
WHERE p.race_id = $1 ORDER BY p.id;

-- name: GetRoomRace :one
SELECT r.status, r.race_starts_at, r.race_type_id, rt.kind, rt.target_value, rt.label, races.id AS race_id, r.mode, r.duration_seconds, r.host_user_id
FROM rooms r
JOIN race_types rt ON rt.id = r.race_type_id
LEFT JOIN races ON races.room_id = r.id
WHERE r.id = $1;

-- Lanes follow the same order Begin seeds race participants in.
-- name: ListRoomLineup :many
SELECT u.id, u.display_name, (rp.equipment_ready_at IS NOT NULL)::boolean AS equipment_ready
FROM room_participants rp JOIN users u ON u.id = rp.user_id
WHERE rp.room_id = $1
ORDER BY rp.id;

-- name: MarkRoomEquipmentReady :execrows
UPDATE room_participants rp
SET equipment_ready_at = COALESCE(rp.equipment_ready_at, NOW())
FROM rooms r
WHERE r.id = rp.room_id AND rp.room_id = $1 AND rp.user_id = $2
  AND r.status IN ('counting_down', 'in_progress');

-- name: CountRoomEquipmentUnready :one
SELECT count(*) FROM room_participants WHERE room_id = $1 AND equipment_ready_at IS NULL;

-- name: DeleteRoomEquipmentUnready :many
DELETE FROM room_participants WHERE room_id = $1 AND equipment_ready_at IS NULL
RETURNING user_id;

-- name: SetRoomRaceStartsAt :exec
UPDATE rooms SET race_starts_at = $2 WHERE id = $1;
