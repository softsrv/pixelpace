ALTER TABLE telemetry_samples
    ADD COLUMN stroke_count INT,
    ADD COLUMN calories     INT,
    ADD COLUMN heart_rate   INT,
    ADD COLUMN drag_factor  INT;
