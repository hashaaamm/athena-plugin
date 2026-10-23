package components

import (
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/sql"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// DBName is the logical database and the role that owns it. One name, used in both places and in
// the DSN.
const DBName = "app"

// Database is Cloud SQL for PostgreSQL.
//
// The cost decision: a shared-core instance with 10 GB of HDD is the floor for managed Postgres on
// GCP, and it is the only standing charge in this stack. Everything else scales to zero. Every
// setting below that has a cheaper-looking alternative is already at its minimum; infra/README.md
// has the table of what each one is and what the floor costs you — shared-core and single-zone are
// both outside the Cloud SQL SLA, which is the price of it.
//
// The security decision worth understanding: the instance keeps a public IP with ZERO authorised
// networks. That reads as a mistake and is not. Cloud Run reaches it through the Cloud SQL
// connector, which authenticates with IAM over a Unix socket and does not traverse the public
// internet; with no authorised networks there is no route for anything else, so the address is
// unreachable rather than merely unguarded. The alternative — private IP — needs a VPC connector
// or Direct VPC egress, which costs more per month than the database.
type Database struct {
	pulumi.ResourceState

	Instance *sql.DatabaseInstance
	Password *random.RandomPassword
	User     *sql.User
	Database *sql.Database

	// DatabaseURL is the pgx DSN the service reads as DATABASE_URL_OVERRIDE.
	// `?host=/cloudsql/<connection>` is the Unix socket the Cloud Run connector mounts — there is
	// no hostname to resolve and no port to open. A secret output: it carries the password.
	DatabaseURL pulumi.StringOutput

	// ConnectionName is `project:region:instance`, what Cloud Run's Cloud SQL volume names.
	ConnectionName pulumi.StringOutput
}

// NewDatabase creates the instance, a generated password, the role and the database.
func NewDatabase(ctx *pulumi.Context, cfg stackconfig.StackConfig, opts ...pulumi.ResourceOption) (*Database, error) {
	c := &Database{}
	if err := ctx.RegisterComponentResource("service:infra:Database", cfg.Name("database"), c, opts...); err != nil {
		return nil, err
	}

	var err error
	c.Instance, err = sql.NewDatabaseInstance(ctx, "postgres", &sql.DatabaseInstanceArgs{
		Project:         pulumi.String(cfg.Project),
		Region:          pulumi.String(cfg.Region),
		Name:            pulumi.String(cfg.Name(cfg.Slug, "pg")),
		DatabaseVersion: pulumi.String("POSTGRES_16"),
		// Guard rail, not paranoia: a `pulumi destroy` that takes the database with it is the one
		// mistake with no undo. Flip this deliberately when you actually mean it.
		DeletionProtection: pulumi.Bool(cfg.IsProduction()),
		Settings: &sql.DatabaseInstanceSettingsArgs{
			Tier:             pulumi.String(cfg.DatabaseTier),
			Edition:          pulumi.String("ENTERPRISE"),
			AvailabilityType: pulumi.String("ZONAL"), // REGIONAL doubles the cost
			DiskType:         pulumi.String("PD_HDD"),
			DiskSize:         pulumi.Int(cfg.DatabaseDiskGB),
			DiskAutoresize:   pulumi.Bool(true),
			IpConfiguration: &sql.DatabaseInstanceSettingsIpConfigurationArgs{
				Ipv4Enabled: pulumi.Bool(true),
				// Nothing may connect directly. See the type's documentation.
				AuthorizedNetworks: sql.DatabaseInstanceSettingsIpConfigurationAuthorizedNetworkArray{},
				SslMode:            pulumi.String("ENCRYPTED_ONLY"),
			},
			BackupConfiguration: &sql.DatabaseInstanceSettingsBackupConfigurationArgs{
				Enabled:                    pulumi.Bool(true),
				StartTime:                  pulumi.String("03:00"),
				PointInTimeRecoveryEnabled: pulumi.Bool(cfg.IsProduction()),
				BackupRetentionSettings: &sql.DatabaseInstanceSettingsBackupConfigurationBackupRetentionSettingsArgs{
					RetainedBackups: pulumi.Int(7),
				},
			},
			MaintenanceWindow: &sql.DatabaseInstanceSettingsMaintenanceWindowArgs{
				Day:         pulumi.Int(7),
				Hour:        pulumi.Int(4),
				UpdateTrack: pulumi.String("stable"),
			},
			InsightsConfig: &sql.DatabaseInstanceSettingsInsightsConfigArgs{
				QueryInsightsEnabled: pulumi.Bool(true),
			},
		},
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	// Generated, never chosen. A password a human typed is a password a human can reuse, and this
	// one only ever travels from Secret Manager into the runtime environment.
	c.Password, err = random.NewRandomPassword(ctx, "db-password", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.Bool(true),
		// Keep it URL-safe: this value ends up inside a DSN.
		OverrideSpecial: pulumi.String("-_.~"),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	c.User, err = sql.NewUser(ctx, "db-user", &sql.UserArgs{
		Project:  pulumi.String(cfg.Project),
		Instance: c.Instance.Name,
		Name:     pulumi.String(DBName),
		Password: c.Password.Result,
	}, child(c, opts, pulumi.AdditionalSecretOutputs([]string{"password"}))...)
	if err != nil {
		return nil, err
	}

	// The DependsOn is load-bearing and it is here for the DESTROY, which runs this graph
	// backwards: a dependent is deleted before the thing it depends on, so the database goes first
	// and the role second. Without the edge Pulumi has no ordering between them and deletes both at
	// once. Cloud SQL then runs `DROP ROLE "app"` while the database `app` is still there holding
	// the tables goose created, Postgres refuses with `role "app" cannot be dropped because some
	// objects depend on it`, and the destroy fails with the instance and the role still standing.
	// Sharing an instance and a name is not a dependency Pulumi can infer — `Instance:` ties both of
	// these to the instance, not to each other.
	c.Database, err = sql.NewDatabase(ctx, "database", &sql.DatabaseArgs{
		Project:  pulumi.String(cfg.Project),
		Instance: c.Instance.Name,
		Name:     pulumi.String(DBName),
	}, child(c, opts, pulumi.DependsOn([]pulumi.Resource{c.User}))...)
	if err != nil {
		return nil, err
	}

	c.ConnectionName = c.Instance.ConnectionName
	c.DatabaseURL = pulumi.ToSecret(pulumi.Sprintf(
		"postgres://%s:%s@/%s?host=/cloudsql/%s", DBName, c.Password.Result, DBName, c.ConnectionName,
	)).(pulumi.StringOutput)

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"connectionName": c.ConnectionName})
}
