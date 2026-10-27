package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// Log retention, in days. CloudWatch Logs bills for every byte stored, and a group created
// without a retention keeps everything forever.
const (
	LogRetentionDays           = 30
	ProductionLogRetentionDays = 90
)

// LogsArgs is what Logs needs beyond the configuration.
type LogsArgs struct {
	// Migrate adds the migration task's group. It follows use_postgres.
	Migrate bool
}

// Logs is the CloudWatch log groups the stack's containers write to.
//
// The stack creates them, rather than letting Express Mode create its default group, for two
// reasons that both end in a bill nobody expected. Express Mode's own group has no retention, so
// it keeps every line forever; and it is created by the service rather than by this stack, so a
// `pulumi destroy` leaves it behind, still storing, with nothing that names it. A group declared
// here has a retention and goes with the stack.
type Logs struct {
	pulumi.ResourceState

	Backend *cloudwatch.LogGroup
	// Migrate is nil unless asked for.
	Migrate *cloudwatch.LogGroup
}

// NewLogs creates the log groups.
func NewLogs(ctx *pulumi.Context, cfg stackconfig.StackConfig, args LogsArgs, opts ...pulumi.ResourceOption) (*Logs, error) {
	c := &Logs{}
	if err := ctx.RegisterComponentResource("service:infra:Logs", cfg.Name("logs"), c, opts...); err != nil {
		return nil, err
	}

	retention := LogRetentionDays
	if cfg.IsProduction() {
		retention = ProductionLogRetentionDays
	}
	group := func(name, app string) (*cloudwatch.LogGroup, error) {
		return cloudwatch.NewLogGroup(ctx, name, &cloudwatch.LogGroupArgs{
			Name:            pulumi.String("/ecs/" + cfg.Name(app)),
			RetentionInDays: pulumi.Int(retention),
		}, child(c, opts)...)
	}

	var err error
	if c.Backend, err = group("backend-logs", cfg.Slug); err != nil {
		return nil, err
	}
	if args.Migrate {
		if c.Migrate, err = group("migrate-logs", cfg.Slug+"-migrate"); err != nil {
			return nil, err
		}
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"backend": c.Backend.Name})
}
