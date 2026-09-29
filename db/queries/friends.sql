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
