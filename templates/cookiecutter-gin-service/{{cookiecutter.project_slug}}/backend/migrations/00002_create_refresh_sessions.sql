-- One row per refresh token issued, keyed by the token's `jti`. The token itself is never stored.
--
-- A JWT is valid because it verifies, not because anything says it still should be. That is what
-- makes the access token cheap, and it is what would make a thirty-day refresh token a thirty-day
-- credential nobody could take back. This table buys revocation back for the refresh token only, at
-- one round trip per refresh. A dump of it yields session identifiers and no credentials.
--
-- Revocation is per family: every token descended from one sign-in shares a family_id, and a revoked
-- row presented again revokes the family (internal/service/auth.go, Refresh).
--
-- Rows are never deleted on the request path. A device refreshing every fifteen minutes writes about
-- 2,900 rows a month, so a project in production wants a scheduled job deleting rows some days past
-- expires_at. Nothing in the template runs one.

-- +goose Up
-- The foreign key takes a lock on `users`, which is the table a bounded wait matters for.
SET LOCAL lock_timeout = '3s';

CREATE TABLE refresh_sessions (
    -- The refresh token's own `jti`. No default: an id this table invented would name no token.
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    -- Shared by every token descended from one sign-in.
    family_id uuid NOT NULL,
    -- The token's `exp`, checked here as well as in the token, so the row is the authority:
    -- shortening a session is an UPDATE, not a re-issue.
    expires_at timestamp with time zone NOT NULL,
    -- Set when the token is rotated, logged out or revoked with its family. NULL means spendable.
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pk_refresh_sessions PRIMARY KEY (id),
    -- CASCADE, so deleting a user takes their sessions with them rather than failing on them.
    CONSTRAINT fk_refresh_sessions_user_id_users FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

-- Plain, not CONCURRENTLY: the table was created in this same transaction, so nothing else can be
-- reading or writing it, and CREATE INDEX CONCURRENTLY cannot run inside a transaction at all. An
-- index added later to a table with traffic is the case that needs it, in a migration goose is told
-- to run outside a transaction (its NO TRANSACTION annotation).
--
-- family_id: revoking a family is an UPDATE by this column, on the path where a stolen token was
-- just detected. user_id: changing a password revokes every family by it, and the foreign key's
-- ON DELETE CASCADE scans by it.
CREATE INDEX ix_refresh_sessions_family_id ON refresh_sessions (family_id);
CREATE INDEX ix_refresh_sessions_user_id ON refresh_sessions (user_id);

-- +goose Down
-- Every refresh token in circulation stops working, because the rows that vouch for them are gone.
-- Access tokens are unaffected and expire on their own; users sign in again.
SET LOCAL lock_timeout = '3s';
DROP INDEX ix_refresh_sessions_user_id;
DROP INDEX ix_refresh_sessions_family_id;
DROP TABLE refresh_sessions;
