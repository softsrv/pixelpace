-- name: CreateFriendRequest :one
INSERT INTO friend_requests (id, sender_id, recipient_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING *;

-- name: GetPendingFriendRequest :one
SELECT * FROM friend_requests
WHERE sender_id = $1 AND recipient_id = $2 AND status = 'pending';

-- name: LatestFriendRequestRejection :one
SELECT MAX(decided_at)::timestamptz AS decided_at FROM friend_requests
WHERE sender_id = $1 AND recipient_id = $2 AND status = 'rejected';

-- name: FriendshipExists :one
SELECT EXISTS (SELECT 1 FROM friendships WHERE user_id_a = $1 AND user_id_b = $2);

-- name: GetFriendRequest :one
SELECT * FROM friend_requests WHERE id = $1;

-- name: DecideFriendRequest :one
UPDATE friend_requests SET status = $2, decided_at = NOW()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: CreateFriendship :exec
INSERT INTO friendships (id, user_id_a, user_id_b) VALUES ($1, $2, $3);

-- name: ListFriends :many
SELECT f.user_id_b AS friend_id FROM friendships AS f WHERE f.user_id_a = $1
UNION
SELECT f.user_id_a AS friend_id FROM friendships AS f WHERE f.user_id_b = $1;

-- name: ListFriendEntries :many
SELECT u.id, u.display_name
FROM (
    SELECT f.user_id_b AS friend_id FROM friendships AS f WHERE f.user_id_a = $1
    UNION
    SELECT f.user_id_a AS friend_id FROM friendships AS f WHERE f.user_id_b = $1
) AS friends
JOIN users AS u ON u.id = friends.friend_id
ORDER BY u.id;

-- name: ListIncomingFriendRequests :many
SELECT fr.id AS request_id, u.id AS user_id, u.display_name
FROM friend_requests AS fr
JOIN users AS u ON u.id = fr.sender_id
WHERE fr.recipient_id = $1 AND fr.status = 'pending'
ORDER BY fr.created_at, fr.id;

-- name: ListOutgoingFriendRequests :many
SELECT fr.id AS request_id, u.id AS user_id, u.display_name
FROM friend_requests AS fr
JOIN users AS u ON u.id = fr.recipient_id
WHERE fr.sender_id = $1 AND fr.status = 'pending'
ORDER BY fr.created_at, fr.id;

