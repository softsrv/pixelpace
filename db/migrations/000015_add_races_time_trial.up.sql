-- A race that begins with a single entrant is a Time Trial. It is recorded at
-- the instant the race begins so its results rank on the Time Trial
-- Leaderboard rather than the Race Leaderboard for the same Race Type.
ALTER TABLE races ADD COLUMN IF NOT EXISTS time_trial BOOLEAN NOT NULL DEFAULT false;

-- Races that already began with one entrant (the others were dropped while
-- their equipment connected) were time trials in all but name.
UPDATE races SET time_trial = true
WHERE (SELECT count(*) FROM race_participants rp WHERE rp.race_id = races.id) = 1;
