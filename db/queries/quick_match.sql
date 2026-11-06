-- name: EnqueueQuickMatch :exec
-- Writes only preset_id; the 000021 sync trigger fills its old twin column.
INSERT INTO quick_match_queue (id, user_id, preset_id)
VALUES ($1, $2, $3);

-- name: LeaveQuickMatch :exec
DELETE FROM quick_match_queue WHERE user_id = $1 AND preset_id = $2;

-- name: ListWaitingByPreset :many
SELECT user_id, joined_at,
       EXTRACT(EPOCH FROM (NOW() - joined_at))::double precision AS waited_seconds
FROM quick_match_queue
WHERE preset_id = $1
ORDER BY joined_at ASC, id ASC
FOR UPDATE;

-- name: RecentFinishesByPreset :many
SELECT rp.elapsed_milliseconds, rp.finished_at
FROM race_participants AS rp
JOIN activities AS a ON a.id = rp.race_id
WHERE a.preset_id = $1 AND rp.user_id = $2
  AND rp.status = 'finished'
  AND a.activity_type <> 'warmup'
ORDER BY rp.finished_at DESC NULLS LAST, rp.id DESC;
