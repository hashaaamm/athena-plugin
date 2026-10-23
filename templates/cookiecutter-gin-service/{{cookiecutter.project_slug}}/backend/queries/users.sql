-- name: GetUser :one
SELECT id, email, hashed_password, is_active, created_at, updated_at
FROM users
WHERE id = $1;

-- The email is assumed already normalised (trimmed, lower-cased) — see internal/http/handler.
-- name: GetUserByEmail :one
SELECT id, email, hashed_password, is_active, created_at, updated_at
FROM users
WHERE email = $1;

-- name: CreateUser :one
INSERT INTO users (email, hashed_password)
VALUES ($1, $2)
RETURNING id, email, hashed_password, is_active, created_at, updated_at;

-- updated_at is set here because nothing else will: there is no ORM `onupdate` and no trigger.
-- name: SetUserPasswordHash :exec
UPDATE users
SET hashed_password = $2, updated_at = now()
WHERE id = $1;
