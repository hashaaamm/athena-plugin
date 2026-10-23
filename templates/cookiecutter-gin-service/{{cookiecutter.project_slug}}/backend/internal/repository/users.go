package repository

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/db"
)

// PasswordDigest is an Argon2id digest. It does not print — fmt and slog render "[redacted]" — so a
// `slog.Info("user", "user", u)` or a `%+v` in an error message puts no password hash in a log
// aggregator, where retention is measured in years and the access control is not the one this table
// was designed for.
type PasswordDigest string

func (d PasswordDigest) String() string { return "[redacted]" }

// GoString covers %#v, which would otherwise bypass String.
func (d PasswordDigest) GoString() string { return `"[redacted]"` }

// LogValue covers slog, which does not call String on its own.
func (d PasswordDigest) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// User is a person who can sign in.
//
// The digest is the only representation of the password anywhere in the system — not encrypted,
// not reversible. There is deliberately no last_login_at: a column written on every login turns the
// hottest path in the service into a row-locking write. It belongs in an audit event.
type User struct {
	ID             uuid.UUID
	Email          string
	HashedPassword PasswordDigest
	IsActive       bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func userFromRow(row db.User) User {
	return User{
		ID:             row.ID,
		Email:          row.Email,
		HashedPassword: PasswordDigest(row.HashedPassword),
		IsActive:       row.IsActive,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

// UserRepository is all users data access.
type UserRepository struct {
	q *db.Queries
}

// Get returns the user with this id, or ErrNotFound.
func (r UserRepository) Get(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := r.q.GetUser(ctx, id)
	if err != nil {
		return User{}, notFound(err, "get user")
	}
	return userFromRow(row), nil
}

// GetByEmail returns the user with this email, or ErrNotFound. The email must already be
// normalised; the unique index is on the stored, lower-cased value.
func (r UserRepository) GetByEmail(ctx context.Context, email string) (User, error) {
	row, err := r.q.GetUserByEmail(ctx, email)
	if err != nil {
		return User{}, notFound(err, "get user by email")
	}
	return userFromRow(row), nil
}

// Create inserts a user. A taken email is ErrDuplicate — which is how two registrations racing for
// one address end: the index decides, and the loser gets a conflict rather than a 500.
func (r UserRepository) Create(ctx context.Context, email string, digest PasswordDigest) (User, error) {
	row, err := r.q.CreateUser(ctx, db.CreateUserParams{Email: email, HashedPassword: string(digest)})
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrDuplicate
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return userFromRow(row), nil
}

// SetPasswordHash replaces the stored digest.
func (r UserRepository) SetPasswordHash(ctx context.Context, id uuid.UUID, digest PasswordDigest) error {
	if err := r.q.SetUserPasswordHash(ctx, db.SetUserPasswordHashParams{ID: id, HashedPassword: string(digest)}); err != nil {
		return fmt.Errorf("set password hash: %w", err)
	}
	return nil
}
