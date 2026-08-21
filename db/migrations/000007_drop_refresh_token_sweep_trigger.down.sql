-- Recreate the per-INSERT sweep trigger removed in this migration's up step.
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
