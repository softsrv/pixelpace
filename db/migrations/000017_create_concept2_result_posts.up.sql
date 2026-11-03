-- One row per (user, race) result to post to that user's Concept2 Logbook.
-- Finalizing a race enqueues a pending row for each finisher whose connection
-- has auto_post on; the unique (user_id, race_id) key makes the enqueue
-- idempotent (ON CONFLICT DO NOTHING), so re-finalizing or resuming a race
-- never adds a second row. The poster claims due pending rows, POSTs them and
-- marks them posted, or records the error and retries until max attempts.
-- Deleting the user deletes their queued posts.
-- A new table only: no existing row is read or changed.
CREATE TABLE IF NOT EXISTS concept2_result_posts (
    id              UUID        PRIMARY KEY,
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    race_id         UUID        NOT NULL REFERENCES races(id) ON DELETE CASCADE,
    status          TEXT        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'posted', 'failed')),
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT,
    c2_result_id    BIGINT,
    posted_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, race_id)
);

CREATE INDEX IF NOT EXISTS concept2_result_posts_due_idx
    ON concept2_result_posts (next_attempt_at) WHERE status = 'pending';
