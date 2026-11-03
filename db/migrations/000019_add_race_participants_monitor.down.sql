ALTER TABLE race_participants
    DROP COLUMN IF EXISTS pm5_hardware_revision,
    DROP COLUMN IF EXISTS pm5_firmware_version,
    DROP COLUMN IF EXISTS pm5_serial_number;
