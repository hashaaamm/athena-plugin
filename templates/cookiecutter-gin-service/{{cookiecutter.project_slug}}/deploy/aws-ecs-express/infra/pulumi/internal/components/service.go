package components

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// ServiceIgnoreChanges is the one field the pipeline owns. The primary container is a single
// object in the provider's schema — not a list — so the path has no index.
var ServiceIgnoreChanges = []string{"primaryContainer.image"}

// Scaling. Express Mode scales on one metric per service; average CPU is the one a Go service with
// no per-request state saturates first, and 60% leaves a new task time to start before the others
// are pinned.
const (
	ScalingMetric = "AVERAGE_CPU"
	ScalingTarget = 60
)

// ServiceArgs is everything that makes one Express service different from another. Each field
// has a usable zero value, so an instantiation names only what is particular about it.
type ServiceArgs struct {
	// App is the service's base name; the ECS service is App plus the environment.
	App string

	Cluster               pulumi.StringInput
	ExecutionRoleARN      pulumi.StringInput
	InfrastructureRoleARN pulumi.StringInput
	// TaskRoleARN is the application's own AWS identity. Nil means none: a container that calls
	// no AWS API needs no role, and an unused role is a permission waiting for a use.
	TaskRoleARN pulumi.StringInput

	Port int
	// HealthPath is what the load balancer's health check reads. Liveness, never readiness:
	// readiness can legitimately be false on a first deploy, and a target that is never healthy
	// is a deployment that never finishes.
	HealthPath string

	Env map[string]pulumi.StringInput
	// SecretEnv maps an environment variable to a logical secret name in SecretRefs.
	SecretEnv  map[string]string
	SecretRefs map[string]SecretRef

	LogGroup pulumi.StringInput

	// Subnets and SecurityGroups are where the tasks — and so the load balancer — run.
	Subnets        pulumi.StringArrayInput
	SecurityGroups pulumi.StringArrayInput

	CPU      string // default StackConfig.CPU
	Memory   string // default StackConfig.Memory
	MinTasks int    // default StackConfig.MinTasks
	MaxTasks int    // default StackConfig.MaxTasks
}

// Service is an ECS Express Mode service: the task, the load balancer, the HTTPS listener, the
// target group, the service-specific security group and the scaling policy, from one resource.
//
// Division of ownership, which is what keeps Pulumi and the CD pipeline from fighting: THIS STACK
// OWNS THE SHAPE — size, scaling, roles, network, environment, secret wiring, logs — and THE
// PIPELINE OWNS THE IMAGE. The image is therefore declared with ignoreChanges.
//
// ignoreChanges alone is NOT sufficient, and getting this wrong silently rolls the service back
// to the bootstrap image. It suppresses the diff between the program and Pulumi's state — but
// when CD updates the image out of band, state is never told. Program and state both still say
// `hello`, so there is no diff to ignore, and the next update of any unrelated field sends the
// whole primary container, stale image included.
//
// The missing half is --refresh: it pulls the live image into state first, and ignoreChanges then
// preserves THAT rather than the program's constant. Every apply path here passes it, and the
// infra/justfile recipes are the enforcement.
//
// Service describes an Express service in this stack, not an app. Everything that varies — the
// port, the probe path, the roles, the secrets, the size — is an argument with a default, never a
// branch inside the component. A second service is one more NewService call in main.go.
type Service struct {
	pulumi.ResourceState

	Service *ecs.ExpressGatewayService
	Name    pulumi.StringOutput
	ARN     pulumi.StringOutput
	URL     pulumi.StringOutput
}

// NewService creates the Express service.
func NewService(ctx *pulumi.Context, cfg stackconfig.StackConfig, args ServiceArgs, opts ...pulumi.ResourceOption) (*Service, error) {
	args.CPU = cmp.Or(args.CPU, cfg.CPU)
	args.Memory = cmp.Or(args.Memory, cfg.Memory)
	args.MinTasks = cmp.Or(args.MinTasks, cfg.MinTasks)
	args.MaxTasks = cmp.Or(args.MaxTasks, cfg.MaxTasks)

	// Fail here rather than mid-apply. A missing key surfaces otherwise as a task that cannot
	// resolve a secret, which reads as "the app is broken".
	if err := checkSecrets(args.App, args.SecretEnv, args.SecretRefs); err != nil {
		return nil, err
	}

	c := &Service{}
	if err := ctx.RegisterComponentResource("service:infra:Service", cfg.Name(args.App), c, opts...); err != nil {
		return nil, err
	}

	// Sorted by name. A map has no order, and a container whose environment comes out in a
	// different order on the next run is a diff — and a new deployment — for nothing.
	envs := ecs.ExpressGatewayServicePrimaryContainerEnvironmentArray{}
	for _, name := range sortedKeys(args.Env) {
		envs = append(envs, &ecs.ExpressGatewayServicePrimaryContainerEnvironmentArgs{
			Name:  pulumi.String(name),
			Value: args.Env[name],
		})
	}
	secrets := ecs.ExpressGatewayServicePrimaryContainerSecretArray{}
	for _, name := range sortedKeys(args.SecretEnv) {
		secrets = append(secrets, &ecs.ExpressGatewayServicePrimaryContainerSecretArgs{
			Name:      pulumi.String(name),
			ValueFrom: args.SecretRefs[args.SecretEnv[name]].ValueFrom(),
		})
	}

	service, err := ecs.NewExpressGatewayService(ctx, args.App, &ecs.ExpressGatewayServiceArgs{
		ServiceName:           pulumi.String(cfg.Name(args.App)),
		Cluster:               ptr(args.Cluster),
		Cpu:                   pulumi.String(args.CPU),
		Memory:                pulumi.String(args.Memory),
		ExecutionRoleArn:      args.ExecutionRoleARN,
		InfrastructureRoleArn: args.InfrastructureRoleARN,
		TaskRoleArn:           ptr(args.TaskRoleARN),
		HealthCheckPath:       pulumi.String(args.HealthPath),
		NetworkConfigurations: ecs.ExpressGatewayServiceNetworkConfigurationArray{
			&ecs.ExpressGatewayServiceNetworkConfigurationArgs{
				Subnets:        args.Subnets,
				SecurityGroups: args.SecurityGroups,
			},
		},
		ScalingTargets: ecs.ExpressGatewayServiceScalingTargetArray{
			&ecs.ExpressGatewayServiceScalingTargetArgs{
				MinTaskCount:           pulumi.Int(args.MinTasks),
				MaxTaskCount:           pulumi.Int(args.MaxTasks),
				AutoScalingMetric:      pulumi.String(ScalingMetric),
				AutoScalingTargetValue: pulumi.Int(ScalingTarget),
			},
		},
		PrimaryContainer: &ecs.ExpressGatewayServicePrimaryContainerArgs{
			Image:         pulumi.String(BootstrapImage),
			ContainerPort: pulumi.Int(args.Port),
			Environments:  envs,
			Secrets:       secrets,
			AwsLogsConfigurations: ecs.ExpressGatewayServicePrimaryContainerAwsLogsConfigurationArray{
				&ecs.ExpressGatewayServicePrimaryContainerAwsLogsConfigurationArgs{
					LogGroup:        args.LogGroup,
					LogStreamPrefix: pulumi.String("ecs"),
				},
			},
		},
		// Not waiting is what lets the first `up` finish: the service starts on the bootstrap
		// image, and a steady state reached on a placeholder proves nothing. CD waits instead,
		// on the real image, following the rollout to COMPLETED.
		WaitForSteadyState: pulumi.Bool(false),
	}, child(c, opts, pulumi.IgnoreChanges(ServiceIgnoreChanges))...)
	if err != nil {
		return nil, err
	}

	c.Service = service
	c.Name = service.ServiceName
	c.ARN = service.ServiceArn
	// The first ingress path is the service's public endpoint. The API returns a hostname; a URL
	// is what every consumer — CORS, the smoke test, a browser bundle — needs.
	c.URL = service.IngressPaths.ApplyT(func(paths []ecs.ExpressGatewayServiceIngressPath) string {
		if len(paths) == 0 || paths[0].Endpoint == "" {
			return ""
		}
		return EndpointURL(paths[0].Endpoint)
	}).(pulumi.StringOutput)

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"url": c.URL})
}

// EndpointURL turns an Express ingress endpoint into an https URL, leaving one that already is a
// URL alone. Express Mode terminates TLS on its load balancer with a certificate it manages, so
// https is the scheme the endpoint answers on.
func EndpointURL(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	return "https://" + endpoint
}

// ptr is an optional string argument, nil when absent.
func ptr(input pulumi.StringInput) pulumi.StringPtrInput {
	if input == nil {
		return nil
	}
	return input.ToStringOutput().ToStringPtrOutput()
}

// checkSecrets refuses a mount of a secret the service was never given.
func checkSecrets(owner string, secretEnv map[string]string, refs map[string]SecretRef) error {
	for _, name := range sortedKeys(secretEnv) {
		if _, ok := refs[secretEnv[name]]; !ok {
			return fmt.Errorf("%s: %s is mounted from secret %q, which was not passed to this service "+
				"(a manual secret has no reference until its VersionId is in stack configuration)",
				owner, name, secretEnv[name])
		}
	}
	return nil
}
