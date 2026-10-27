// Removed by the post-generation hook when use_postgres is "no", together with database.go.
package components_test

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/components"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const instanceType = "aws:rds/instance:Instance"

func databaseArgs() components.DatabaseArgs {
	return components.DatabaseArgs{
		SubnetIDs:       pulumi.ToStringArray([]string{"isolated-0-id", "isolated-1-id"}),
		SecurityGroupID: pulumi.String("db-id"),
	}
}

func newDatabase(t *testing.T, c stackconfig.StackConfig) *infratest.Recorder {
	t.Helper()
	return run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewDatabase(ctx, c, databaseArgs())
		return err
	})
}

func TestTheDatabaseIsUnreachableFromOutsideTheVPC(t *testing.T) {
	// No public endpoint, in the isolated subnets, behind the group that admits the app tasks
	// only. Each is one line, and any one of them wrong is a database on the internet.
	rec := newDatabase(t, cfg)
	instance := rec.Find(t, instanceType, "postgres").Inputs
	if instance["publiclyAccessible"] != false {
		t.Error("publiclyAccessible is not false")
	}
	if got := strs(instance["vpcSecurityGroupIds"]); !slices.Equal(got, []string{"db-id"}) {
		t.Errorf("vpcSecurityGroupIds = %v", got)
	}
	group := rec.Find(t, "aws:rds/subnetGroup:SubnetGroup", "db-subnets")
	if got := strs(group.Inputs["subnetIds"]); !slices.Equal(got, []string{"isolated-0-id", "isolated-1-id"}) {
		t.Errorf("the subnet group is %v, not the isolated subnets", got)
	}
	if instance["dbSubnetGroupName"] != group.Inputs["name"] {
		t.Errorf("the instance is in %v, not its own subnet group", instance["dbSubnetGroupName"])
	}
}

func TestTheDatabaseIsEncryptedAtRestAndInTransit(t *testing.T) {
	rec := newDatabase(t, cfg)
	instance := rec.Find(t, instanceType, "postgres").Inputs
	if instance["storageEncrypted"] != true {
		t.Error("storage is not encrypted")
	}
	parameters := rec.Find(t, "aws:rds/parameterGroup:ParameterGroup", "db-parameters")
	if instance["parameterGroupName"] != parameters.Inputs["name"] {
		t.Error("the instance does not use the TLS-only parameter group")
	}
	if got := infratest.Get(parameters.Inputs, "parameters", 0); infratest.Get(got, "name") != "rds.force_ssl" ||
		infratest.Get(got, "value") != "1" {
		t.Errorf("parameters = %v", got)
	}
	if instance["caCertIdentifier"] != components.CACertificate {
		t.Errorf("caCertIdentifier = %v", instance["caCertIdentifier"])
	}
}

func TestTheDatabaseUsesTheCheapestHonestShape(t *testing.T) {
	instance := newDatabase(t, cfg).Find(t, instanceType, "postgres").Inputs
	want := map[string]any{
		"engine":              "postgres",
		"engineVersion":       "16",
		"instanceClass":       "db.t4g.micro",
		"allocatedStorage":    20.0,
		"maxAllocatedStorage": 100.0,
		"storageType":         "gp3",
		"multiAz":             false,
	}
	for key, value := range want {
		if got := instance[key]; got != value {
			t.Errorf("%s = %v, want %v", key, got, value)
		}
	}
}

func TestOnlyProductionProtectsTheDatabaseFromADestroy(t *testing.T) {
	// Outside production a destroy leaves nothing behind, a final snapshot included. In
	// production deletion protection is on and a destroy that gets past it still leaves a snapshot.
	dev := newDatabase(t, cfg).Find(t, instanceType, "postgres").Inputs
	if dev["deletionProtection"] != false || dev["skipFinalSnapshot"] != true || dev["finalSnapshotIdentifier"] != nil {
		t.Errorf("dev: deletionProtection=%v skipFinalSnapshot=%v finalSnapshotIdentifier=%v",
			dev["deletionProtection"], dev["skipFinalSnapshot"], dev["finalSnapshotIdentifier"])
	}
	prod := cfg
	prod.Environment = "prod"
	instance := newDatabase(t, prod).Find(t, instanceType, "postgres").Inputs
	if instance["deletionProtection"] != true {
		t.Error("a prod database without deletion protection is one `pulumi destroy` from gone")
	}
	if instance["skipFinalSnapshot"] != false || instance["finalSnapshotIdentifier"] == nil {
		t.Errorf("prod: skipFinalSnapshot=%v finalSnapshotIdentifier=%v",
			instance["skipFinalSnapshot"], instance["finalSnapshotIdentifier"])
	}
	if instance["applyImmediately"] != false {
		t.Error("a prod change should wait for the maintenance window")
	}
}

func TestRDSKeepsTheMasterPasswordAndTheStackNeverSeesIt(t *testing.T) {
	// Pulumi standards MUST-3. RDS generates the master password, stores it in a secret of its
	// own and rotates it; the stack sends no password and receives only the secret's ARN.
	var arn string
	rec := run(t, func(ctx *pulumi.Context) error {
		db, err := components.NewDatabase(ctx, cfg, databaseArgs())
		if err != nil {
			return err
		}
		infratest.Capture(ctx, "master", db.MasterSecretARN, &arn)
		return nil
	})
	instance := rec.Find(t, instanceType, "postgres")
	if instance.Inputs["manageMasterUserPassword"] != true {
		t.Error("manageMasterUserPassword is not on")
	}
	for _, field := range []string{"password", "passwordWo"} {
		if _, ok := instance.Inputs[field]; ok {
			t.Errorf("the instance is sent a %s", field)
		}
	}
	for _, res := range rec.Resources() {
		if strings.HasPrefix(res.Type, "random:") || len(res.Secret) != 0 {
			t.Errorf("%s %q puts a secret value in state", res.Type, res.Name)
		}
	}
	if arn != infratest.MasterSecretARN {
		t.Errorf("MasterSecretARN = %q", arn)
	}
}

func TestTheServiceLogsInWithIAMAuthentication(t *testing.T) {
	instance := newDatabase(t, cfg).Find(t, instanceType, "postgres").Inputs
	if instance["iamDatabaseAuthenticationEnabled"] != true {
		t.Error("IAM database authentication is off, so the service's token would be refused")
	}
	if instance["username"] != stackconfig.MasterUsername {
		t.Errorf("master username = %v", instance["username"])
	}
}

func TestTheRDSIdentifierFitsWhatRDSAccepts(t *testing.T) {
	long := cfg
	long.Slug = "a-very-long-service-name-that-goes-on-and-on-and-on-for-ever-and-ever"
	id, err := long.DBIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) > stackconfig.MaxDBIdentifier || !regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`).MatchString(id) {
		t.Errorf("identifier %q", id)
	}
}

func TestTheMigrationTaskGetsTheDatabaseAndNothingElse(t *testing.T) {
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewMigrations(ctx, cfg, components.MigrationsArgs{
			Image:            pulumi.String("registry/" + slug + ":0123abc"),
			ExecutionRoleARN: pulumi.String("arn:migrate-exec"),
			EntryPoint:       []string{"/app/api"},
			Command:          []string{"migrate", "up"},
			Env:              map[string]pulumi.StringInput{"ENVIRONMENT": pulumi.String("staging"), "DATABASE_HOST": pulumi.String("db.internal")},
			SecretEnv:        map[string]string{"DATABASE_PASSWORD": "master-password"},
			SecretRefs: map[string]components.SecretRef{
				"master-password": components.RDSManagedKey(pulumi.String("arn:master"), "password"),
				"jwt-secret":      {ARN: pulumi.String("arn:jwt"), VersionID: pulumi.String("v1")},
			},
			LogGroup: pulumi.String("/ecs/" + slug + "-migrate-dev"),
		})
		return err
	})
	task := rec.Find(t, "aws:ecs/taskDefinition:TaskDefinition", "migrate").Inputs
	if task["family"] != slug+"-migrate-dev" || task["networkMode"] != "awsvpc" {
		t.Errorf("family=%v networkMode=%v", task["family"], task["networkMode"])
	}
	// No task role: a migration logs in with a password ECS resolved before it started and calls
	// no AWS API, so an AWS identity would be a permission waiting for a use.
	if _, ok := task["taskRoleArn"]; ok {
		t.Errorf("the migration task has a task role: %v", task["taskRoleArn"])
	}
	if task["executionRoleArn"] != "arn:migrate-exec" {
		t.Errorf("executionRoleArn = %v", task["executionRoleArn"])
	}
	var definitions []map[string]any
	if err := json.Unmarshal([]byte(task["containerDefinitions"].(string)), &definitions); err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0]["name"] != components.MigrateContainer {
		t.Fatalf("container definitions = %v", definitions)
	}
	env := infratest.EnvOf(definitions[0])
	if env["DATABASE_PASSWORD"] != "secret:arn:master:password::" || env["DATABASE_HOST"] != "db.internal" {
		t.Errorf("env = %v", env)
	}
	if _, ok := env["JWT_SECRET"]; ok {
		t.Error("the migration task holds the signing key")
	}
	if got := infratest.Get(definitions[0], "logConfiguration", "options", "awslogs-group"); got != "/ecs/"+slug+"-migrate-dev" {
		t.Errorf("awslogs-group = %v", got)
	}
}

// --- Who may read the master secret, and connect as whom --------------------------------------

func databaseIdentities(t *testing.T) *infratest.Recorder {
	t.Helper()
	return run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewIdentities(ctx, cfg, components.IdentitiesArgs{
			SecretARNs:         []pulumi.StringOutput{pulumi.String("arn:jwt").ToStringOutput()},
			MasterSecretARN:    pulumi.String(infratest.MasterSecretARN),
			DatabaseResourceID: pulumi.String(infratest.DatabaseResourceID),
			DatabaseAppRole:    "app_iam",
		})
		return err
	})
}

func TestOnlyTheMigrationTasksExecutionRoleReadsTheMasterSecret(t *testing.T) {
	// The schema owner's password. The backend's execution role resolves the service's secrets and
	// must not be able to resolve this one; the migration task's role resolves this and nothing else.
	rec := databaseIdentities(t)
	migrate := statements(t, rec.Find(t, policyType, "migrate-execution-reads-master-secret").Inputs["policy"])
	if len(migrate) != 1 || !slices.Equal(strs(infratest.Get(migrate[0], "Resource")), []string{infratest.MasterSecretARN}) ||
		!slices.Equal(strs(infratest.Get(migrate[0], "Action")), []string{"secretsmanager:GetSecretValue"}) {
		t.Errorf("migrate execution policy = %v", migrate)
	}
	for _, policy := range rec.OfType(policyType) {
		if policy.Name != "migrate-execution-reads-master-secret" && strings.Contains(policy.Inputs["policy"].(string), infratest.MasterSecretARN) {
			t.Errorf("%s grants the master secret", policy.Name)
		}
	}
	backend := statements(t, rec.Find(t, policyType, "execution-reads-secrets").Inputs["policy"])
	if got := strs(infratest.Get(backend[0], "Resource")); !slices.Equal(got, []string{"arn:jwt"}) {
		t.Errorf("the backend's execution role reads %v", got)
	}
	migrateRole := rec.Find(t, roleType, "migrate-execution-role")
	trust := statements(t, migrateRole.Inputs["assumeRolePolicy"])
	if infratest.Get(trust[0], "Condition", "StringEquals", "aws:SourceAccount") != infratest.AccountID {
		t.Error("the migration execution role has no aws:SourceAccount guard")
	}
}

func TestTheRuntimeRoleMayConnectAsTheAppRoleOnThisInstanceOnly(t *testing.T) {
	rec := databaseIdentities(t)
	runtime := rec.Find(t, roleType, "runtime-role")
	var grants []any
	for _, policy := range rec.OfType(policyType) {
		if policy.Inputs["role"] == runtime.Inputs["name"] {
			grants = append(grants, statements(t, policy.Inputs["policy"])...)
		}
	}
	want := "arn:aws:rds-db:" + infratest.Region + ":" + infratest.AccountID + ":dbuser:" + infratest.DatabaseResourceID + "/app_iam"
	if len(grants) != 1 || !slices.Equal(strs(infratest.Get(grants[0], "Action")), []string{"rds-db:connect"}) ||
		!slices.Equal(strs(infratest.Get(grants[0], "Resource")), []string{want}) {
		t.Errorf("runtime grants = %v; want rds-db:connect on %s alone", grants, want)
	}
	for _, attachment := range rec.OfType("aws:iam/rolePolicyAttachment:RolePolicyAttachment") {
		if attachment.Inputs["role"] == runtime.Inputs["name"] {
			t.Errorf("the runtime role has %v attached", attachment.Inputs["policyArn"])
		}
	}
}

func TestTheAppRoleIsAPlainIdentifierAndNotTheMaster(t *testing.T) {
	// It is spelled into an IAM ARN, where a wildcard would grant every user on the instance.
	for _, bad := range []string{"*", "app_*", "App", "app-iam", stackconfig.MasterUsername, ""} {
		c := cfg
		c.DatabaseAppRole = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "databaseAppRole") {
			t.Errorf("databaseAppRole %q: Validate() = %v", bad, err)
		}
	}
	if cfg.DatabaseAppRole != "app_iam" {
		t.Errorf("databaseAppRole defaults to %q, want app_iam", cfg.DatabaseAppRole)
	}
}
