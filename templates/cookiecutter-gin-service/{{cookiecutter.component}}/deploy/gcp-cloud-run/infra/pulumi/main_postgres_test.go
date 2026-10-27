// Removed by the post-generation hook when use_postgres is "no".
package main

import (
	"slices"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const jobType = "gcp:cloudrunv2/job:Job"

func (s stack) migrateJob(t *testing.T) infratest.Resource {
	t.Helper()
	return s.Find(t, jobType, slug+"-migrate")
}

func (s stack) migrateContainer(t *testing.T) any {
	t.Helper()
	return infratest.Get(s.migrateJob(t).Inputs, "template", "template", "containers", 0)
}

// accountEmail is the email of the stack's account for role, as the mocked provider reports it.
func accountEmail(t *testing.T, role string) string {
	t.Helper()
	id, err := baseConfig().AccountID(role)
	if err != nil {
		t.Fatal(err)
	}
	return id + "@" + infratest.Project + ".iam.gserviceaccount.com"
}

// withSecrets is a stack after `just infra-secrets` has recorded both version numbers.
func withSecrets(c *stackconfig.StackConfig) {
	c.JWTSecretVersion = "2"
	c.DBAdminPasswordVersion = "5"
}

func TestTheMigrationJobRunsTheBinarysMigrateCommand(t *testing.T) {
	// The same image as the service, a different command. A job left on the image's default
	// command would start a server, never exit, and migrate nothing.
	c := deployed(t, nil).migrateContainer(t)
	if got := infratest.Get(c, "image"); got != firstImage {
		t.Errorf("image = %v, want the service's first image", got)
	}
	if got := infratest.Get(c, "commands"); !slices.Equal(toStrings(got), []string{"/app/api"}) {
		t.Errorf("commands = %v", got)
	}
	if got := infratest.Get(c, "args"); !slices.Equal(toStrings(got), []string{"migrate", "up"}) {
		t.Errorf("args = %v", got)
	}
}

// --- Two database identities ------------------------------------------------------------------

func TestTheServiceConnectsAsItsOwnIAMUserWithNoPassword(t *testing.T) {
	// The Go connector in-process, as the service account: no password, no socket, no volume.
	s := deployed(t, withSecrets)
	env := s.backendEnv(t)
	want := map[string]string{
		"DATABASE_AUTH":      "cloud-sql-iam",
		"CLOUD_SQL_INSTANCE": s.exports["cloud_sql_instance"],
		"DATABASE_NAME":      "app",
		"DATABASE_USER":      strings.TrimSuffix(accountEmail(t, "run"), ".gserviceaccount.com"),
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
	}
	for _, name := range []string{"DATABASE_PASSWORD", "DATABASE_URL_OVERRIDE", "DATABASE_HOST"} {
		if _, ok := env[name]; ok {
			t.Errorf("the service has %s", name)
		}
	}
	if volumes, _ := infratest.Get(s.Find(t, serviceType, slug).Inputs, "template", "volumes").([]any); len(volumes) != 0 {
		t.Errorf("the service mounts %v", volumes)
	}
}

func TestTheMigrationJobLogsInAsTheAdminAndGrantsTheService(t *testing.T) {
	// The admin login over Cloud Run's Cloud SQL socket, password pinned; then the grants to the
	// service's IAM user, by the same name the service logs in with.
	s := deployed(t, withSecrets)
	env := infratest.EnvOf(s.migrateContainer(t))
	want := map[string]string{
		"DATABASE_AUTH":     "password",
		"DATABASE_HOST":     "/cloudsql/" + s.exports["cloud_sql_instance"],
		"DATABASE_NAME":     "app",
		"DATABASE_USER":     "postgres",
		"DATABASE_PASSWORD": "secret:" + slug + "-db-admin-password-dev@5",
		"DATABASE_APP_ROLE": s.backendEnv(t)["DATABASE_USER"],
		"DATABASE_APP_AUTH": "cloud-sql-iam",
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
	}
	job := s.migrateJob(t)
	if got := infratest.Get(job.Inputs, "template", "template", "volumes", 0, "cloudSqlInstance", "instances", 0); got != s.exports["cloud_sql_instance"] {
		t.Errorf("the job's Cloud SQL volume = %v", got)
	}
}

func TestTheMigrationJobRunsAsTheMigratorAndTheServiceDoesNot(t *testing.T) {
	s := deployed(t, nil)
	migrator := infratest.Get(s.migrateJob(t).Inputs, "template", "template", "serviceAccount")
	service := infratest.Get(s.Find(t, serviceType, slug).Inputs, "template", "serviceAccount")
	if migrator != accountEmail(t, "migrate") {
		t.Errorf("the job runs as %v", migrator)
	}
	if service == migrator {
		t.Error("the service and the migration job share an account")
	}
}

func TestEachSecretIsReadableOnlyByTheAccountThatUsesIt(t *testing.T) {
	// The admin password is the one database credential in the stack. The service must not be able
	// to read it, and the migrator — which signs nothing — must not read the signing key.
	s := deployed(t, nil)
	runtime := "serviceAccount:" + accountEmail(t, "run")
	migrator := "serviceAccount:" + accountEmail(t, "migrate")
	for secret, member := range map[string]string{"db-admin-password": migrator, "jwt-secret": runtime} {
		grant := s.Find(t, "gcp:secretmanager/secretIamMember:SecretIamMember", secret+"-accessor")
		if grant.Inputs["member"] != member {
			t.Errorf("%s is readable by %v, want %s", secret, grant.Inputs["member"], member)
		}
	}
}

func TestTheMigrationJobIsNotGrantedTheSigningKey(t *testing.T) {
	// It signs nothing. A job holding a credential it never uses is a credential with a second
	// place to leak from, and this one is the whole of the auth system.
	s := deployed(t, withSecrets)
	jobEnv := infratest.EnvOf(s.migrateContainer(t))
	if _, ok := jobEnv["JWT_SECRET"]; ok {
		t.Error("the migration job holds JWT_SECRET")
	}
	if got := s.backendEnv(t)["JWT_SECRET"]; got != "secret:"+slug+"-jwt-secret-dev@2" {
		t.Errorf("JWT_SECRET = %q, want the pinned version", got)
	}
}

// --- Out-of-band secrets ----------------------------------------------------------------------

func TestNothingIsMountedUntilItsVersionIsConfigured(t *testing.T) {
	// The first `up` creates the containers empty, and a mounted empty container fails the deploy.
	// So the stack mounts each secret only once `just infra-secrets` has recorded its version, and
	// says so in secrets_ready, which sync-github.sh checks before it un-gates CD.
	s := deployed(t, nil)
	if _, ok := s.backendEnv(t)["JWT_SECRET"]; ok {
		t.Error("JWT_SECRET is mounted before jwtSecretVersion is set")
	}
	if _, ok := infratest.EnvOf(s.migrateContainer(t))["DATABASE_PASSWORD"]; ok {
		t.Error("DATABASE_PASSWORD is mounted before dbAdminPasswordVersion is set")
	}
	if s.exports["secrets_ready"] != "false" {
		t.Errorf("secrets_ready = %q before any version is set", s.exports["secrets_ready"])
	}
	for _, half := range []func(*stackconfig.StackConfig){
		func(c *stackconfig.StackConfig) { c.JWTSecretVersion = "1" },
		func(c *stackconfig.StackConfig) { c.DBAdminPasswordVersion = "1" },
	} {
		if got := deployed(t, half).exports["secrets_ready"]; got != "false" {
			t.Errorf("secrets_ready = %q with one of the two versions set", got)
		}
	}
	if got := deployed(t, withSecrets).exports["secrets_ready"]; got != "true" {
		t.Errorf("secrets_ready = %q with both versions set", got)
	}
}

func TestTheDatabaseExportsAreSet(t *testing.T) {
	s := deployed(t, nil)
	if s.exports["cloud_run_migrate_job"] != slug+"-migrate-dev" {
		t.Errorf("cloud_run_migrate_job = %q", s.exports["cloud_run_migrate_job"])
	}
	if !strings.HasPrefix(s.exports["cloud_sql_instance"], infratest.Project+":") {
		t.Errorf("cloud_sql_instance = %q", s.exports["cloud_sql_instance"])
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
