// Removed by the post-generation hook when use_postgres is "no", together with database.go.
package components_test

import (
	"encoding/json"
	"regexp"
	"slices"
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

func TestTheDatabaseDSNIsAPgxURLRequiringTLS(t *testing.T) {
	// The backend parses DATABASE_URL_OVERRIDE with pgx. sslmode=require because the server
	// refuses anything else; not verify-full, because the RDS CA is not in the image.
	var dsn string
	run(t, func(ctx *pulumi.Context) error {
		db, err := components.NewDatabase(ctx, cfg, databaseArgs())
		if err != nil {
			return err
		}
		infratest.Capture(ctx, "dsn", db.DatabaseURL, &dsn)
		return nil
	})
	pattern := regexp.MustCompile(`^postgres://app:[A-Za-z0-9_.~-]+@[a-z0-9.-]+\.rds\.amazonaws\.com:5432/app\?sslmode=require$`)
	if !pattern.MatchString(dsn) {
		t.Errorf("DSN %q is not a pgx TCP URL requiring TLS", dsn)
	}
}

func TestTheDatabasePasswordIsGeneratedAndSecret(t *testing.T) {
	rec := newDatabase(t, cfg)
	password := rec.Find(t, "random:index/randomPassword:RandomPassword", "db-password")
	if password.Inputs["length"] != 32.0 {
		t.Errorf("password length = %v", password.Inputs["length"])
	}
	if !slices.Contains(rec.Find(t, instanceType, "postgres").Secret, "password") {
		t.Error("the database password reaches the provider as plaintext rather than as a secret")
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
			ExecutionRoleARN: pulumi.String("arn:exec"),
			TaskRoleARN:      pulumi.String("arn:runtime"),
			EntryPoint:       []string{"/app/api"},
			Command:          []string{"migrate", "up"},
			Env:              map[string]string{"ENVIRONMENT": "staging"},
			SecretEnv:        map[string]string{"DATABASE_URL_OVERRIDE": "database-url"},
			SecretRefs: map[string]components.SecretRef{
				"database-url": {ARN: pulumi.String("arn:db"), VersionID: pulumi.String("v1")},
				"jwt-secret":   {ARN: pulumi.String("arn:jwt"), VersionID: pulumi.String("v1")},
			},
			LogGroup: pulumi.String("/ecs/" + slug + "-migrate-dev"),
		})
		return err
	})
	task := rec.Find(t, "aws:ecs/taskDefinition:TaskDefinition", "migrate").Inputs
	if task["family"] != slug+"-migrate-dev" || task["networkMode"] != "awsvpc" {
		t.Errorf("family=%v networkMode=%v", task["family"], task["networkMode"])
	}
	var definitions []map[string]any
	if err := json.Unmarshal([]byte(task["containerDefinitions"].(string)), &definitions); err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0]["name"] != components.MigrateContainer {
		t.Fatalf("container definitions = %v", definitions)
	}
	env := infratest.EnvOf(definitions[0])
	if env["DATABASE_URL_OVERRIDE"] != "secret:arn:db:::v1" {
		t.Errorf("DATABASE_URL_OVERRIDE = %q", env["DATABASE_URL_OVERRIDE"])
	}
	if _, ok := env["JWT_SECRET"]; ok {
		t.Error("the migration task holds the signing key")
	}
	if got := infratest.Get(definitions[0], "logConfiguration", "options", "awslogs-group"); got != "/ecs/"+slug+"-migrate-dev" {
		t.Errorf("awslogs-group = %v", got)
	}
}
