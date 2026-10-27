// Package repository is all data access. It wraps the sqlc-generated queries in internal/db, maps
// their rows to its own types, and owns the one decision about transactions.
//
// It is the only package allowed to import internal/db or pgx (depguard in .golangci.yml), so SQL
// cannot leak upwards: a service sees User and RefreshSession, never a row, a pgx type or a query.
// Nothing here decides anything about the domain — a method whose name contains "and", or a
// business term, belongs in the service.
//
// Ask Athena for the layered architecture rules and the database access rules.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"{{ cookiecutter.go_module }}/internal/apperr"
	"{{ cookiecutter.go_module }}/internal/db"
)

// ErrNotFound is a query for one row that found none. Services translate it into whatever the
// absence means to them — a 401 for a stale token, a 404 for a resource.
var ErrNotFound = errors.New("repository: not found")

// ErrDuplicate is a write that violated a unique index.
var ErrDuplicate = errors.New("repository: duplicate")

// DB is what a Store runs on: the pgx pool in the service, a transaction in the test suite.
//
// Both *pgxpool.Pool and pgx.Tx satisfy it, and that is the point. Begin on a pool opens a
// transaction; Begin on a transaction opens a SAVEPOINT. So the test harness hands the store a
// transaction it rolls back at the end, every WithTx inside the code under test becomes a savepoint
// within it, and the real commit-or-rollback logic runs unmodified.
type DB interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store is the entry point to every repository.
type Store struct {
	conn DB
	q    *db.Queries
}

// New builds a Store over a pool or a transaction.
func New(conn DB) *Store {
	return &Store{conn: conn, q: db.New(conn)}
}

// Users is the users repository on this store's connection.
func (s *Store) Users() UserRepository { return UserRepository{q: s.q} }

// RefreshSessions is the refresh_sessions repository on this store's connection.
func (s *Store) RefreshSessions() RefreshSessionRepository { return RefreshSessionRepository{q: s.q} }

// Health is the readiness repository on this store's connection.
func (s *Store) Health() HealthRepository { return HealthRepository{q: s.q} }

// WithTx runs fn in one transaction, and is the only code in the service that decides whether a
// unit of work is committed or rolled back.
//
// A nil error commits. An error rolls back, so a failure late in a use case undoes the writes that
// came earlier in it — except an apperr that declares KeepsWrites, whose writes are the outcome the
// error reports (apperr.CompromisedSession). A failed commit is returned as an error, never
// swallowed: the service returns it, the handler has not written anything yet, and the client gets
// a 500 instead of a 2xx for a write that never happened. That ordering — commit, then respond — is
// why services call WithTx themselves rather than leaving a transaction open across the handler.
//
// fn receives a Store bound to the transaction. Use it, not the outer one: a query on the outer
// store runs on another connection, outside the transaction and outside its locks.
func (s *Store) WithTx(ctx context.Context, fn func(tx *Store) error) error {
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Rollback must still run when the request's context is already cancelled — a client that hung
	// up mid-request — or pgx closes the connection instead of returning it to the pool clean.
	rollback := func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }
	defer func() {
		if recovered := recover(); recovered != nil {
			rollback()
			panic(recovered)
		}
	}()

	if err := fn(&Store{conn: tx, q: db.New(tx)}); err != nil {
		if !apperr.KeepsWrites(err) {
			rollback()
			return err
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			// The writes this error exists to keep did not land. That is a 500, not the refusal
			// the caller was about to get: the defence it reports did not happen.
			return fmt.Errorf("commit writes kept by %q: %w", err.Error(), commitErr)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// notFound maps pgx's "no rows" to ErrNotFound and wraps anything else with what was being done.
func notFound(err error, doing string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// isUniqueViolation reports a unique-index violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
