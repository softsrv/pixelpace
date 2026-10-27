DROP TABLE IF EXISTS room_invites;
ALTER TABLE room_participants DROP COLUMN IF EXISTS equipment_ready_at;
ALTER TABLE rooms DROP COLUMN IF EXISTS race_starts_at;
