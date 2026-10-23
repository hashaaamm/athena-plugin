// Removed by the post-generation hook when use_postgres is "no", together with database.go.
package components_test

import (
	"regexp"
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
		db, err = components.NewDatabase(ctx, cfg)
		return err
	})
	return rec, db
}

func TestTheDatabaseAcceptsNoDirectConnections(t *testing.T) {
	// An authorised network here would expose Postgres to the internet. With none, there is no
	// route in at all: Cloud Run reaches it over the connector's Unix socket, authenticated by IAM.
	rec, _ := newDatabase(t)
	ip := infratest.Get(rec.Find(t, instanceType, "postgres").Inputs, "settings", "ipConfiguration")
	if networks, _ := infratest.Get(ip, "authorizedNetworks").([]any); len(networks) != 0 {
		t.Errorf("authorizedNetworks = %v", networks)
	}
	if got := infratest.Get(ip, "sslMode"); got != "ENCRYPTED_ONLY" {
		t.Errorf("sslMode = %v", got)
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

func TestTheRoleIsDroppedAfterTheDatabaseAndNotBesideIt(t *testing.T) {
	// The one ordering in this stack that only matters on the way out. Pulumi deletes a dependency
	// graph backwards, so `database depends on user` is what makes the database go first and the
	// role second. Without the edge they are deleted concurrently, Cloud SQL runs DROP ROLE while
	// the database still holds the tables goose created, Postgres refuses, and the destroy stops
	// with the instance still running and still billing.
	rec, _ := newDatabase(t)
	database := rec.Find(t, "gcp:sql/database:Database", "database")
	if !slices.ContainsFunc(database.Dependencies, func(urn string) bool {
		return strings.HasSuffix(urn, "gcp:sql/user:User::db-user")
	}) {
		t.Errorf("sql.Database must depend on sql.User; dependencies = %v", database.Dependencies)
	}
}

func TestTheDatabaseDSNIsAPgxURLOverTheConnectorSocket(t *testing.T) {
	// The backend parses DATABASE_URL_OVERRIDE with pgx. A SQLAlchemy-style `+driver` scheme or a
	// TCP host would parse, and then fail to connect from inside Cloud Run, where the only way in
	// is the Unix socket the connector mounts at /cloudsql.
	var dsn string
	run(t, func(ctx *pulumi.Context) error {
		db, err := components.NewDatabase(ctx, cfg)
		if err != nil {
			return err
		}
		infratest.Capture(db.DatabaseURL, &dsn)
		return nil
	})
	pattern := regexp.MustCompile(`^postgres://app:[A-Za-z0-9_.~-]+@/app\?host=/cloudsql/` +
		regexp.QuoteMeta(infratest.Project) + `:europe-west1:[a-z0-9-]+$`)
	if !pattern.MatchString(dsn) {
		t.Errorf("DSN %q is not a pgx Unix-socket URL", dsn)
	}
}

func TestTheDatabaseDSNIsGeneratedRatherThanTyped(t *testing.T) {
	// A password a human chose is a password a human can reuse. This one only ever travels from
	// Secret Manager into the runtime environment, and nobody ever sees it.
	rec, _ := newDatabase(t)
	password := rec.Find(t, "random:index/randomPassword:RandomPassword", "db-password")
	if password.Inputs["length"] != 32.0 {
		t.Errorf("password length = %v", password.Inputs["length"])
	}
	user := rec.Find(t, "gcp:sql/user:User", "db-user")
	if !slices.Contains(user.Secret, "password") {
		t.Error("the database password reaches the provider as plaintext rather than as a secret")
	}
}

func TestProductionProtectsTheDatabaseFromADestroy(t *testing.T) {
	prod := cfg
	prod.Environment = "prod"
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewDatabase(ctx, prod)
		return err
	})
	instance := rec.Find(t, instanceType, "postgres")
	if instance.Inputs["deletionProtection"] != true {
		t.Error("a prod database without deletion protection is one `pulumi destroy` from gone")
	}
}
