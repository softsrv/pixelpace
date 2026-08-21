-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, email_verified, created_at, updated_at)
VALUES ($1, $2, $3, false, NOW(), NOW())
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 LIMIT 1;

-- name: SetEmailVerified :exec
UPDATE users SET email_verified = true, updated_at = NOW() WHERE id = $1;

-- name: IncrementFailedLoginAttempts :exec
UPDATE users SET failed_login_attempts = failed_login_attempts + 1, updated_at = NOW() WHERE id = $1;

-- name: LockAccount :exec
UPDATE users SET locked_until = $2, updated_at = NOW() WHERE id = $1;

-- name: ResetLoginAttempts :exec
UPDATE users SET failed_login_attempts = 0, locked_until = NULL, updated_at = NOW() WHERE id = $1;

-- name: UpdatePasswordHash :exec
UPDATE users SET password_hash = $2, failed_login_attempts = 0, locked_until = NULL, updated_at = NOW() WHERE id = $1;

-- name: DeleteUserByID :exec
DELETE FROM users WHERE id = $1;
