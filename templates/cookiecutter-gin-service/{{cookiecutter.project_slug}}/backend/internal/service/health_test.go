package service_test

import (
	"context"
	"testing"
{% if cookiecutter.use_postgres == "yes" %}
	"{{ cookiecutter.go_module }}/internal/repository"
	"{{ cookiecutter.go_module }}/internal/service"
	"{{ cookiecutter.go_module }}/internal/testdb"
)

func TestReadinessReportsTheDatabaseAndTheBuild(t *testing.T) {
	svc := service.NewHealthService(repository.New(testdb.Tx(t)), "abc1234")

	readiness := svc.Readiness(context.Background())

	if !readiness.Ready() || readiness.Database != "ok" || readiness.Version != "abc1234" {
		t.Fatalf("Readiness = %+v", readiness)
	}
}
{%- else %}
	"{{ cookiecutter.go_module }}/internal/service"
)

func TestReadinessReportsTheBuild(t *testing.T) {
	readiness := service.NewHealthService("abc1234").Readiness(context.Background())

	if !readiness.Ready() || readiness.Version != "abc1234" {
		t.Fatalf("Readiness = %+v", readiness)
	}
}
{%- endif %}
