CREATE TABLE IF NOT EXISTS race_types (
    id           UUID        PRIMARY KEY,
    kind         TEXT        NOT NULL CHECK (kind IN ('distance','time','interval')),
    target_value BIGINT      NOT NULL,
    label        TEXT        NOT NULL
);

CREATE TABLE IF NOT EXISTS race_type_segments (
    id           UUID        PRIMARY KEY,
    race_type_id UUID        NOT NULL REFERENCES race_types(id) ON DELETE CASCADE,
    position     INTEGER     NOT NULL,
    target_value BIGINT      NOT NULL
);

CREATE TABLE IF NOT EXISTS rooms (
    id           UUID        PRIMARY KEY,
    race_type_id UUID        NOT NULL REFERENCES race_types(id),
    host_user_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status       TEXT        NOT NULL CHECK (status IN ('waiting','counting_down','in_progress','finished','dissolved')),
    join_code    TEXT        NOT NULL CHECK (join_code ~ '^[A-HJ-KMNP-Z2-9]{6}$'),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_rooms_join_code_active ON rooms (join_code) WHERE status NOT IN ('finished','dissolved');

CREATE TABLE IF NOT EXISTS room_participants (
    id        UUID        PRIMARY KEY,
    room_id   UUID        NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    user_id   UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ready     BOOLEAN     NOT NULL DEFAULT false,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO race_types (id, kind, target_value, label) VALUES
    ('01950000-0000-7000-8000-000000000001'::UUID, 'distance', 500000, '500m'),
    ('01950000-0000-7000-8000-000000000002'::UUID, 'distance', 1000000, '1000m'),
    ('01950000-0000-7000-8000-000000000003'::UUID, 'distance', 2000000, '2000m'),
    ('01950000-0000-7000-8000-000000000004'::UUID, 'distance', 5000000, '5000m'),
    ('01950000-0000-7000-8000-000000000005'::UUID, 'distance', 10000000, '10000m'),
    ('01950000-0000-7000-8000-000000000006'::UUID, 'time', 300000, '5 min'),
    ('01950000-0000-7000-8000-000000000007'::UUID, 'time', 600000, '10 min'),
    ('01950000-0000-7000-8000-000000000008'::UUID, 'time', 1200000, '20 min');
