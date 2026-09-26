CREATE TABLE IF NOT EXISTS friend_requests (
    id           UUID        PRIMARY KEY,
    sender_id    UUID        NOT NULL REFERENCES users(id),
    recipient_id UUID        NOT NULL REFERENCES users(id),
    status       TEXT        NOT NULL CHECK (status IN ('pending','accepted','rejected')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    decided_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_friend_requests_pending ON friend_requests (sender_id, recipient_id) WHERE status = 'pending';
