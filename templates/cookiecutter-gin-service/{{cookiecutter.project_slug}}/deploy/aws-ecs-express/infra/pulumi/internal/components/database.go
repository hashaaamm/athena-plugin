package components

import (
	"errors"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/rds"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const (
	// DBName is the logical database. The service and the migration task both name it.
	DBName = "app"

	// PostgresFamily is the parameter-group family for EngineVersion. They move together: a major
	// version upgrade is a new family and a new parameter group.
	PostgresFamily = "postgres16"
	EngineVersion  = "16"

	// CACertificate is the RDS certificate authority the instance's server certificate is issued
	// from. Named rather than left to the account default, so a default that changes does not
	// rotate the certificate under a running service.
	CACertificate = "rds-ca-rsa2048-g1"
)

// DatabaseArgs is what Database needs beyond the configuration.
type DatabaseArgs struct {
	// SubnetIDs is where the instance may be placed: the network's isolated subnets, which have
	// no route out and no route in except from inside the VPC.
	SubnetIDs pulumi.StringArrayInput
	// SecurityGroupID is the database's own group, which admits Postgres from the app tasks only.
	SecurityGroupID pulumi.StringInput
}

// Database is RDS for PostgreSQL.
//
// The cost decision: db.t4g.micro on 20 GB of gp3 in one AZ is the floor for a managed Postgres
// on AWS, and it bills by the hour whether or not anybody uses it. infra/README.md has what each
// setting is and what moving off the floor buys.
//
// The security decisions worth understanding:
//
//   - There is no public endpoint and no route to one. The instance sits in isolated subnets,
//     `publiclyAccessible` is off, and its security group admits port 5432 from the app tasks'
//     group and from nothing else — not a CIDR, which would admit whatever lands in those subnets
//     next.
//   - Two database identities, and no password in this program, its configuration or its state
//     (Pulumi standards MUST-3, ADR-0023). The service logs in as databaseAppRole with an IAM token
//     its task role signs — `iamDatabaseAuthenticationEnabled` — and holds row privileges only. The
//     migration task logs in as the master user, which owns the schema, with the password RDS
//     generates, keeps in a Secrets Manager secret of its own and rotates —
//     `manageMasterUserPassword`. The stack only ever sees that secret's ARN.
type Database struct {
	pulumi.ResourceState

	Instance *rds.Instance

	// Address is the instance's hostname: DATABASE_HOST for the service and the migration task.
	Address pulumi.StringOutput

	// ResourceID is the instance's `db-…` resource id, the part of an rds-db:connect ARN that
	// names the instance.
	ResourceID pulumi.StringOutput

	// MasterSecretARN is the secret RDS keeps the master login in, as JSON with `username` and
	// `password` keys. Only the migration task's execution role may read it.
	MasterSecretARN pulumi.StringOutput

	// Identifier is the RDS instance identifier, exported as rds_instance for a human with the
	// console open.
	Identifier pulumi.StringOutput
}

// NewDatabase creates the subnet group, the parameter group and the instance.
func NewDatabase(ctx *pulumi.Context, cfg stackconfig.StackConfig, args DatabaseArgs, opts ...pulumi.ResourceOption) (*Database, error) {
	identifier, err := cfg.DBIdentifier()
	if err != nil {
		return nil, err
	}

	c := &Database{}
	if err := ctx.RegisterComponentResource("service:infra:Database", cfg.Name("database"), c, opts...); err != nil {
		return nil, err
	}

	subnets, err := rds.NewSubnetGroup(ctx, "db-subnets", &rds.SubnetGroupArgs{
		Name:        pulumi.String(cfg.Name(cfg.Slug)),
		Description: pulumi.String("Isolated subnets for " + cfg.Slug + "'s database"),
		SubnetIds:   args.SubnetIDs,
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	// rds.force_ssl refuses a plaintext connection at the server, which is the only place that
	// cannot be argued with: a client that forgets `sslmode` fails to connect rather than
	// connecting in the clear. An IAM login over plaintext is refused regardless.
	parameters, err := rds.NewParameterGroup(ctx, "db-parameters", &rds.ParameterGroupArgs{
		Name:        pulumi.String(cfg.Name(cfg.Slug, PostgresFamily)),
		Family:      pulumi.String(PostgresFamily),
		Description: pulumi.String("TLS-only Postgres for " + cfg.Slug),
		Parameters: rds.ParameterGroupParameterArray{
			&rds.ParameterGroupParameterArgs{
				Name:  pulumi.String("rds.force_ssl"),
				Value: pulumi.String("1"),
			},
		},
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	instance := &rds.InstanceArgs{
		Identifier:              pulumi.String(identifier),
		Engine:                  pulumi.String("postgres"),
		EngineVersion:           pulumi.String(EngineVersion),
		AutoMinorVersionUpgrade: pulumi.Bool(true),
		InstanceClass:           pulumi.String(cfg.DatabaseInstanceClass),
		AllocatedStorage:        pulumi.Int(cfg.DatabaseStorageGB),
		// Grows on its own up to the ceiling, and never shrinks back — which is why it starts at
		// the floor.
		MaxAllocatedStorage: pulumi.Int(stackconfig.MaxDatabaseStorageGB),
		StorageType:         pulumi.String("gp3"),
		StorageEncrypted:    pulumi.Bool(true),
		// A standby in a second AZ is high availability at roughly double the instance charge.
		MultiAz:             pulumi.Bool(cfg.DatabaseMultiAZ),
		PubliclyAccessible:  pulumi.Bool(false),
		DbSubnetGroupName:   subnets.Name,
		VpcSecurityGroupIds: pulumi.StringArray{args.SecurityGroupID},
		ParameterGroupName:  parameters.Name,
		CaCertIdentifier:    pulumi.String(CACertificate),
		DbName:              pulumi.String(DBName),
		Username:            pulumi.String(stackconfig.MasterUsername),
		// RDS generates the master password, stores it in a secret of its own — encrypted with the
		// account's aws/secretsmanager key, since no key is named — and rotates it. No password
		// field: the two are mutually exclusive, and this one would put the value in state.
		ManageMasterUserPassword: pulumi.Bool(true),
		// The service's login: a token its task role signs, checked by RDS against rds-db:connect.
		IamDatabaseAuthenticationEnabled: pulumi.Bool(true),
		// Seven days of automated backups and point-in-time recovery, in a window before the
		// maintenance one so the two never overlap.
		BackupRetentionPeriod: pulumi.Int(7),
		BackupWindow:          pulumi.String("03:00-03:30"),
		MaintenanceWindow:     pulumi.String("sun:04:00-sun:04:30"),
		CopyTagsToSnapshot:    pulumi.Bool(true),
		// Guard rail, not paranoia: a `pulumi destroy` that takes the database with it is the one
		// mistake with no undo. Flip it deliberately when you actually mean it.
		DeletionProtection: pulumi.Bool(cfg.IsProduction()),
		// Outside production a destroy should leave nothing behind, a final snapshot included —
		// a snapshot bills for storage, indefinitely, and nobody remembers it exists. The master
		// secret goes with the instance either way: RDS deletes the secret it manages.
		SkipFinalSnapshot: pulumi.Bool(!cfg.IsProduction()),
		// A change to the instance waits for the maintenance window in production, and happens
		// now everywhere else.
		ApplyImmediately: pulumi.Bool(!cfg.IsProduction()),
	}
	if cfg.IsProduction() {
		instance.FinalSnapshotIdentifier = pulumi.String(identifier + "-final")
	}

	c.Instance, err = rds.NewInstance(ctx, "postgres", instance, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	c.Identifier = c.Instance.Identifier
	c.Address = c.Instance.Address
	c.ResourceID = c.Instance.ResourceId
	c.MasterSecretARN = c.Instance.MasterUserSecrets.ApplyT(func(secrets []rds.InstanceMasterUserSecret) (string, error) {
		if len(secrets) == 0 || secrets[0].SecretArn == nil {
			return "", errors.New("RDS reported no master user secret, though manageMasterUserPassword is on")
		}
		return *secrets[0].SecretArn, nil
	}).(pulumi.StringOutput)

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"identifier": c.Identifier})
}
