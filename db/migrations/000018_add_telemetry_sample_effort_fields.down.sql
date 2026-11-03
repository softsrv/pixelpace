ALTER TABLE telemetry_samples
    DROP COLUMN IF EXISTS drag_factor,
    DROP COLUMN IF EXISTS heart_rate,
    DROP COLUMN IF EXISTS calories,
    DROP COLUMN IF EXISTS stroke_count;
