-- Drop rotation-specific columns; previous_token_id first (it has the FK).
ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS previous_token_id;
ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS token_family;
DROP INDEX IF EXISTS idx_refresh_tokens_token_family;

-- Sweep expired sessions automatically after every INSERT so the DB
-- enforces expiry independently of the application-level check.
CREATE OR REPLACE FUNCTION sweep_expired_refresh_tokens() RETURNS trigger AS $$
BEGIN
    DELETE FROM refresh_tokens WHERE expires_at < NOW();
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sweep_expired_refresh_tokens
    AFTER INSERT ON refresh_tokens
    FOR EACH STATEMENT
    EXECUTE FUNCTION sweep_expired_refresh_tokens();
