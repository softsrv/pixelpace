-- sqlc schema input only: never applied to a database (it is not under
-- db/migrations/, so neither `migrate` nor CI's migration step reads it).
--
-- Migration 000021 keeps the pre-rename tables readable through compatibility
-- views (races, race_types) while old and new binaries overlap. sqlc reads
-- db/migrations/ in order, then this file, so dropping the views here keeps
-- generated models and methods off them: internal/db has only Activity,
-- Preset and PresetSegment, and a query naming an old table fails to
-- generate. Delete this file in the contract migration that drops the views.
DROP VIEW IF EXISTS races;
DROP VIEW IF EXISTS race_types;
