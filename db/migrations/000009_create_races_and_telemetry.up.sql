CREATE TABLE IF NOT EXISTS races (
    id           UUID        PRIMARY KEY,
    room_id      UUID        NOT NULL REFERENCES rooms(id),
    race_type_id UUID        NOT NULL REFERENCES race_types(id),
    started_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS race_participants (
    id                    UUID        PRIMARY KEY,
    race_id               UUID        NOT NULL REFERENCES races(id),
    user_id               UUID        NOT NULL REFERENCES users(id),
    status                TEXT        NOT NULL CHECK (status IN ('racing','finished','dnf')),
    finished_at           TIMESTAMPTZ,
    elapsed_milliseconds  INTEGER
);

CREATE TABLE IF NOT EXISTS telemetry_samples (
    id                     UUID        PRIMARY KEY,
    race_id                UUID        NOT NULL REFERENCES races(id),
    user_id                UUID        NOT NULL REFERENCES users(id),
    elapsed_milliseconds   BIGINT,
    distance_millimeters   BIGINT,
    stroke_rate            INTEGER,
    power                  INTEGER,
    sampled_at             TIMESTAMPTZ NOT NULL
);
