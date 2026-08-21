-- The per-INSERT sweep trigger added in 000005 ran a full predicate DELETE on
-- refresh_tokens after every login/refresh, amplifying writes on the hot path.
-- Expired/stale token removal is owned by the daily background job
-- (app.RunTokenCleanup), so the trigger is redundant. Drop it.
DROP TRIGGER IF EXISTS trg_sweep_expired_refresh_tokens ON refresh_tokens;
DROP FUNCTION IF EXISTS sweep_expired_refresh_tokens();
