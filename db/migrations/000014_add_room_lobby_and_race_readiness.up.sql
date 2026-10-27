-- When every rower's equipment is confirmed (or the wait times out) the room
-- records the instant the race goes. NULL while racers are still connecting.
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS race_starts_at TIMESTAMPTZ;

-- Set when the participant's race page has loaded and their rowing machine is
-- connected. Distinct from the lobby's ready flag, which is a social signal.
ALTER TABLE room_participants ADD COLUMN IF NOT EXISTS equipment_ready_at TIMESTAMPTZ;

-- In-app invitations from a lobby to a friend. One per room and invitee;
-- deleted when accepted or declined, and with the room.
CREATE TABLE IF NOT EXISTS room_invites (
    id              UUID        PRIMARY KEY,
    room_id         UUID        NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    inviter_user_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invitee_user_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (room_id, invitee_user_id)
);

CREATE INDEX IF NOT EXISTS idx_room_invites_invitee ON room_invites (invitee_user_id);
