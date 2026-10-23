package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/db"
)

// RefreshSession is one refresh token this service issued, and whether it may still be spent. The
// token itself is never stored, only its `jti` (ID).
type RefreshSession struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	ExpiresAt time.Time
	// Nil means spendable.
	RevokedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewRefreshSession is what a fresh row records.
type NewRefreshSession struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	ExpiresAt time.Time
}

// RefreshSessionRepository is all refresh_sessions data access: rows and locks, nothing else.
type RefreshSessionRepository struct {
	q *db.Queries
}

// Create records a newly issued refresh token.
func (r RefreshSessionRepository) Create(ctx context.Context, s NewRefreshSession) error {
	err := r.q.CreateRefreshSession(ctx, db.CreateRefreshSessionParams{
		ID:        s.ID,
		UserID:    s.UserID,
		FamilyID:  s.FamilyID,
		ExpiresAt: s.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("create refresh session: %w", err)
	}
	return nil
}

// GetForUpdate returns the row and holds its lock until the surrounding transaction ends, or
// ErrNotFound. Only meaningful inside Store.WithTx: outside one, the lock is released the moment the
// statement finishes, and reuse detection silently stops working.
func (r RefreshSessionRepository) GetForUpdate(ctx context.Context, id uuid.UUID) (RefreshSession, error) {
	row, err := r.q.GetRefreshSessionForUpdate(ctx, id)
	if err != nil {
		return RefreshSession{}, notFound(err, "get refresh session for update")
	}
	return RefreshSession{
		ID:        row.ID,
		UserID:    row.UserID,
		FamilyID:  row.FamilyID,
		ExpiresAt: row.ExpiresAt,
		RevokedAt: row.RevokedAt,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// Revoke marks one token spent.
func (r RefreshSessionRepository) Revoke(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := r.q.RevokeRefreshSession(ctx, db.RevokeRefreshSessionParams{ID: id, RevokedAt: &at}); err != nil {
		return fmt.Errorf("revoke refresh session: %w", err)
	}
	return nil
}

// RevokeFamily revokes every live token descended from one sign-in.
func (r RefreshSessionRepository) RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error {
	if err := r.q.RevokeRefreshSessionFamily(ctx, db.RevokeRefreshSessionFamilyParams{FamilyID: familyID, RevokedAt: &at}); err != nil {
		return fmt.Errorf("revoke refresh session family: %w", err)
	}
	return nil
}

// RevokeAllForUser revokes every family the user has, on every device.
func (r RefreshSessionRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID, at time.Time) error {
	if err := r.q.RevokeUserRefreshSessions(ctx, db.RevokeUserRefreshSessionsParams{UserID: userID, RevokedAt: &at}); err != nil {
		return fmt.Errorf("revoke user refresh sessions: %w", err)
	}
	return nil
}
