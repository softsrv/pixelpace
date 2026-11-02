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
DELETE FROM concept2_connections WHERE user_id = sqlc.arg(user_id);

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
