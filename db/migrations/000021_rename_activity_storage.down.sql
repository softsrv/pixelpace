-- Reverses 000021: drops the sync triggers, the compatibility views and the
-- twin columns, then reverses every rename. The triggers kept each old column
-- equal to its twin, so dropping the twins loses no value, including rows the
-- renamed binary wrote. No row is inserted, updated or deleted.

DROP TRIGGER trg_sync_quick_match_queue_preset_column ON quick_match_queue;
DROP FUNCTION sync_quick_match_queue_preset_column();

DROP TRIGGER trg_sync_rooms_activity_columns ON rooms;
DROP FUNCTION sync_rooms_activity_columns();

-- Drops idx_quick_match_queue_waiting_preset, quick_match_queue_preset_id_fkey
-- and quick_match_queue_user_id_preset_id_key with the column.
ALTER TABLE quick_match_queue DROP COLUMN preset_id;

-- Drops rooms_activity_type_check, rooms_activity_type_warmup_duration_check
-- and rooms_preset_id_fkey with the columns.
ALTER TABLE rooms
    DROP COLUMN activity_type,
    DROP COLUMN preset_id;

DROP VIEW races;
DROP VIEW race_types;

ALTER TABLE activities RENAME COLUMN activity_type TO mode;
ALTER TABLE activities RENAME COLUMN preset_id TO race_type_id;
ALTER TABLE activities RENAME TO races;

ALTER TABLE preset_segments RENAME COLUMN preset_id TO race_type_id;
ALTER TABLE preset_segments RENAME TO race_type_segments;

ALTER TABLE presets RENAME TO race_types;
