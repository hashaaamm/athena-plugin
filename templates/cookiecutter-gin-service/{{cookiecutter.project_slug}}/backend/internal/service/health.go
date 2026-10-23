// Package service holds the use cases. One exported method per use case: it applies the rules,
// calls whatever the use case needs, and returns a result the handler maps to its wire type.
//
// Nothing here imports net/http, gin, pgx or internal/db (depguard enforces it): a service must be
// callable from a worker, a CLI or a test with no HTTP stack anywhere, so it returns apperr values
// and never a status code, and it reaches the database only through internal/repository.
//
// Ask Athena for the layered architecture rules.
package service

import (
	"context"
{%- if cookiecutter.use_postgres == "yes" %}
	"time"

	"{{ cookiecutter.go_module }}/internal/repository"
{%- endif %}
)

// Readiness is what "ready" means for this service.
//
// Liveness and readiness answer different questions, which is why they are different endpoints.
// Liveness touches nothing: a probe that checks the database restarts the application during a
// database blip, turning a dependency's bad minute into an outage of your own. Readiness is where a
// dependency check belongs, and a second dependency is another field rather than another shape.
type Readiness struct {
	// "ok" or "degraded".
	Status string
	// The build that is answering.
	Version string
{%- if cookiecutter.use_postgres == "yes" %}
	// "ok" when the database answered a trivial query, "unreachable" otherwise.
	Database string
{%- endif %}
}

// Ready reports whether Status is "ok".
func (r Readiness) Ready() bool { return r.Status == "ok" }
{%- if cookiecutter.use_postgres == "yes" %}

// pingTimeout bounds the readiness query. A probe that hangs as long as the database does tells the
// load balancer nothing until its own timeout fires, and holds a pool connection the whole time.
const pingTimeout = 2 * time.Second

// HealthService answers the readiness probe.
type HealthService struct {
	store   *repository.Store
	version string
}

// NewHealthService builds the service. version is the build SHA.
func NewHealthService(store *repository.Store, version string) *HealthService {
	return &HealthService{store: store, version: version}
}

// Readiness checks every dependency and reports the result. It never fails: any failure to reach
// the database means "not ready", including the ones nobody thought of — readiness is the one place
// treating every error alike is the correct answer.
func (s *HealthService) Readiness(ctx context.Context) Readiness {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := s.store.Health().Ping(ctx); err != nil {
		return Readiness{Status: "degraded", Version: s.version, Database: "unreachable"}
	}
	return Readiness{Status: "ok", Version: s.version, Database: "ok"}
}
{%- else %}

// HealthService answers the readiness probe. No dependencies yet, so readiness is liveness; add a
// check here, and a field on Readiness, as each dependency appears.
type HealthService struct {
	version string
}

// NewHealthService builds the service. version is the build SHA.
func NewHealthService(version string) *HealthService {
	return &HealthService{version: version}
}

// Readiness checks every dependency and reports the result.
func (s *HealthService) Readiness(_ context.Context) Readiness {
	return Readiness{Status: "ok", Version: s.version}
}
{%- endif %}
