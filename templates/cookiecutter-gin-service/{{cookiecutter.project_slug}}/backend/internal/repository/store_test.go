package repository_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
	"{{ cookiecutter.go_module }}/internal/repository"
	"{{ cookiecutter.go_module }}/internal/testdb"
)

// The one decision about transactions: success commits, an error rolls back, and the one error
// whose refusal *is* the write keeps it.

func email() string { return "ada-" + uuid.NewString()[:8] + "@example.com" }

func exists(t *testing.T, store *repository.Store, address string) bool {
	t.Helper()
	_, err := store.Users().GetByEmail(context.Background(), address)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestASuccessfulUnitOfWorkCommits(t *testing.T) {
	store := repository.New(testdb.Tx(t))
	address := email()

	err := store.WithTx(context.Background(), func(tx *repository.Store) error {
		_, err := tx.Users().Create(context.Background(), address, "digest")
		return err
	})

	if err != nil || !exists(t, store, address) {
		t.Fatalf("WithTx = %v, user exists = %v; want nil, true", err, exists(t, store, address))
	}
}

// A failure late in a use case undoes the writes that came earlier in it.
func TestAFailedUnitOfWorkRollsBack(t *testing.T) {
	store := repository.New(testdb.Tx(t))
	address := email()

	for _, failure := range []error{apperr.Conflict("late refusal"), errors.New("a defect")} {
		err := store.WithTx(context.Background(), func(tx *repository.Store) error {
			if _, err := tx.Users().Create(context.Background(), address, "digest"); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("WithTx = %v, want %v back", err, failure)
		}
		if exists(t, store, address) {
			t.Fatalf("the write survived %v", failure)
		}
	}
}

// apperr.CompromisedSession: the 401 goes out and the family revocation stays. A rollback here would
// undo the whole defence.
func TestAnErrorThatKeepsItsWritesCommitsThemAndIsStillReturned(t *testing.T) {
	store := repository.New(testdb.Tx(t))
	address := email()
	refusal := apperr.CompromisedSession("Invalid or expired token")

	err := store.WithTx(context.Background(), func(tx *repository.Store) error {
		if _, err := tx.Users().Create(context.Background(), address, "digest"); err != nil {
			return err
		}
		return refusal
	})

	if !errors.Is(err, refusal) {
		t.Fatalf("WithTx = %v, want the refusal back", err)
	}
	if !exists(t, store, address) {
		t.Fatal("the writes the refusal exists to keep were rolled back")
	}
}

func TestAPanicRollsBackAndStillPanics(t *testing.T) {
	store := repository.New(testdb.Tx(t))
	address := email()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic was swallowed")
			}
		}()
		_ = store.WithTx(context.Background(), func(tx *repository.Store) error {
			if _, err := tx.Users().Create(context.Background(), address, "digest"); err != nil {
				return err
			}
			panic("a defect")
		})
	}()

	if exists(t, store, address) {
		t.Fatal("the write survived the panic")
	}
}

// Two registrations racing for one address: the index decides, and the loser gets ErrDuplicate.
func TestATakenEmailIsErrDuplicate(t *testing.T) {
	store := repository.New(testdb.Tx(t))
	address := email()
	if _, err := store.Users().Create(context.Background(), address, "digest"); err != nil {
		t.Fatal(err)
	}

	err := store.WithTx(context.Background(), func(tx *repository.Store) error {
		_, err := tx.Users().Create(context.Background(), address, "digest")
		return err
	})

	if !errors.Is(err, repository.ErrDuplicate) {
		t.Fatalf("second Create = %v, want ErrDuplicate", err)
	}
}

func TestAMissingRowIsErrNotFound(t *testing.T) {
	store := repository.New(testdb.Tx(t))

	if _, err := store.Users().Get(context.Background(), uuid.New()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
}

// One `%+v` in an error message must not put a password hash in a log aggregator.
func TestAUserPrintsNoDigest(t *testing.T) {
	store := repository.New(testdb.Tx(t))
	user, err := store.Users().Create(context.Background(), email(), "$argon2id$v=19$the-digest")
	if err != nil {
		t.Fatal(err)
	}

	if dump := fmt.Sprintf("%v %+v %#v", user, user, user); strings.Contains(dump, "the-digest") {
		t.Fatalf("a User printed its digest: %s", dump)
	}
}

func TestPingAnswers(t *testing.T) {
	if err := repository.New(testdb.Tx(t)).Health().Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
