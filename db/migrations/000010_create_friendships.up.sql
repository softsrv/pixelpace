CREATE TABLE IF NOT EXISTS friendships (
    id         UUID        PRIMARY KEY,
    user_id_a  UUID        NOT NULL REFERENCES users(id),
    user_id_b  UUID        NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (user_id_a < user_id_b)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_friendships_pair ON friendships (user_id_a, user_id_b);
