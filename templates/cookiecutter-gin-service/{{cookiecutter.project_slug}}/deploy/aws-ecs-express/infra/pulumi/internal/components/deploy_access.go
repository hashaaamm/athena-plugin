package components

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// ECRPushActions is what `docker push` needs on a repository, and `docker buildx` reading a layer
// back. Nothing that deletes an image or changes a repository's policy.
var ECRPushActions = []string{
	"ecr:BatchCheckLayerAvailability",
	"ecr:BatchGetImage",
	"ecr:CompleteLayerUpload",
	"ecr:GetDownloadUrlForLayer",
	"ecr:InitiateLayerUpload",
	"ecr:PutImage",
	"ecr:UploadLayerPart",
}

// PassedToServices is who the deploy role may hand a role to. Registering a task definition and
// rolling an Express service both pass roles; this is the condition that stops the same grant
// from passing them to Lambda or EC2.
var PassedToServices = []string{"ecs-tasks.amazonaws.com", "ecs.amazonaws.com"}

// DeployTargets is everything the deploy role may act on, resolved to plain strings.
type DeployTargets struct {
	RepositoryARNs []string
	ServiceARNs    []string
	Cluster        string
	// MigrateFamily and MigrateLogGroupARN are empty without a database.
	MigrateFamily      string
	MigrateLogGroupARN string
	PassRoleARNs       []string
}

// DeployAccessArgs is DeployTargets as the outputs they are before an apply.
type DeployAccessArgs struct {
	Role           *iam.Role
	RepositoryARNs []pulumi.StringOutput
	ServiceARNs    []pulumi.StringOutput
	Cluster        pulumi.StringOutput
	// MigrateFamily and MigrateLogGroupARN are nil without a database.
	MigrateFamily      pulumi.StringInput
	MigrateLogGroupARN pulumi.StringInput
	PassRoleARNs       []pulumi.StringOutput
}

// DeployAccess is what cd.yml may do, granted to the deploy role once the things it deploys to
// exist.
//
// A component of its own rather than part of Identities, because the grant names the services and
// the cluster, the services need the roles, and a role cannot wait for a service that is waiting
// for it. The role comes first; what it may touch is attached here, last.
//
// Least privilege, spelled out: push to this stack's repositories, roll this stack's services,
// register and run this stack's migration task, pass this stack's roles to ECS. Two actions are
// granted on `*` because AWS offers no resource-level permission for them —
// ecr:GetAuthorizationToken is a login, not an action on a repository, and the task-definition
// calls act on a family that does not have an ARN until it has been registered.
type DeployAccess struct {
	pulumi.ResourceState

	Policy *iam.RolePolicy
}

// NewDeployAccess attaches the deploy role's inline policy.
func NewDeployAccess(ctx *pulumi.Context, cfg stackconfig.StackConfig, args DeployAccessArgs, opts ...pulumi.ResourceOption) (*DeployAccess, error) {
	c := &DeployAccess{}
	if err := ctx.RegisterComponentResource("service:infra:DeployAccess", cfg.Name("deploy-access"), c, opts...); err != nil {
		return nil, err
	}

	// Flattened into one All, in a fixed order, and cut back apart by length.
	inputs := []pulumi.StringInput{args.Cluster}
	inputs = append(inputs, optional(args.MigrateFamily), optional(args.MigrateLogGroupARN))
	inputs = append(inputs, stringInputs(args.RepositoryARNs)...)
	inputs = append(inputs, stringInputs(args.ServiceARNs)...)
	inputs = append(inputs, stringInputs(args.PassRoleARNs)...)
	repositories, services := len(args.RepositoryARNs), len(args.ServiceARNs)

	policy := DocumentOf(func(v []string) []Statement {
		rest := v[3:]
		return DeployStatements(cfg, DeployTargets{
			Cluster:            v[0],
			MigrateFamily:      v[1],
			MigrateLogGroupARN: v[2],
			RepositoryARNs:     rest[:repositories],
			ServiceARNs:        rest[repositories : repositories+services],
			PassRoleARNs:       rest[repositories+services:],
		})
	}, inputs...)

	var err error
	c.Policy, err = iam.NewRolePolicy(ctx, "deploy-permissions", &iam.RolePolicyArgs{
		Role:   args.Role.Name,
		Name:   pulumi.String("deploy-this-stack"),
		Policy: policy,
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}
	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{})
}

// DeployStatements is the deploy role's policy.
func DeployStatements(cfg stackconfig.StackConfig, t DeployTargets) []Statement {
	ecsARN := func(rest string) string {
		return fmt.Sprintf("arn:%s:ecs:%s:%s:%s", cfg.Partition(), cfg.Region, cfg.AccountID, rest)
	}
	statements := []Statement{
		Allow("LogInToECR", []string{"ecr:GetAuthorizationToken"}, "*"),
		Allow("PushOwnImages", ECRPushActions, t.RepositoryARNs...),
		// Read the running container, send it back with a new image.
		Allow("RollOwnServices",
			[]string{"ecs:DescribeExpressGatewayService", "ecs:UpdateExpressGatewayService"},
			t.ServiceARNs...),
		// CD follows the rollout by polling DescribeServices on the cluster's services.
		Allow("WatchOwnServices", []string{"ecs:DescribeServices"}, ecsARN("service/"+t.Cluster+"/*")),
		// The migration step registers a revision of its own. Granted with or without one,
		// because rolling an Express service also produces a new task-definition revision, and
		// whether ECS registers that one with the caller's permissions or with the infrastructure
		// role's is not something this template has verified against a live account.
		Allow("RegisterRevisions", []string{"ecs:DescribeTaskDefinition", "ecs:RegisterTaskDefinition"}, "*"),
	}
	if t.MigrateFamily != "" {
		statements = append(statements,
			Allow("RunMigrations", []string{"ecs:RunTask"}, ecsARN("task-definition/"+t.MigrateFamily+":*")),
			Allow("WatchMigrations", []string{"ecs:DescribeTasks"}, ecsARN("task/"+t.Cluster+"/*")),
			// On failure, the job prints the migration's own log rather than "exit code 1".
			Allow("ReadMigrationLogs", []string{"logs:GetLogEvents"}, t.MigrateLogGroupARN+":*"),
		)
	}
	statements = append(statements, Statement{
		Sid:      "PassOwnRolesToECS",
		Effect:   "Allow",
		Action:   []string{"iam:PassRole"},
		Resource: t.PassRoleARNs,
		Condition: map[string]map[string]any{
			"StringEquals": {"iam:PassedToService": PassedToServices},
		},
	})
	return statements
}

func optional(input pulumi.StringInput) pulumi.StringInput {
	if input == nil {
		return pulumi.String("")
	}
	return input
}
