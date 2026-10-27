// Removed by the post-generation hook when use_postgres is "no".
package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const taskDefinitionType = "aws:ecs/taskDefinition:TaskDefinition"

func (s stack) migrateContainer(t *testing.T) map[string]any {
	t.Helper()
	raw, _ := s.Find(t, taskDefinitionType, "migrate").Inputs["containerDefinitions"].(string)
	var definitions []map[string]any
	if err := json.Unmarshal([]byte(raw), &definitions); err != nil || len(definitions) != 1 {
		t.Fatalf("container definitions %q: %v", raw, err)
	}
	return definitions[0]
}

func TestTheMigrationTaskRunsTheBinarysMigrateCommand(t *testing.T) {
	// The image's ENTRYPOINT is /app/api and its CMD is `serve`; ECS overrides them separately.
	// A task left on the default command would start a second server, pass, and migrate nothing.
	c := deployed(t, nil).migrateContainer(t)
	// The service's first image: the first deploy migrates with the image it just pushed.
	if got := infratest.Get(c, "image"); got != firstImage {
		t.Errorf("image = %v, want the service's first image", got)
	}
	if got := infratest.Get(c, "entryPoint"); !slices.Equal(toStrings(got), []string{"/app/api"}) {
		t.Errorf("entryPoint = %v", got)
	}
	if got := infratest.Get(c, "command"); !slices.Equal(toStrings(got), []string{"migrate", "up"}) {
		t.Errorf("command = %v", got)
	}
}

// withSecrets is a stack after `just infra-secrets` has recorded the signing key's VersionId.
func withSecrets(c *stackconfig.StackConfig) {
	c.JWTSecretVersion = "a1b2c3d4-0000-0000-0000-000000000000"
}

func TestTheMigrationTaskIsGivenOnlyTheMasterLogin(t *testing.T) {
	// It signs nothing. A task holding a credential it never uses is a credential with a second
	// place to leak from, and the signing key is the whole of the auth system.
	s := deployed(t, withSecrets)
	env := infratest.EnvOf(s.migrateContainer(t))
	var secrets []string
	for name, value := range env {
		if strings.HasPrefix(value, "secret:") {
			secrets = append(secrets, name)
		}
	}
	slices.Sort(secrets)
	if !slices.Equal(secrets, []string{"DATABASE_PASSWORD", "DATABASE_USER"}) {
		t.Errorf("the migration task mounts %v, want only the master login", secrets)
	}
	if got := s.backendEnv(t)["JWT_SECRET"]; !strings.HasSuffix(got, ":::a1b2c3d4-0000-0000-0000-000000000000") {
		t.Errorf("JWT_SECRET = %q, want the pinned version", got)
	}
}

func TestTheMigrationTaskLogsInAsTheMasterFromRDSsOwnSecret(t *testing.T) {
	// `<arn>:<key>::` — the one unpinned reference, because RDS rotates the secret and a pinned
	// version would be a password the database no longer accepts.
	s := deployed(t, nil)
	env := infratest.EnvOf(s.migrateContainer(t))
	want := map[string]string{
		"DATABASE_AUTH":     "password",
		"DATABASE_HOST":     slug + "-pg-dev.abcdefghijkl." + infratest.Region + ".rds.amazonaws.com",
		"DATABASE_PORT":     "5432",
		"DATABASE_NAME":     "app",
		"DATABASE_USER":     "secret:" + infratest.MasterSecretARN + ":username::",
		"DATABASE_PASSWORD": "secret:" + infratest.MasterSecretARN + ":password::",
		"PGSSLMODE":         "require",
		"DATABASE_APP_ROLE": "app_iam",
		"DATABASE_APP_AUTH": "rds-iam",
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
	}
	if _, ok := s.Find(t, taskDefinitionType, "migrate").Inputs["taskRoleArn"]; ok {
		t.Error("the migration task has a task role")
	}
}

func TestTheServiceConnectsAsTheAppRoleWithAnIAMTokenAndNoPassword(t *testing.T) {
	s := deployed(t, withSecrets)
	env := s.backendEnv(t)
	want := map[string]string{
		"DATABASE_AUTH": "rds-iam",
		"DATABASE_HOST": slug + "-pg-dev.abcdefghijkl." + infratest.Region + ".rds.amazonaws.com",
		"DATABASE_PORT": "5432",
		"DATABASE_NAME": "app",
		"DATABASE_USER": "app_iam",
		"AWS_REGION":    infratest.Region,
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
	}
	for _, name := range []string{"DATABASE_PASSWORD", "DATABASE_URL_OVERRIDE", "PGSSLMODE"} {
		if _, ok := env[name]; ok {
			t.Errorf("the service has %s", name)
		}
	}
	// And its task role may connect as exactly that user, on exactly this instance.
	policy, _ := s.Find(t, "aws:iam/rolePolicy:RolePolicy", "runtime-connects-to-database").Inputs["policy"].(string)
	if !strings.Contains(policy, ":dbuser:"+infratest.DatabaseResourceID+"/app_iam") {
		t.Errorf("the runtime role's database grant = %s", policy)
	}
}

func TestTheConfiguredAppRoleReachesTheServiceTheMigratorAndTheGrant(t *testing.T) {
	// One name in three places: the user the service logs in as, the role the migrator creates,
	// and the user IAM lets the task role connect as. Any two disagreeing is a login that fails.
	s := deployed(t, func(c *stackconfig.StackConfig) { c.DatabaseAppRole = "orders_api" })
	if got := s.backendEnv(t)["DATABASE_USER"]; got != "orders_api" {
		t.Errorf("DATABASE_USER = %q", got)
	}
	if got := infratest.EnvOf(s.migrateContainer(t))["DATABASE_APP_ROLE"]; got != "orders_api" {
		t.Errorf("DATABASE_APP_ROLE = %q", got)
	}
	policy, _ := s.Find(t, "aws:iam/rolePolicy:RolePolicy", "runtime-connects-to-database").Inputs["policy"].(string)
	if !strings.Contains(policy, "/orders_api") {
		t.Errorf("the runtime role's database grant = %s", policy)
	}
}

func TestEveryOtherSecretReferencePinsAVersion(t *testing.T) {
	// `<arn>:::<version-id>`, never the bare ARN, which ECS resolves at AWSCURRENT — a different
	// value for tasks started either side of a rotation. RDS's own master secret is the one
	// exception, and only the migration task mounts it.
	s := deployed(t, withSecrets)
	for owner, env := range map[string]map[string]string{
		"service": s.backendEnv(t),
		"migrate": infratest.EnvOf(s.migrateContainer(t)),
	} {
		for name, value := range env {
			if !strings.HasPrefix(value, "secret:") || strings.HasPrefix(value, "secret:"+infratest.MasterSecretARN+":") {
				continue
			}
			arn, version, ok := strings.Cut(strings.TrimPrefix(value, "secret:"), ":::")
			if !ok || version == "" || !strings.HasPrefix(arn, "arn:aws:secretsmanager:") {
				t.Errorf("%s %s = %q does not pin a version", owner, name, value)
			}
		}
	}
	for name, value := range s.backendEnv(t) {
		if strings.Contains(value, infratest.MasterSecretARN) {
			t.Errorf("the service mounts the master secret as %s", name)
		}
	}
}

func TestNothingIsMountedUntilItsVersionIsConfigured(t *testing.T) {
	// The first `up` creates the signing key's secret empty, and a task cannot start on an empty
	// secret. So the stack mounts it only once `just infra-secrets` has recorded its version, and
	// says so in secrets_ready, which sync-github.sh checks before it un-gates CD.
	s := deployed(t, nil)
	if _, ok := s.backendEnv(t)["JWT_SECRET"]; ok {
		t.Error("JWT_SECRET is mounted before jwtSecretVersion is set")
	}
	if s.exports["secrets_ready"] != "false" {
		t.Errorf("secrets_ready = %q before the version is set", s.exports["secrets_ready"])
	}
	if got := deployed(t, withSecrets).exports["secrets_ready"]; got != "true" {
		t.Errorf("secrets_ready = %q with the version set", got)
	}
}

func TestTheMigrationTaskWaitsForItsExecutionRolesGrants(t *testing.T) {
	// The same race as the service's: a task whose execution role cannot yet read the master
	// secret fails to start.
	task := deployed(t, nil).Find(t, taskDefinitionType, "migrate")
	if !slices.ContainsFunc(task.Dependencies, func(urn string) bool {
		return strings.HasSuffix(urn, "aws:iam/rolePolicy:RolePolicy::migrate-execution-reads-master-secret")
	}) {
		t.Errorf("the migration task does not wait for its execution role's grant; dependencies = %v", task.Dependencies)
	}
}

func TestTheDeployRoleMayPassTheMigrationExecutionRole(t *testing.T) {
	// cd.yml registers a revision of the migration family, which passes its execution role again.
	s := deployed(t, nil)
	policy, _ := s.Find(t, "aws:iam/rolePolicy:RolePolicy", "deploy-permissions").Inputs["policy"].(string)
	role, _ := s.Find(t, roleType, "migrate-execution-role").Inputs["name"].(string)
	if !strings.Contains(policy, ":role/"+role+`"`) {
		t.Errorf("the deploy policy cannot pass %s: %s", role, policy)
	}
}

func TestTheDatabaseExportsAreSet(t *testing.T) {
	s := deployed(t, nil)
	if s.exports["ecs_migrate_task_family"] != slug+"-migrate-dev" {
		t.Errorf("ecs_migrate_task_family = %q", s.exports["ecs_migrate_task_family"])
	}
	if s.exports["rds_instance"] == "" {
		t.Error("rds_instance is empty")
	}
}

func TestTheDatabaseLivesInTheIsolatedSubnetsBehindItsOwnGroup(t *testing.T) {
	s := deployed(t, nil)
	group := s.Find(t, "aws:rds/subnetGroup:SubnetGroup", "db-subnets")
	for _, id := range toStrings(group.Inputs["subnetIds"]) {
		if !strings.HasPrefix(id, "isolated-") {
			t.Errorf("the database subnet group holds %s", id)
		}
	}
	instance := s.Find(t, "aws:rds/instance:Instance", "postgres")
	if got := toStrings(instance.Inputs["vpcSecurityGroupIds"]); !slices.Equal(got, []string{"db-id"}) {
		t.Errorf("vpcSecurityGroupIds = %v", got)
	}
}

func TestTheDeployRoleMayRunOnlyTheMigrationFamily(t *testing.T) {
	policy, _ := deployed(t, nil).Find(t, "aws:iam/rolePolicy:RolePolicy", "deploy-permissions").Inputs["policy"].(string)
	if !strings.Contains(policy, ":task-definition/"+slug+"-migrate-dev:*") {
		t.Errorf("the deploy policy does not scope RunTask to the migration family: %s", policy)
	}
	if !strings.Contains(policy, "log-group:/ecs/"+slug+"-migrate-dev:*") {
		t.Errorf("the deploy policy cannot read the migration's log: %s", policy)
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

func TestTheMigrationTaskGetsNoHTTPSettings(t *testing.T) {
	// It serves no HTTP. And the application refuses REFRESH_TRANSPORT=cookie without CORS_ORIGINS
	// at load, so a task handed one without the other would never boot.
	env := infratest.EnvOf(deployed(t, withBrowserClient).migrateContainer(t))
	for _, name := range []string{"CORS_ORIGINS", "REFRESH_TRANSPORT"} {
		if got, ok := env[name]; ok {
			t.Errorf("the migration task has %s = %q", name, got)
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
