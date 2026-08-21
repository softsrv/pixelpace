CREATE TABLE IF NOT EXISTS refresh_tokens (
    id                UUID        PRIMARY KEY,
    user_id           UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash        TEXT        NOT NULL,
    token_family      UUID        NOT NULL,
    previous_token_id UUID        REFERENCES refresh_tokens(id),
    device_name       TEXT,
    ip_address        INET,
    user_agent        TEXT,
    expires_at        TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token_hash   ON refresh_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token_family ON refresh_tokens (token_family);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id      ON refresh_tokens (user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires_at   ON refresh_tokens (expires_at);
