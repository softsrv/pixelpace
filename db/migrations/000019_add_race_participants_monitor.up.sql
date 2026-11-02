-- The PM5 a participant rowed on, as the browser read it over Bluetooth once
-- after connecting (best effort). Each is NULL when it was not read. The
-- Concept2 poster sends the non-null ones as the result's monitor metadata.
-- Nullable columns only: no existing row is rewritten.
ALTER TABLE race_participants
    ADD COLUMN IF NOT EXISTS pm5_serial_number     TEXT,
    ADD COLUMN IF NOT EXISTS pm5_firmware_version  TEXT,
    ADD COLUMN IF NOT EXISTS pm5_hardware_revision TEXT;
