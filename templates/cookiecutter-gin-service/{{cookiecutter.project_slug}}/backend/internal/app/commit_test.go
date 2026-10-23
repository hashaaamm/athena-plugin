package app_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"{{ cookiecutter.go_module }}/internal/testdb"
)

// The commit happens before the response is written.
//
// A client that receives a 2xx is entitled to believe the write happened. That is true only if the
// unit of work commits before the handler writes — which it does, because services call
// Store.WithTx and return only after the commit — and only if a failed commit becomes an error the
// client sees. Here the commit is made to fail the way Cloud SQL's proxy dropping a connection
// mid-commit fails, and the client must get a 500 with a trace id, not a 201.

// commitFails is a database handle whose transactions cannot commit. Commit rolls the savepoint back
// underneath, so the write is really undone, then reports the failure.
type commitFails struct {
	pgx.Tx
	commits *atomic.Int32
}

func (c commitFails) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := c.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failingCommit{Tx: tx, commits: c.commits}, nil
}

type failingCommit struct {
	pgx.Tx
	commits *atomic.Int32
}

func (f failingCommit) Commit(ctx context.Context) error {
	f.commits.Add(1)
	_ = f.Rollback(ctx)
	return errors.New("write tcp 10.0.0.2:5432: connection reset by peer")
}

func TestAWriteWhoseCommitFailsIsA500NotA201(t *testing.T) {
	tx := testdb.Tx(t)
	commits := &atomic.Int32{}
	broken := newAPIOn(t, testConfig(t, nil), commitFails{Tx: tx, commits: commits})
	payload := registerPayload()

	response := broken.call(http.MethodPost, "/api/v1/auth/register", payload, nil)

	if commits.Load() == 0 {
		t.Fatal("the request never reached its commit")
	}
	expectStatus(t, response, http.StatusInternalServerError)
	envelope, _ := object(t, response)["error"].(map[string]any)
	if envelope["code"] != "internal_error" || envelope["trace_id"] == nil || envelope["trace_id"] == "" {
		t.Fatalf("body %s; want internal_error with a trace id", response.Body.String())
	}
	if text := response.Body.String(); strings.Contains(text, "connection reset") || strings.Contains(text, "10.0.0.2") {
		t.Fatalf("the 500 leaks the underlying error: %s", text)
	}

	// And the account really does not exist: the same email registers on a healthy handle.
	healthy := newAPIOn(t, testConfig(t, nil), tx)
	expectStatus(t, healthy.call(http.MethodPost, "/api/v1/auth/register", payload, nil), http.StatusCreated)
}
