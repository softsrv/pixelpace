CREATE TABLE IF NOT EXISTS users (
    id                    UUID        PRIMARY KEY,
    email                 TEXT        NOT NULL UNIQUE,
    password_hash         TEXT        NOT NULL,
    email_verified        BOOLEAN     NOT NULL DEFAULT false,
    failed_login_attempts INTEGER     NOT NULL DEFAULT 0,
    locked_until          TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);
