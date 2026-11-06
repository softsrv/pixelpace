ALTER TABLE rooms
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'race' CHECK (mode IN ('race', 'time_trial', 'warmup')),
    ADD COLUMN IF NOT EXISTS duration_seconds INTEGER,
    ADD CHECK ((mode = 'warmup') = (duration_seconds IS NOT NULL)),
    ADD CHECK (duration_seconds IS NULL OR (duration_seconds BETWEEN 60 AND 1200 AND duration_seconds % 60 = 0));

ALTER TABLE races
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'race' CHECK (mode IN ('race', 'time_trial', 'warmup'));

UPDATE races SET mode = 'time_trial' WHERE time_trial = true;

UPDATE rooms SET mode = 'time_trial'
WHERE id IN (SELECT room_id FROM races WHERE time_trial = true);

INSERT INTO race_types (id, kind, target_value, label) VALUES
    ('01950000-0000-7000-8000-000000000009'::UUID, 'time', 0, 'Warmup')
ON CONFLICT DO NOTHING;
