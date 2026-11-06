-- Expand step of the storage rename: race_types -> presets,
-- race_type_segments -> preset_segments (race_type_id -> preset_id) and
-- races -> activities (race_type_id -> preset_id, mode -> activity_type).
-- rooms and quick_match_queue get twin columns activity_type / preset_id next
-- to mode / race_type_id.
--
-- Both the pre-rename and the renamed app binary must work while this is
-- applied, so:
--   * views races and race_types keep the old names readable and writable
--     (simple views are auto-updatable);
--   * a BEFORE INSERT OR UPDATE trigger on rooms and quick_match_queue keeps
--     each old column and its new twin equal, whichever side a writer sets.
-- A later contract migration drops the views, the triggers and the old columns
-- once only the renamed binary runs.
--
-- No stored value changes: there are only renames, column adds, the
-- new = old copies below, the sync triggers and the views. Activity-type
-- values stay 'race', 'time_trial' and 'warmup'; presets.kind keeps its name.
-- Renamed tables keep their constraint and index names, so down restores
-- the 000020 schema exactly.

ALTER TABLE race_types RENAME TO presets;

ALTER TABLE race_type_segments RENAME TO preset_segments;
ALTER TABLE preset_segments RENAME COLUMN race_type_id TO preset_id;

ALTER TABLE races RENAME TO activities;
ALTER TABLE activities RENAME COLUMN race_type_id TO preset_id;
ALTER TABLE activities RENAME COLUMN mode TO activity_type;

-- Compatibility views for the pre-rename binary, with its column names and
-- order. Omitted columns on insert take the base table's defaults.
CREATE VIEW race_types AS
    SELECT id, kind, target_value, label FROM presets;

CREATE VIEW races AS
    SELECT id, room_id, preset_id AS race_type_id, started_at, finished_at, time_trial, activity_type AS mode
    FROM activities;

-- rooms: twins of mode and race_type_id with the same constraints. The twins
-- have no default: the trigger fills a twin the writer left NULL from the
-- other column (mode's own default 'race' still applies to old-style inserts).
ALTER TABLE rooms
    ADD COLUMN activity_type TEXT
        CONSTRAINT rooms_activity_type_check CHECK (activity_type IN ('race', 'time_trial', 'warmup')),
    ADD COLUMN preset_id UUID
        CONSTRAINT rooms_preset_id_fkey REFERENCES presets(id),
    ADD CONSTRAINT rooms_activity_type_warmup_duration_check
        CHECK ((activity_type = 'warmup') = (duration_seconds IS NOT NULL));

UPDATE rooms SET activity_type = mode, preset_id = race_type_id;

ALTER TABLE rooms
    ALTER COLUMN activity_type SET NOT NULL,
    ALTER COLUMN preset_id SET NOT NULL;

-- quick_match_queue: twin of race_type_id with the same FK, uniqueness and
-- waiting-list index.
ALTER TABLE quick_match_queue
    ADD COLUMN preset_id UUID
        CONSTRAINT quick_match_queue_preset_id_fkey REFERENCES presets(id);

UPDATE quick_match_queue SET preset_id = race_type_id;

ALTER TABLE quick_match_queue
    ALTER COLUMN preset_id SET NOT NULL,
    ADD CONSTRAINT quick_match_queue_user_id_preset_id_key UNIQUE (user_id, preset_id);

CREATE INDEX idx_quick_match_queue_waiting_preset ON quick_match_queue (preset_id, joined_at);

-- Sync triggers. On INSERT a non-NULL new column wins (only the renamed binary
-- sets it), otherwise it is copied from the old one. On UPDATE whichever side
-- changed is copied to its twin; if both changed, the new column wins.
CREATE FUNCTION sync_rooms_activity_columns() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.activity_type IS NULL THEN
            NEW.activity_type := NEW.mode;
        ELSE
            NEW.mode := NEW.activity_type;
        END IF;
        IF NEW.preset_id IS NULL THEN
            NEW.preset_id := NEW.race_type_id;
        ELSE
            NEW.race_type_id := NEW.preset_id;
        END IF;
    ELSE
        IF NEW.activity_type IS DISTINCT FROM OLD.activity_type THEN
            NEW.mode := NEW.activity_type;
        ELSIF NEW.mode IS DISTINCT FROM OLD.mode THEN
            NEW.activity_type := NEW.mode;
        END IF;
        IF NEW.preset_id IS DISTINCT FROM OLD.preset_id THEN
            NEW.race_type_id := NEW.preset_id;
        ELSIF NEW.race_type_id IS DISTINCT FROM OLD.race_type_id THEN
            NEW.preset_id := NEW.race_type_id;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sync_rooms_activity_columns
    BEFORE INSERT OR UPDATE ON rooms
    FOR EACH ROW EXECUTE FUNCTION sync_rooms_activity_columns();

CREATE FUNCTION sync_quick_match_queue_preset_column() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.preset_id IS NULL THEN
            NEW.preset_id := NEW.race_type_id;
        ELSE
            NEW.race_type_id := NEW.preset_id;
        END IF;
    ELSE
        IF NEW.preset_id IS DISTINCT FROM OLD.preset_id THEN
            NEW.race_type_id := NEW.preset_id;
        ELSIF NEW.race_type_id IS DISTINCT FROM OLD.race_type_id THEN
            NEW.preset_id := NEW.race_type_id;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sync_quick_match_queue_preset_column
    BEFORE INSERT OR UPDATE ON quick_match_queue
    FOR EACH ROW EXECUTE FUNCTION sync_quick_match_queue_preset_column();
