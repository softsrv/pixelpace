-- name: BeginRaceRoom :one
UPDATE rooms SET status = 'in_progress'
WHERE id = $1 AND status = 'counting_down'
RETURNING *;

-- name: CreateRace :one
INSERT INTO races (id, room_id, race_type_id, started_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListRaceRoomParticipants :many
SELECT user_id FROM room_participants WHERE room_id = $1 ORDER BY id;

-- name: CreateRaceParticipant :exec
INSERT INTO race_participants (id, race_id, user_id, status)
VALUES ($1, $2, $3, 'racing');

-- name: LockRace :one
SELECT r.*, rt.kind, rt.target_value
FROM races r JOIN race_types rt ON rt.id = r.race_type_id
WHERE r.id = $1 FOR UPDATE OF r;

-- name: InsertRaceTelemetry :exec
INSERT INTO telemetry_samples
(id, race_id, user_id, elapsed_milliseconds, distance_millimeters, stroke_rate, power, sampled_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

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
