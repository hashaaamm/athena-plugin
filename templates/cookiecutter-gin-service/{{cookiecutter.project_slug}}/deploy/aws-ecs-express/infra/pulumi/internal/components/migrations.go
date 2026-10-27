// Removed by the post-generation hook when use_postgres is "no", together with database.go.
package components

import (
	"encoding/json"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// MigrateContainer is the one container in the migration task definition. cd.yml rewrites its
// image by position, and a test holds the name.
const MigrateContainer = "migrate"

// MigrationsArgs is what Migrations needs beyond the configuration.
type MigrationsArgs struct {
	ExecutionRoleARN pulumi.StringInput
	// TaskRoleARN is the task's own AWS identity. Nil means none, which is right for a migration:
	// it logs in with a password ECS resolved before the container started, and calls no AWS API.
	TaskRoleARN pulumi.StringInput

	// EntryPoint and Command are the image's ENTRYPOINT and CMD for this task. ECS overrides the
	// two separately, exactly as Docker does, so the binary goes in one and the subcommand in the
	// other — putting all three words in Command would run `/app/api /app/api migrate up`.
	EntryPoint []string
	Command    []string

	// Env is the plain environment.
	Env map[string]pulumi.StringInput
	// SecretEnv maps an environment variable to a logical secret name in SecretRefs.
	SecretEnv  map[string]string
	SecretRefs map[string]SecretRef

	LogGroup pulumi.StringInput
}

// Migrations is the task definition CD runs the schema migrations with, once per deploy, before
// the service is rolled.
//
// Who owns what is different here from the services, on purpose. Pulumi owns the task definition's
// SHAPE — roles, environment, secrets, logging, size — and CD never edits it. On every deploy, CD
// reads the latest ACTIVE revision of the family, swaps the image, registers that as the next
// revision and runs it. So there is no ignoreChanges: Pulumi's own revision is the template every
// CD revision is cut from, a change here is a new revision CD picks up on its next run, and the
// revisions CD registered are never Pulumi's to reconcile.
type Migrations struct {
	pulumi.ResourceState

	TaskDefinition *ecs.TaskDefinition
	// Family is what cd.yml reads as ECS_MIGRATE_TASK_FAMILY.
	Family pulumi.StringOutput
}

// NewMigrations creates the migration task definition.
func NewMigrations(ctx *pulumi.Context, cfg stackconfig.StackConfig, args MigrationsArgs, opts ...pulumi.ResourceOption) (*Migrations, error) {
	if err := checkSecrets(cfg.Slug+"/"+MigrateContainer, args.SecretEnv, args.SecretRefs); err != nil {
		return nil, err
	}

	c := &Migrations{}
	if err := ctx.RegisterComponentResource("service:infra:Migrations", cfg.Name("migrations"), c, opts...); err != nil {
		return nil, err
	}

	// The container definition is a JSON string in the provider's schema, so every value that is
	// an output — the log group, an environment value such as the database's address, and each
	// secret reference — is resolved first, in a fixed order, and cut back apart by position.
	envNames := sortedKeys(args.Env)
	secretNames := sortedKeys(args.SecretEnv)
	inputs := []any{args.LogGroup}
	for _, name := range envNames {
		inputs = append(inputs, args.Env[name])
	}
	for _, name := range secretNames {
		inputs = append(inputs, args.SecretRefs[args.SecretEnv[name]].ValueFrom())
	}
	definitions := pulumi.All(inputs...).ApplyT(func(resolved []any) (string, error) {
		logGroup, _ := resolved[0].(string)
		type pair struct {
			Name      string `json:"name"`
			Value     string `json:"value,omitempty"`
			ValueFrom string `json:"valueFrom,omitempty"`
		}
		environment := []pair{}
		for i, name := range envNames {
			value, _ := resolved[1+i].(string)
			environment = append(environment, pair{Name: name, Value: value})
		}
		secrets := []pair{}
		for i, name := range secretNames {
			valueFrom, _ := resolved[1+len(envNames)+i].(string)
			secrets = append(secrets, pair{Name: name, ValueFrom: valueFrom})
		}
		data, err := json.Marshal([]map[string]any{
			{
				"name": MigrateContainer,
				// The placeholder, like every container's first image. CD never runs this
				// revision: it registers one of its own with the image it just pushed.
				"image":       BootstrapImage,
				"essential":   true,
				"entryPoint":  args.EntryPoint,
				"command":     args.Command,
				"environment": environment,
				"secrets":     secrets,
				"logConfiguration": map[string]any{
					"logDriver": "awslogs",
					"options": map[string]string{
						"awslogs-group":         logGroup,
						"awslogs-region":        cfg.Region,
						"awslogs-stream-prefix": "ecs",
					},
				},
			},
		})
		return string(data), err
	}).(pulumi.StringOutput)

	var err error
	c.TaskDefinition, err = ecs.NewTaskDefinition(ctx, "migrate", &ecs.TaskDefinitionArgs{
		Family:                  pulumi.String(cfg.Name(cfg.Slug, MigrateContainer)),
		RequiresCompatibilities: pulumi.StringArray{pulumi.String("FARGATE")},
		NetworkMode:             pulumi.String("awsvpc"),
		// The smallest Fargate size. Goose applies SQL files one at a time and holds nothing.
		Cpu:                  pulumi.String("256"),
		Memory:               pulumi.String("512"),
		ExecutionRoleArn:     ptr(args.ExecutionRoleARN),
		TaskRoleArn:          ptr(args.TaskRoleARN),
		ContainerDefinitions: definitions,
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	c.Family = c.TaskDefinition.Family
	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"family": c.Family})
}
