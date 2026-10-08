CREATE TABLE IF NOT EXISTS quick_match_queue (
    id           UUID        PRIMARY KEY,
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    race_type_id UUID        NOT NULL REFERENCES race_types(id),
    joined_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, race_type_id)
);

CREATE INDEX IF NOT EXISTS idx_quick_match_queue_waiting ON quick_match_queue (race_type_id, joined_at);
