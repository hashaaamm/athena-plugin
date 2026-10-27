-- name: CreateRefreshSession :exec
INSERT INTO refresh_sessions (id, user_id, family_id, expires_at)
VALUES ($1, $2, $3, $4);

-- The row, locked until the surrounding transaction ends. The lock is what makes reuse detection
-- work: without it two refreshes arriving together both read `revoked_at IS NULL`, both rotate, and
-- the family ends up with two live chains and no way to tell the thief from the owner. With it the
-- second waits, then reads the first one's revocation. FOR UPDATE lives inside the transaction, so
-- it is safe behind pgBouncer's transaction pooling; a session-level advisory lock would not be.
-- name: GetRefreshSessionForUpdate :one
SELECT id, user_id, family_id, expires_at, revoked_at, created_at, updated_at
FROM refresh_sessions
WHERE id = $1
FOR UPDATE;

-- name: RevokeRefreshSession :exec
UPDATE refresh_sessions
SET revoked_at = sqlc.arg(revoked_at), updated_at = now()
WHERE id = sqlc.arg(id);

-- Every live token descended from one sign-in. Already-revoked rows keep their timestamp, which is
-- the record of when each one was actually spent.
-- name: RevokeRefreshSessionFamily :exec
UPDATE refresh_sessions
SET revoked_at = sqlc.arg(revoked_at), updated_at = now()
WHERE family_id = sqlc.arg(family_id) AND revoked_at IS NULL;

-- Every family the user has, on every device.
-- name: RevokeUserRefreshSessions :exec
UPDATE refresh_sessions
SET revoked_at = sqlc.arg(revoked_at), updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND revoked_at IS NULL;
