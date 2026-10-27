package repository

import (
	"context"
	"fmt"

	"{{ cookiecutter.go_module }}/internal/db"
)

// HealthRepository is readiness' one query.
type HealthRepository struct {
	q *db.Queries
}

// Ping returns nil when the database answered a trivial query.
func (r HealthRepository) Ping(ctx context.Context) error {
	ok, err := r.q.Ping(ctx)
	if err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if ok != 1 {
		return fmt.Errorf("ping database: unexpected answer %d", ok)
	}
	return nil
}
