DROP INDEX IF EXISTS users_display_name_lower_key;
ALTER TABLE users DROP COLUMN IF EXISTS display_name;
