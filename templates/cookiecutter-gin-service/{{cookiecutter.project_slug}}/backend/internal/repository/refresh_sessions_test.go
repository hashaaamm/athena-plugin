package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"{{ cookiecutter.go_module }}/internal/repository"
	"{{ cookiecutter.go_module }}/internal/testdb"
)

// The row lock that reuse detection stands on.
//
// Without FOR UPDATE, two refreshes arriving together both read `revoked_at IS NULL`, both rotate,
// and the family ends up with two live chains — and no reliable reuse detection at all. Nothing in a
// single-connection test can see that, so this one uses two connections of its own, commits the rows
// it needs, and deletes them again.
func TestARowReadForUpdateIsHeldUntilItsTransactionEnds(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool()
	store := repository.New(pool)

	user, err := store.Users().Create(ctx, email(), "digest")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The sessions go with the user: ON DELETE CASCADE.
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
	})
	jti := uuid.New()
	err = store.RefreshSessions().Create(ctx, repository.NewRefreshSession{
		ID: jti, UserID: user.ID, FamilyID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := repository.New(holder).RefreshSessions().GetForUpdate(ctx, jti); err != nil {
		t.Fatal(err)
	}

	contender, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Bounded, so the test fails in a tenth of a second instead of hanging for ever.
	if _, err := contender.Exec(ctx, "SET LOCAL lock_timeout = '100ms'"); err != nil {
		t.Fatal(err)
	}
	_, err = repository.New(contender).RefreshSessions().GetForUpdate(ctx, jti)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("a second reader got %v; want lock_not_available (55P03) while the first holds the row", err)
	}
	_ = contender.Rollback(ctx)
	_ = holder.Rollback(ctx)

	// Released with the holder's transaction: the next reader gets the row at once.
	after, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Rollback(ctx) }()
	if _, err := after.Exec(ctx, "SET LOCAL lock_timeout = '100ms'"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.New(after).RefreshSessions().GetForUpdate(ctx, jti); err != nil {
		t.Fatalf("the lock outlived its transaction: %v", err)
	}
}

// Revoking a family touches only its live rows; already-revoked rows keep the time they were spent.
func TestRevokingAFamilyKeepsEachSpentRowsTimestamp(t *testing.T) {
	ctx := context.Background()
	store := repository.New(testdb.Tx(t))
	user, err := store.Users().Create(ctx, email(), "digest")
	if err != nil {
		t.Fatal(err)
	}
	family, other := uuid.New(), uuid.New()
	spent, live, elsewhere := uuid.New(), uuid.New(), uuid.New()
	for id, fam := range map[uuid.UUID]uuid.UUID{spent: family, live: family, elsewhere: other} {
		if err := store.RefreshSessions().Create(ctx, repository.NewRefreshSession{ID: id, UserID: user.ID, FamilyID: fam, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	spentAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	if err := store.RefreshSessions().Revoke(ctx, spent, spentAt); err != nil {
		t.Fatal(err)
	}

	revokedAt := time.Now().UTC().Truncate(time.Microsecond)
	err = store.WithTx(ctx, func(tx *repository.Store) error {
		return tx.RefreshSessions().RevokeFamily(ctx, family, revokedAt)
	})
	if err != nil {
		t.Fatal(err)
	}

	read := func(id uuid.UUID) repository.RefreshSession {
		var row repository.RefreshSession
		_ = store.WithTx(ctx, func(tx *repository.Store) error {
			row, err = tx.RefreshSessions().GetForUpdate(ctx, id)
			return err
		})
		return row
	}
	if got := read(spent).RevokedAt; got == nil || !got.Equal(spentAt) {
		t.Fatalf("the spent row's revoked_at = %v, want its original %v", got, spentAt)
	}
	if got := read(live).RevokedAt; got == nil || !got.Equal(revokedAt) {
		t.Fatalf("the live row's revoked_at = %v, want %v", got, revokedAt)
	}
	if got := read(elsewhere).RevokedAt; got != nil {
		t.Fatalf("another family's row was revoked at %v", got)
	}
}
