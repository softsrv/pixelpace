-- name: LeaderboardPersonalBest :many
SELECT rp.user_id, u.display_name, MIN(rp.elapsed_milliseconds)::integer AS elapsed_milliseconds
FROM race_participants AS rp
JOIN activities AS a ON a.id = rp.race_id
JOIN users AS u ON u.id = rp.user_id
WHERE a.preset_id = $1
  AND a.activity_type = $4
  AND rp.status = 'finished'
  AND ($2::timestamptz IS NULL OR rp.finished_at >= $2)
  AND ($3::uuid[] IS NULL OR rp.user_id = ANY($3))
GROUP BY rp.user_id, u.display_name
ORDER BY elapsed_milliseconds ASC, rp.user_id ASC;

-- The racer's own fastest finished Time Trial at a preset, from activities that
-- started and were finished by the racer before the given instant (the minimum
-- elapsed time). No row when there is none. A Time Trial's pacer rows at this
-- time; bounding on the finish keeps it fixed for the whole race.
-- name: TimeTrialPersonalBest :one
SELECT rp.elapsed_milliseconds
FROM race_participants AS rp
JOIN activities AS a ON a.id = rp.race_id
WHERE a.preset_id = @preset_id
  AND rp.user_id = @user_id
  AND a.activity_type = 'time_trial'
  AND rp.status = 'finished'
  AND rp.elapsed_milliseconds IS NOT NULL
  AND a.started_at < @before::timestamptz
  AND rp.finished_at < @before::timestamptz
ORDER BY rp.elapsed_milliseconds ASC
LIMIT 1;
