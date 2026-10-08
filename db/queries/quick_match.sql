-- name: EnqueueQuickMatch :exec
INSERT INTO quick_match_queue (id, user_id, race_type_id)
VALUES ($1, $2, $3);

-- name: LeaveQuickMatch :exec
DELETE FROM quick_match_queue WHERE user_id = $1 AND race_type_id = $2;

-- name: ListWaitingByRaceType :many
SELECT user_id, joined_at,
       EXTRACT(EPOCH FROM (NOW() - joined_at))::double precision AS waited_seconds
FROM quick_match_queue
WHERE race_type_id = $1
ORDER BY joined_at ASC, id ASC
FOR UPDATE;

-- name: RecentFinishesByRaceType :many
SELECT rp.elapsed_milliseconds, rp.finished_at
FROM race_participants AS rp
JOIN races AS r ON r.id = rp.race_id
WHERE r.race_type_id = $1 AND rp.user_id = $2
  AND rp.status = 'finished'
ORDER BY rp.finished_at DESC NULLS LAST, rp.id DESC;
