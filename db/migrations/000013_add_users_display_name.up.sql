-- Optional public display name. Nullable with no default: adding it is a
-- metadata-only change in Postgres (no table rewrite), and existing accounts
-- simply have no name. The app built before this migration never selects or
-- writes the column, so applying this ahead of the new binary is safe.
ALTER TABLE users ADD COLUMN IF NOT EXISTS display_name TEXT;

-- Names are unique ignoring case ('Rower' and 'rower' collide). NULLs never
-- collide, so accounts without a name are unaffected. Not CONCURRENTLY:
-- golang-migrate sends the whole file as one multi-statement exec (an implicit
-- transaction), where CONCURRENTLY is rejected; the column is all NULL here,
-- so the build is near-instant.
CREATE UNIQUE INDEX IF NOT EXISTS users_display_name_lower_key ON users (lower(display_name));
