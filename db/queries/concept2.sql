-- Every query here is keyed by the PixelPace user's id (user_id), so acting
-- for one user can never read or change another user's connection.

-- name: UpsertConcept2Connection :one
-- Connect or reconnect: a reconnect overwrites the stored tokens, account and
-- timezone and clears needs_reconnect. auto_post and connected_at are kept.
INSERT INTO concept2_connections (
    user_id, c2_user_id, c2_username, access_token_enc, refresh_token_enc,
    expires_at, scope, timezone, connected_at, updated_at
)
VALUES (
    sqlc.arg(user_id), sqlc.arg(c2_user_id), sqlc.arg(c2_username),
    sqlc.arg(access_token_enc), sqlc.arg(refresh_token_enc),
    sqlc.arg(expires_at), sqlc.arg(scope), sqlc.arg(timezone), NOW(), NOW()
)
ON CONFLICT (user_id) DO UPDATE SET
    c2_user_id        = EXCLUDED.c2_user_id,
    c2_username       = EXCLUDED.c2_username,
    access_token_enc  = EXCLUDED.access_token_enc,
    refresh_token_enc = EXCLUDED.refresh_token_enc,
    expires_at        = EXCLUDED.expires_at,
    scope             = EXCLUDED.scope,
    timezone          = EXCLUDED.timezone,
    needs_reconnect   = false,
    updated_at        = NOW()
RETURNING *;

-- name: GetConcept2Connection :one
SELECT * FROM concept2_connections WHERE user_id = sqlc.arg(user_id) LIMIT 1;

-- name: DeleteConcept2Connection :execrows
-- Disconnect: deletes the connection and, in the same statement, the user's
-- pending result posts, so a later reconnect (possibly to another Concept2
-- account) never sends races finished before it. Posted and failed rows are
-- kept: results already in the Logbook are left alone.
WITH cancelled AS (
    DELETE FROM concept2_result_posts p
    WHERE p.user_id = sqlc.arg(user_id) AND p.status = 'pending'
)
DELETE FROM concept2_connections c WHERE c.user_id = sqlc.arg(user_id);

-- name: UpdateConcept2Tokens :execrows
UPDATE concept2_connections SET
    access_token_enc  = sqlc.arg(access_token_enc),
    refresh_token_enc = sqlc.arg(refresh_token_enc),
    expires_at        = sqlc.arg(expires_at),
    needs_reconnect   = false,
    updated_at        = NOW()
WHERE user_id = sqlc.arg(user_id);

-- name: MarkConcept2NeedsReconnect :execrows
UPDATE concept2_connections SET needs_reconnect = true, updated_at = NOW()
WHERE user_id = sqlc.arg(user_id);

-- name: SetConcept2AutoPost :one
-- Switching auto-post off also deletes the user's pending result posts in the
-- same statement, so switching it back on never sends races finished while it
-- was off (no back-posting). Switching it on deletes nothing.
WITH cancelled AS (
    DELETE FROM concept2_result_posts p
    WHERE p.user_id = sqlc.arg(user_id) AND p.status = 'pending'
      AND NOT sqlc.arg(auto_post)::boolean
)
UPDATE concept2_connections c
SET auto_post = sqlc.arg(auto_post), updated_at = NOW()
WHERE c.user_id = sqlc.arg(user_id)
RETURNING *;

-- ── Result posts ─────────────────────────────────────────────────────────────
-- Every query below is keyed by the post's user_id as well as its id.

-- name: ListConcept2PostCandidates :many
-- The race's finished participants whose connection has auto_post on.
-- Unconnected and DNF participants are never listed. A connection that needs
-- reconnecting is still listed: its post waits, unclaimed, until the user
-- reconnects (ClaimDueConcept2Posts holds it), so no finish is lost.
SELECT rp.user_id
FROM race_participants rp
JOIN races r ON r.id = rp.race_id
JOIN concept2_connections c ON c.user_id = rp.user_id
WHERE rp.race_id = sqlc.arg(race_id)
  AND r.mode <> 'warmup'
  AND rp.status = 'finished'
  AND c.auto_post
ORDER BY rp.user_id;

-- name: EnqueueConcept2ResultPost :execrows
-- Idempotent: a (user, race) pair already queued is left as it is.
INSERT INTO concept2_result_posts (id, user_id, race_id)
VALUES (sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(race_id))
ON CONFLICT (user_id, race_id) DO NOTHING;

-- name: ClaimDueConcept2Posts :many
-- Leases up to max_posts due pending posts: each claimed row is counted as an
-- attempt and not due again until lease_until, so concurrent posters (SKIP
-- LOCKED) never claim the same row, and a poster that stops mid-post leaves
-- the row to be retried once the lease ends. Only posts of a connection that
-- has auto_post on now and does not need reconnecting are claimed: a post
-- queued before the user switched auto-post off is not sent, and posts of a
-- connection that needs reconnecting wait, unclaimed and with attempts
-- unchanged, until the user reconnects. A post whose user has disconnected
-- is still claimed, so the poster can settle it.
UPDATE concept2_result_posts SET
    attempts        = attempts + 1,
    next_attempt_at = sqlc.arg(lease_until)::timestamptz,
    updated_at      = NOW()
WHERE id IN (
    SELECT d.id FROM concept2_result_posts d
    WHERE d.status = 'pending' AND d.next_attempt_at <= sqlc.arg(now)::timestamptz
      AND NOT EXISTS (
          SELECT 1 FROM concept2_connections c
          WHERE c.user_id = d.user_id AND (c.needs_reconnect OR NOT c.auto_post)
      )
    ORDER BY d.next_attempt_at, d.id
    LIMIT sqlc.arg(max_posts)::int
    FOR UPDATE SKIP LOCKED
)
RETURNING id, user_id, race_id, attempts;

-- name: Concept2PostStillSendable :one
-- Asked immediately before each send: the claimed post is still pending (not
-- cancelled by a disconnect or by auto-post being switched off since it was
-- claimed) and its connection still has auto_post on and needs no reconnect.
SELECT EXISTS (
    SELECT 1
    FROM concept2_result_posts p
    JOIN concept2_connections c ON c.user_id = p.user_id
    WHERE p.id = sqlc.arg(id) AND p.user_id = sqlc.arg(user_id)
      AND p.status = 'pending' AND c.auto_post AND NOT c.needs_reconnect
)::boolean AS sendable;

-- name: GetConcept2PostPayload :one
-- What one post sends: the participant's recorded finish and PM5 details, the
-- race target and the user's connection timezone. No row when the user has
-- since disconnected. The tokens are read with GetConcept2Connection, the
-- strokes with ListConcept2PostStrokes.
SELECT p.id, p.user_id, p.race_id,
       rp.elapsed_milliseconds, rp.finished_at,
       rp.pm5_serial_number, rp.pm5_firmware_version, rp.pm5_hardware_revision,
       rt.kind, rt.target_value,
       c.timezone
FROM concept2_result_posts p
JOIN race_participants rp ON rp.race_id = p.race_id AND rp.user_id = p.user_id AND rp.status = 'finished'
JOIN races r ON r.id = p.race_id
JOIN race_types rt ON rt.id = r.race_type_id
JOIN concept2_connections c ON c.user_id = p.user_id
WHERE p.id = sqlc.arg(id) AND p.user_id = sqlc.arg(user_id);

-- name: ListConcept2PostStrokes :many
-- The post's racer's samples of its race, up to their recorded finish, in
-- rowing order: the stroke data, splits and totals one post sends.
SELECT s.elapsed_milliseconds, s.distance_millimeters, s.stroke_rate, s.power,
       s.stroke_count, s.calories, s.heart_rate, s.drag_factor
FROM concept2_result_posts p
JOIN race_participants rp ON rp.race_id = p.race_id AND rp.user_id = p.user_id
JOIN telemetry_samples s ON s.race_id = p.race_id AND s.user_id = p.user_id
WHERE p.id = sqlc.arg(id) AND p.user_id = sqlc.arg(user_id)
  AND s.elapsed_milliseconds IS NOT NULL
  AND s.distance_millimeters IS NOT NULL
  AND s.elapsed_milliseconds <= rp.elapsed_milliseconds
ORDER BY s.elapsed_milliseconds, s.sampled_at, s.id;

-- name: MarkConcept2PostPosted :execrows
UPDATE concept2_result_posts SET
    status       = 'posted',
    c2_result_id = sqlc.narg(c2_result_id),
    last_error   = NULL,
    posted_at    = NOW(),
    updated_at   = NOW()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND status = 'pending';

-- name: RecordConcept2PostError :execrows
-- Keeps the post pending for another attempt at retry_at. A transient failure
-- never fails a post: only a permanent rejection does (MarkConcept2PostFailed).
UPDATE concept2_result_posts SET
    last_error      = sqlc.arg(last_error),
    next_attempt_at = sqlc.arg(retry_at)::timestamptz,
    updated_at      = NOW()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND status = 'pending';

-- name: MarkConcept2PostFailed :execrows
-- Fails the post for good: a failed post is never claimed again.
UPDATE concept2_result_posts SET
    status     = 'failed',
    last_error = sqlc.arg(last_error),
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND status = 'pending';

-- name: RequeueConcept2PostForReconnect :execrows
-- Gives back the attempt the claim counted, because nothing was sent: the
-- post waits, pending and due, until the user reconnects (ClaimDue skips it
-- while the connection needs reconnecting).
UPDATE concept2_result_posts SET
    attempts        = GREATEST(attempts - 1, 0),
    next_attempt_at = sqlc.arg(now)::timestamptz,
    last_error      = sqlc.arg(last_error),
    updated_at      = NOW()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND status = 'pending';
