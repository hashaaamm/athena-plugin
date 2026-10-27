package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// Cluster is the ECS cluster every Express service and the migration task run in.
//
// Named and owned by this stack rather than left as the account's `default` cluster, which Express
// Mode would otherwise use: a cluster shared with whatever else lives in the account is a
// permission boundary nobody drew, and the deploy role's grants are scoped to this one by name.
type Cluster struct {
	pulumi.ResourceState

	Cluster *ecs.Cluster
	Name    pulumi.StringOutput
	ARN     pulumi.StringOutput
}

// NewCluster creates the cluster.
func NewCluster(ctx *pulumi.Context, cfg stackconfig.StackConfig, opts ...pulumi.ResourceOption) (*Cluster, error) {
	c := &Cluster{}
	if err := ctx.RegisterComponentResource("service:infra:Cluster", cfg.Name("cluster"), c, opts...); err != nil {
		return nil, err
	}

	// Container Insights bills per metric per task. Enhanced observability is worth that in
	// production, where somebody is paged on it; outside it, the service's own logs and the load
	// balancer's metrics answer every question a dev stack is asked.
	insights := "disabled"
	if cfg.IsProduction() {
		insights = "enhanced"
	}

	cluster, err := ecs.NewCluster(ctx, "cluster", &ecs.ClusterArgs{
		Name: pulumi.String(cfg.Name(cfg.Slug)),
		Settings: ecs.ClusterSettingArray{
			&ecs.ClusterSettingArgs{
				Name:  pulumi.String("containerInsights"),
				Value: pulumi.String(insights),
			},
		},
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	c.Cluster = cluster
	c.Name = cluster.Name
	c.ARN = cluster.Arn
	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"name": c.Name})
}
