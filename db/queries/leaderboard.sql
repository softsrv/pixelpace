-- name: LeaderboardPersonalBest :many
SELECT rp.user_id, MIN(rp.elapsed_milliseconds)::integer AS elapsed_milliseconds
FROM race_participants AS rp
JOIN races AS r ON r.id = rp.race_id
WHERE r.race_type_id = $1
  AND rp.status = 'finished'
  AND ($2::timestamptz IS NULL OR rp.finished_at >= $2)
  AND ($3::uuid[] IS NULL OR rp.user_id = ANY($3))
GROUP BY rp.user_id
ORDER BY elapsed_milliseconds ASC, rp.user_id ASC;
