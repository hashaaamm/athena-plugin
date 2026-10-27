// Removed by the post-generation hook when use_postgres is "no".
package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
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
	if got := infratest.Get(c, "entryPoint"); !slices.Equal(toStrings(got), []string{"/app/api"}) {
		t.Errorf("entryPoint = %v", got)
	}
	if got := infratest.Get(c, "command"); !slices.Equal(toStrings(got), []string{"migrate", "up"}) {
		t.Errorf("command = %v", got)
	}
}

func TestTheMigrationTaskIsGivenOnlyTheDatabase(t *testing.T) {
	// It signs nothing. A task holding a credential it never uses is a credential with a second
	// place to leak from, and this one is the whole of the auth system.
	s := deployed(t, nil)
	env := infratest.EnvOf(s.migrateContainer(t))
	var secrets []string
	for name, value := range env {
		if strings.HasPrefix(value, "secret:") {
			secrets = append(secrets, name)
		}
	}
	if !slices.Equal(secrets, []string{"DATABASE_URL_OVERRIDE"}) {
		t.Errorf("the migration task mounts %v, want only DATABASE_URL_OVERRIDE", secrets)
	}
	serviceEnv := s.backendEnv(t)
	for _, name := range []string{"JWT_SECRET", "DATABASE_URL_OVERRIDE"} {
		if !strings.HasPrefix(serviceEnv[name], "secret:") {
			t.Errorf("the service does not mount %s from Secrets Manager: %q", name, serviceEnv[name])
		}
	}
}

func TestEverySecretReferencePinsAVersion(t *testing.T) {
	// `<arn>:::<version-id>`, never the bare ARN, which ECS resolves at AWSCURRENT — a different
	// value for tasks started either side of a rotation.
	s := deployed(t, nil)
	for owner, env := range map[string]map[string]string{
		"service": s.backendEnv(t),
		"migrate": infratest.EnvOf(s.migrateContainer(t)),
	} {
		for name, value := range env {
			if !strings.HasPrefix(value, "secret:") {
				continue
			}
			arn, version, ok := strings.Cut(strings.TrimPrefix(value, "secret:"), ":::")
			if !ok || version == "" || !strings.HasPrefix(arn, "arn:aws:secretsmanager:") {
				t.Errorf("%s %s = %q does not pin a version", owner, name, value)
			}
		}
	}
}

func TestTheServiceWaitsForTheSecretVersionsNotJustTheSecrets(t *testing.T) {
	svc := deployed(t, nil).Find(t, serviceType, slug)
	for _, version := range []string{"database-url-version", "jwt-secret-version"} {
		if !slices.ContainsFunc(svc.Dependencies, func(urn string) bool {
			return strings.HasSuffix(urn, "aws:secretsmanager/secretVersion:SecretVersion::"+version)
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
