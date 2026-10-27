// Removed by the post-generation hook when use_postgres is "no".
package main

import (
	"slices"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
)

const jobType = "gcp:cloudrunv2/job:Job"

func (s stack) migrateContainer(t *testing.T) any {
	t.Helper()
	job := s.Find(t, jobType, slug+"-migrate")
	return infratest.Get(job.Inputs, "template", "template", "containers", 0)
}

func TestTheMigrationJobRunsTheBinarysMigrateCommand(t *testing.T) {
	// The same image as the service, a different command. A job left on the bootstrap image's
	// default command queues executions that do nothing, and nothing reports an error.
	c := deployed(t, nil).migrateContainer(t)
	if got := infratest.Get(c, "commands"); !slices.Equal(toStrings(got), []string{"/app/api"}) {
		t.Errorf("commands = %v", got)
	}
	if got := infratest.Get(c, "args"); !slices.Equal(toStrings(got), []string{"migrate", "up"}) {
		t.Errorf("args = %v", got)
	}
}

func TestTheMigrationJobIsNotGrantedTheSigningKey(t *testing.T) {
	// It signs nothing. A job holding a credential it never uses is a credential with a second
	// place to leak from, and this one is the whole of the auth system.
	s := deployed(t, nil)
	jobEnv := infratest.EnvOf(s.migrateContainer(t))
	if _, ok := jobEnv["JWT_SECRET"]; ok {
		t.Error("the migration job holds JWT_SECRET")
	}
	if !strings.HasPrefix(jobEnv["DATABASE_URL_OVERRIDE"], "secret:") {
		t.Errorf("the migration job has no database: %v", jobEnv)
	}
	serviceEnv := s.backendEnv(t)
	for _, name := range []string{"JWT_SECRET", "DATABASE_URL_OVERRIDE"} {
		if !strings.HasPrefix(serviceEnv[name], "secret:") {
			t.Errorf("the service does not mount %s from Secret Manager: %q", name, serviceEnv[name])
		}
	}
}

func TestTheServiceAndTheJobBothReachTheDatabaseOverTheConnector(t *testing.T) {
	s := deployed(t, nil)
	svc := s.Find(t, serviceType, slug)
	job := s.Find(t, jobType, slug+"-migrate")
	for name, volumes := range map[string]any{
		"service": infratest.Get(svc.Inputs, "template", "volumes"),
		"job":     infratest.Get(job.Inputs, "template", "template", "volumes"),
	} {
		if got := infratest.Get(volumes, 0, "cloudSqlInstance", "instances", 0); got != s.exports["cloud_sql_instance"] {
			t.Errorf("%s Cloud SQL volume = %v, want %q", name, got, s.exports["cloud_sql_instance"])
		}
	}
}

func TestTheServiceWaitsForTheSecretVersionsNotJustTheContainers(t *testing.T) {
	// A service that waits only for the container races the version into existence and fails
	// with "version was not found".
	svc := deployed(t, nil).Find(t, serviceType, slug)
	for _, version := range []string{"database-url-version", "jwt-secret-version"} {
		if !slices.ContainsFunc(svc.Dependencies, func(urn string) bool {
			return strings.HasSuffix(urn, "gcp:secretmanager/secretVersion:SecretVersion::"+version)
		}) {
			t.Errorf("the service does not wait for %s", version)
		}
	}
}

func TestTheSigningKeyIsGeneratedAndLongEnough(t *testing.T) {
	// `serve` refuses a key under 32 characters outside local/test. 48 is the margin.
	key := deployed(t, nil).Find(t, "random:index/randomPassword:RandomPassword", "jwt-secret")
	if key.Inputs["length"] != 48.0 {
		t.Errorf("jwt-secret length = %v", key.Inputs["length"])
	}
}

func TestTheDatabaseExportsAreSet(t *testing.T) {
	s := deployed(t, nil)
	if s.exports["cloud_run_migrate_job"] != slug+"-migrate-dev" {
		t.Errorf("cloud_run_migrate_job = %q", s.exports["cloud_run_migrate_job"])
	}
	if s.exports["cloud_sql_instance"] == "" {
		t.Error("cloud_sql_instance is empty")
	}
}

func TestTheRefreshTransportIsAlwaysTheConfiguredOne(t *testing.T) {
	// Set even at its default, so the deployed value is the one in Pulumi.<stack>.yaml rather than
	// whatever the image defaults to. A browser client gets `cookie`: the refresh token in an
	// HttpOnly cookie rather than in storage the bundle's JavaScript can read.
	if got := deployed(t, nil).backendEnv(t)["REFRESH_TRANSPORT"]; got != "body" {
		t.Errorf("REFRESH_TRANSPORT = %q, want body by default", got)
	}
	if got := deployed(t, withBrowserClient).backendEnv(t)["REFRESH_TRANSPORT"]; got != "cookie" {
		t.Errorf("REFRESH_TRANSPORT = %q, want cookie when configured", got)
	}
}

func TestTheMigrationJobGetsNoHTTPSettings(t *testing.T) {
	// It serves no HTTP. And the application refuses REFRESH_TRANSPORT=cookie without CORS_ORIGINS
	// at load, so a job handed one without the other would never boot.
	jobEnv := infratest.EnvOf(deployed(t, withBrowserClient).migrateContainer(t))
	for _, name := range []string{"CORS_ORIGINS", "REFRESH_TRANSPORT"} {
		if got, ok := jobEnv[name]; ok {
			t.Errorf("the migration job has %s = %q", name, got)
		}
	}
}

func toStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}
