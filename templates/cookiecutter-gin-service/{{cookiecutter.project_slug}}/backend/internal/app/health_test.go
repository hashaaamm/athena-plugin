package app_test

import (
{%- if cookiecutter.use_postgres == "yes" %}
	"context"
{%- endif %}
	"net/http"
	"testing"
{%- if cookiecutter.use_postgres == "yes" %}

	"github.com/jackc/pgx/v5/pgxpool"
{%- endif %}
)

// Health endpoints: enough to prove the service accepts traffic, says which build it is, and tells
// a load balancer the truth about its dependencies.

func TestLiveTouchesNothing(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))

	response := a.call(http.MethodGet, "/health/live", nil, nil)

	expectStatus(t, response, http.StatusOK)
	if response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("body %s", response.Body.String())
	}
}

func TestReadyReportsDependenciesAndTheBuild(t *testing.T) {
	a := newAPI(t, testConfig(t, nil))

	response := a.call(http.MethodGet, "/health/ready", nil, nil)

	expectStatus(t, response, http.StatusOK)
	body := object(t, response)
	if body["status"] != "ok" || body["version"] != "abc1234" {
		t.Fatalf("readiness %v", body)
	}
{%- if cookiecutter.use_postgres == "yes" %}
	if body["database"] != "ok" {
		t.Fatalf("readiness %v", body)
	}
{%- endif %}
}
{%- if cookiecutter.use_postgres == "yes" %}

// 503, not 500: "send traffic elsewhere", not "this request is broken". And liveness stays green,
// or a database blip becomes a restart loop.
func TestReadyIs503WhenTheDatabaseIsUnreachableAndLiveIsNot(t *testing.T) {
	// Lazy: nothing connects until the probe does, and port 1 refuses at once.
	unreachable, err := pgxpool.New(context.Background(), "postgres://nobody:nothing@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unreachable.Close)
	a := newAPIOn(t, testConfig(t, nil), unreachable)

	ready := a.call(http.MethodGet, "/health/ready", nil, nil)

	expectStatus(t, ready, http.StatusServiceUnavailable)
	if body := object(t, ready); body["status"] != "degraded" || body["database"] != "unreachable" {
		t.Fatalf("readiness %v", body)
	}
	expectStatus(t, a.call(http.MethodGet, "/health/live", nil, nil), http.StatusOK)
}
{%- endif %}
