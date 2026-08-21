CREATE TABLE IF NOT EXISTS email_verification_codes (
    id         UUID        PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code       TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_evc_user_id    ON email_verification_codes (user_id);
CREATE INDEX IF NOT EXISTS idx_evc_expires_at ON email_verification_codes (expires_at);
