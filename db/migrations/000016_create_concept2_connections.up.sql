-- One Concept2 Logbook connection per PixelPace user. user_id is the primary
-- key, so a user has at most one connection and every query is keyed by it.
-- Deleting the user deletes the connection (and the tokens it holds).
-- Tokens are stored sealed (encrypted by the app), never in plaintext.
-- A new table only: no existing row is read or changed.
CREATE TABLE IF NOT EXISTS concept2_connections (
    user_id           UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    c2_user_id        BIGINT      NOT NULL,
    c2_username       TEXT        NOT NULL,
    access_token_enc  BYTEA       NOT NULL,
    refresh_token_enc BYTEA       NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    scope             TEXT        NOT NULL DEFAULT '',
    auto_post         BOOLEAN     NOT NULL DEFAULT true,
    needs_reconnect   BOOLEAN     NOT NULL DEFAULT false,
    timezone          TEXT        NOT NULL DEFAULT 'UTC',
    connected_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
