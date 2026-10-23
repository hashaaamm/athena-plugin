-- The accounts table, and the root of this project's history.
--
-- Do not delete or renumber it to "start clean". A migration that has run against a database
-- somewhere is part of that database's history; removing it from the repository leaves the
-- database's goose_db_version pointing at a version the code no longer contains.
--
-- This file is also sqlc's schema (sqlc.yaml reads migrations/), so the generated row types and the
-- table cannot disagree. Every index and constraint is named explicitly, because a name Postgres
-- invents (`users_pkey`) is one no file in this repository contains, and a Down section — or the
-- next migration that alters the constraint — needs it. The migration test asserts each name.

-- +goose Up
-- A blocked ALTER queues behind a long read *and* blocks every query behind it. Bounding the wait
-- turns that from an outage into a failed migration you can retry. LOCAL, because goose runs each
-- migration in its own transaction and the setting must not outlive it on a pooled connection.
SET LOCAL lock_timeout = '3s';

CREATE TABLE users (
    -- Generated server-side, so a row inserted by psql or a later migration gets an id too.
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- Stored already trimmed and lower-cased by the handler, so a plain unique index is enough. 320
    -- is the RFC's maximum length for an address.
    email varchar(320) NOT NULL,
    -- An Argon2id digest in PHC format is about 97 characters; 255 leaves room for a parameter
    -- change. 60 is the *bcrypt* number, and a varchar(60) would truncate the digest silently.
    hashed_password varchar(255) NOT NULL,
    -- A flag, not a deletion: whatever this service grows next will reference user rows.
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pk_users PRIMARY KEY (id)
);

-- A unique *index*, not a UNIQUE constraint: one object enforcing uniqueness and serving lookups.
CREATE UNIQUE INDEX ix_users_email ON users (email);

-- +goose Down
SET LOCAL lock_timeout = '3s';
DROP INDEX ix_users_email;
DROP TABLE users;
