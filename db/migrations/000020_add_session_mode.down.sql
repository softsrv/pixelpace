-- Delete any telemetry samples, participants, and Concept2 posts for warmup races
DELETE FROM telemetry_samples
WHERE race_id IN (
    SELECT id FROM races WHERE race_type_id = '01950000-0000-7000-8000-000000000009'::UUID
    UNION
    SELECT r.id FROM races r JOIN rooms rm ON r.room_id = rm.id WHERE rm.race_type_id = '01950000-0000-7000-8000-000000000009'::UUID
);

DELETE FROM race_participants
WHERE race_id IN (
    SELECT id FROM races WHERE race_type_id = '01950000-0000-7000-8000-000000000009'::UUID
    UNION
    SELECT r.id FROM races r JOIN rooms rm ON r.room_id = rm.id WHERE rm.race_type_id = '01950000-0000-7000-8000-000000000009'::UUID
);

DELETE FROM concept2_result_posts
WHERE race_id IN (
    SELECT id FROM races WHERE race_type_id = '01950000-0000-7000-8000-000000000009'::UUID
    UNION
    SELECT r.id FROM races r JOIN rooms rm ON r.room_id = rm.id WHERE rm.race_type_id = '01950000-0000-7000-8000-000000000009'::UUID
);

-- Delete warmup races
DELETE FROM races
WHERE race_type_id = '01950000-0000-7000-8000-000000000009'::UUID
   OR room_id IN (SELECT id FROM rooms WHERE race_type_id = '01950000-0000-7000-8000-000000000009'::UUID);

-- Delete warmup rooms (room_participants and room_invites cascade)
DELETE FROM rooms
WHERE race_type_id = '01950000-0000-7000-8000-000000000009'::UUID;

-- Delete the warmup sentinel race type
DELETE FROM race_types
WHERE id = '01950000-0000-7000-8000-000000000009'::UUID;

-- Drop added columns from races and rooms
ALTER TABLE races DROP COLUMN IF EXISTS mode;
ALTER TABLE rooms DROP COLUMN IF EXISTS duration_seconds CASCADE;
ALTER TABLE rooms DROP COLUMN IF EXISTS mode CASCADE;
