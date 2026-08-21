DROP TRIGGER IF EXISTS trg_sweep_expired_refresh_tokens ON refresh_tokens;
DROP FUNCTION IF EXISTS sweep_expired_refresh_tokens();

-- Re-add rotation columns without NOT NULL (existing rows have no values).
ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS token_family      UUID,
    ADD COLUMN IF NOT EXISTS previous_token_id UUID REFERENCES refresh_tokens(id);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token_family ON refresh_tokens (token_family);
