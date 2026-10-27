// Removed by the post-generation hook when use_postgres is "no", together with database.go.
package components_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/components"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
)

const instanceType = "gcp:sql/databaseInstance:DatabaseInstance"

func newDatabase(t *testing.T) (*infratest.Recorder, *components.Database) {
	t.Helper()
	var db *components.Database
	rec := run(t, func(ctx *pulumi.Context) error {
		var err error
		db, err = components.NewDatabase(ctx, cfg, components.DatabaseArgs{AppServiceAccount: pulumi.String(runtimeEmail)})
		return err
	})
	return rec, db
}

func TestTheDatabaseAcceptsNoDirectConnections(t *testing.T) {
	// An authorised network here would expose Postgres to the internet. With none, and connectors
	// required, there is no route in at all: every client goes through a Cloud SQL connector,
	// authenticated by IAM. The GCP infrastructure standards grade it: a public-IP instance has zero
	// authorised networks and connector enforcement REQUIRED.
	rec, _ := newDatabase(t)
	settings := infratest.Get(rec.Find(t, instanceType, "postgres").Inputs, "settings")
	ip := infratest.Get(settings, "ipConfiguration")
	if networks, _ := infratest.Get(ip, "authorizedNetworks").([]any); len(networks) != 0 {
		t.Errorf("authorizedNetworks = %v", networks)
	}
	if got := infratest.Get(ip, "sslMode"); got != "ENCRYPTED_ONLY" {
		t.Errorf("sslMode = %v", got)
	}
	if got := infratest.Get(settings, "connectorEnforcement"); got != "REQUIRED" {
		t.Errorf("connectorEnforcement = %v, want REQUIRED", got)
	}
}

func TestTheInstanceAcceptsIAMDatabaseAuthentication(t *testing.T) {
	// Without the flag the service's IAM database user exists and cannot log in.
	rec, _ := newDatabase(t)
	flags, _ := infratest.Get(rec.Find(t, instanceType, "postgres").Inputs, "settings", "databaseFlags").([]any)
	if !slices.ContainsFunc(flags, func(flag any) bool {
		return infratest.Get(flag, "name") == components.IAMAuthenticationFlag && infratest.Get(flag, "value") == "on"
	}) {
		t.Errorf("databaseFlags = %v; want %s=on", flags, components.IAMAuthenticationFlag)
	}
}

func TestTheServiceLogsInAsItsOwnAccountWithNoPassword(t *testing.T) {
	// The service account, minus .gserviceaccount.com, is the name Cloud SQL gives its IAM user.
	// The user type has no password at all, so nothing secret is in state.
	rec, _ := newDatabase(t)
	user := rec.Find(t, "gcp:sql/user:User", "app-user")
	if user.Inputs["type"] != "CLOUD_IAM_SERVICE_ACCOUNT" {
		t.Errorf("type = %v", user.Inputs["type"])
	}
	if want := "runtime@test-project.iam"; user.Inputs["name"] != want {
		t.Errorf("name = %v, want %s", user.Inputs["name"], want)
	}
	if _, ok := user.Inputs["password"]; ok {
		t.Error("the IAM database user has a password")
	}
	if len(rec.OfType("gcp:sql/user:User")) != 1 {
		t.Error("the stack creates a database user besides the service's IAM user")
	}
	if got := components.IAMDatabaseUser("api-run-dev@p.iam.gserviceaccount.com"); got != "api-run-dev@p.iam" {
		t.Errorf("IAMDatabaseUser = %q", got)
	}
}

func TestTheStackHoldsNoDatabasePassword(t *testing.T) {
	// Pulumi standards MUST-3. The admin login's password is set out of band by `just
	// infra-secrets`; nothing in this program generates, stores or passes one.
	rec, _ := newDatabase(t)
	for _, res := range rec.Resources() {
		if strings.HasPrefix(res.Type, "random:") {
			t.Errorf("%s %q generates a value into state", res.Type, res.Name)
		}
		if len(res.Secret) != 0 {
			t.Errorf("%s %q carries secret inputs %v", res.Type, res.Name, res.Secret)
		}
	}
	if _, ok := rec.Find(t, instanceType, "postgres").Inputs["rootPassword"]; ok {
		t.Error("the instance sets a root password")
	}
}

func TestTheIAMUserIsDroppedAfterTheDatabaseAndNotBesideIt(t *testing.T) {
	// The one ordering in this stack that only matters on the way out. Pulumi deletes a dependency
	// graph backwards, so `database depends on user` is what makes the database go first and the
	// role second. Without the edge they are deleted concurrently, Cloud SQL drops the role while
	// the database still holds the grants the migration job gave it, Postgres refuses, and the
	// destroy stops with the instance still running and still billing.
	rec, _ := newDatabase(t)
	database := rec.Find(t, "gcp:sql/database:Database", "database")
	if !slices.ContainsFunc(database.Dependencies, func(urn string) bool {
		return strings.HasSuffix(urn, "gcp:sql/user:User::app-user")
	}) {
		t.Errorf("sql.Database must depend on the IAM sql.User; dependencies = %v", database.Dependencies)
	}
}

func TestTheDatabaseUsesTheCheapestHonestTier(t *testing.T) {
	rec, _ := newDatabase(t)
	settings := infratest.Get(rec.Find(t, instanceType, "postgres").Inputs, "settings")
	want := map[string]any{
		"tier":             "db-f1-micro",
		"edition":          "ENTERPRISE",
		"availabilityType": "ZONAL",
		"diskType":         "PD_HDD",
		"diskSize":         10.0,
	}
	for key, value := range want {
		if got := infratest.Get(settings, key); got != value {
			t.Errorf("%s = %v, want %v", key, got, value)
		}
	}
	// Point-in-time recovery keeps write-ahead logs, which are billed storage. Production only.
	if got := infratest.Get(settings, "backupConfiguration", "pointInTimeRecoveryEnabled"); got != false {
		t.Errorf("pointInTimeRecoveryEnabled = %v on a dev stack", got)
	}
}

func TestProductionProtectsTheDatabaseFromADestroy(t *testing.T) {
	prod := cfg
	prod.Environment = "prod"
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewDatabase(ctx, prod, components.DatabaseArgs{AppServiceAccount: pulumi.String(runtimeEmail)})
		return err
	})
	instance := rec.Find(t, instanceType, "postgres")
	if instance.Inputs["deletionProtection"] != true {
		t.Error("a prod database without deletion protection is one `pulumi destroy` from gone")
	}
}

// --- Who may do what --------------------------------------------------------------------------

func TestTheMigratorCannotDeployOrLogInWithIAM(t *testing.T) {
	// It connects as the admin login through Cloud Run's Cloud SQL connection; instanceUser would be
	// a second, unused way in, and deploy roles would let a migration change what runs.
	for _, role := range []string{"roles/cloudsql.instanceUser", "roles/run.developer", "roles/artifactregistry.writer", "roles/cloudsql.admin"} {
		if slices.Contains(components.MigratorRoles, role) {
			t.Errorf("the migrator holds %s", role)
		}
	}
	if !slices.Contains(components.MigratorRoles, "roles/cloudsql.client") {
		t.Error("the migrator cannot open a connection to the instance")
	}
}

func TestTheRuntimeAccountLogsInWithIAMAndHoldsNoAdminRole(t *testing.T) {
	for _, role := range []string{"roles/cloudsql.client", "roles/cloudsql.instanceUser"} {
		if !slices.Contains(components.RuntimeRoles, role) {
			t.Errorf("the runtime account lacks %s, so IAM database authentication cannot work", role)
		}
	}
	if slices.Contains(components.RuntimeRoles, "roles/cloudsql.admin") {
		t.Error("the runtime account administers Cloud SQL")
	}
}

func TestCIMayDeployTheMigrationJobAsTheMigrator(t *testing.T) {
	// CD updates the job's image, and the job runs as the migrator: without this grant the update
	// fails with a permission error that never mentions impersonation.
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewIdentities(ctx, cfg, components.IdentitiesArgs{
			ProjectNumber: pulumi.String(infratest.ProjectNumber),
		})
		return err
	})
	grant := rec.Find(t, "gcp:serviceaccount/iAMMember:IAMMember", "ci-acts-as-migrator")
	if grant.Inputs["role"] != "roles/iam.serviceAccountUser" || !strings.Contains(grant.Inputs["serviceAccountId"].(string), "-migrate-dev") {
		t.Errorf("ci-acts-as-migrator = %v", grant.Inputs)
	}
}
