package components

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/cloudrunv2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// BootstrapImage is the placeholder until the first CD run. Cloud Run refuses to create a service
// with no image, and this one is public, tiny and always available.
const BootstrapImage = "us-docker.pkg.dev/cloudrun/container/hello"

// The fields the pipeline owns. Declared ignore_changes on every service and job; see Service.
var (
	ServiceIgnoreChanges = []string{"template.containers[0].image", "client", "clientVersion"}
	JobIgnoreChanges     = []string{"template.template.containers[0].image", "client", "clientVersion"}
)

// JobSpec is one Cloud Run Job alongside a service, running the service's image.
type JobSpec struct {
	Name    string
	Command []string
	Args    []string
	Env     map[string]string
	// SecretEnv maps an environment variable to a logical secret name in ServiceArgs.SecretRefs.
	SecretEnv map[string]string
}

// ServiceArgs is everything that makes one Cloud Run service different from another. Each field
// has a usable zero value, so an instantiation names only what is particular about it.
type ServiceArgs struct {
	// App is the service's base name; the Cloud Run service is App plus the environment.
	App          string
	RuntimeEmail pulumi.StringInput
	Port         int
	// HealthPath is what the startup probe reads. Liveness, never readiness: readiness can
	// legitimately be false on a first deploy, and a startup probe reading it would never let the
	// revision up.
	HealthPath string

	Env map[string]pulumi.StringInput
	// SecretEnv maps an environment variable to a logical secret name in SecretRefs.
	SecretEnv  map[string]string
	SecretRefs map[string]SecretRef

	// ConnectionName attaches a Cloud SQL instance at /cloudsql. Nil means no database: no volume
	// and no mount rather than an empty one — an unused attachment is a permission the service
	// does not need.
	ConnectionName pulumi.StringInput

	CPU                     string // default "1"
	Memory                  string // default "512Mi"
	Concurrency             int    // default 40
	StartupFailureThreshold int    // default 20, at a 5 s period: a hundred-second start budget

	Jobs []JobSpec

	// Private skips the allUsers invoker binding. Public is the default because a service nothing
	// can reach is not yet a service. Set it once the API is behind an identity-aware proxy or is
	// called only by other Google-authenticated workloads; the smoke test in CD then needs an
	// identity token rather than a plain curl.
	Private bool
}

// Service is a Cloud Run service, and optionally the jobs that maintain it.
//
// Division of ownership, which is what keeps Pulumi and the CD pipeline from fighting: THIS STACK
// OWNS THE SHAPE — CPU, memory, scaling, service account, secret wiring, the Cloud SQL attachment —
// and THE PIPELINE OWNS THE IMAGE TAG. Every container image is therefore declared with
// ignore_changes on the image field.
//
// ignore_changes alone is NOT sufficient, and getting this wrong silently rolls the service back
// to the bootstrap image. It suppresses the diff between the program and Pulumi's state — but
// when CD updates the image out of band, state is never told. Program and state both still say
// `hello`, so there is no diff to ignore, and the next update of any unrelated field sends the
// whole template, stale image included, and Cloud Run dutifully creates a revision running it.
//
// The missing half is --refresh: it pulls the live image into state first, and ignore_changes then
// preserves THAT rather than the program's constant. Every apply path here passes it, and the
// infra/justfile recipes are the enforcement.
//
// Service describes a Cloud Run service in this project, not an app. Everything that varies — the
// port, the probe path, a database, secrets, jobs — is an argument with a default, never a branch
// inside the component. That is what makes a second service one more instantiation in main.go
// rather than a copied file with a different string in it.
type Service struct {
	pulumi.ResourceState

	Service *cloudrunv2.Service
	Jobs    map[string]*cloudrunv2.Job
	URL     pulumi.StringOutput
}

// NewService creates the Cloud Run service, its public invoker binding, and its jobs.
func NewService(ctx *pulumi.Context, cfg stackconfig.StackConfig, args ServiceArgs, opts ...pulumi.ResourceOption) (*Service, error) {
	args.CPU = cmp.Or(args.CPU, "1")
	args.Memory = cmp.Or(args.Memory, "512Mi")
	args.Concurrency = cmp.Or(args.Concurrency, 40)
	args.StartupFailureThreshold = cmp.Or(args.StartupFailureThreshold, 20)

	// Fail here rather than mid-apply. A missing key surfaces otherwise as a Cloud Run revision
	// that cannot resolve a secret, which reads as "the app is broken".
	if err := checkSecrets(args.App, args.SecretEnv, args.SecretRefs); err != nil {
		return nil, err
	}
	for _, job := range args.Jobs {
		if err := checkSecrets(args.App+"/"+job.Name, job.SecretEnv, args.SecretRefs); err != nil {
			return nil, err
		}
	}

	c := &Service{Jobs: map[string]*cloudrunv2.Job{}}
	if err := ctx.RegisterComponentResource("service:infra:Service", cfg.Name(args.App), c, opts...); err != nil {
		return nil, err
	}

	volumes := cloudrunv2.ServiceTemplateVolumeArray{}
	mounts := cloudrunv2.ServiceTemplateContainerVolumeMountArray{}
	if args.ConnectionName != nil {
		volumes = append(volumes, &cloudrunv2.ServiceTemplateVolumeArgs{
			Name: pulumi.String("cloudsql"),
			CloudSqlInstance: &cloudrunv2.ServiceTemplateVolumeCloudSqlInstanceArgs{
				Instances: pulumi.StringArray{args.ConnectionName},
			},
		})
		mounts = append(mounts, &cloudrunv2.ServiceTemplateContainerVolumeMountArgs{
			Name:      pulumi.String("cloudsql"),
			MountPath: pulumi.String("/cloudsql"),
		})
	}

	// Sorted by name. A map has no order, and a container whose environment comes out in a
	// different order on the next run is a diff — and a new revision — for nothing.
	envs := cloudrunv2.ServiceTemplateContainerEnvArray{}
	for _, name := range sortedKeys(args.Env) {
		envs = append(envs, &cloudrunv2.ServiceTemplateContainerEnvArgs{
			Name:  pulumi.String(name),
			Value: args.Env[name],
		})
	}
	for _, name := range sortedKeys(args.SecretEnv) {
		ref := args.SecretRefs[args.SecretEnv[name]]
		envs = append(envs, &cloudrunv2.ServiceTemplateContainerEnvArgs{
			Name: pulumi.String(name),
			ValueSource: &cloudrunv2.ServiceTemplateContainerEnvValueSourceArgs{
				SecretKeyRef: &cloudrunv2.ServiceTemplateContainerEnvValueSourceSecretKeyRefArgs{
					Secret:  ref.Secret,
					Version: ref.Version,
				},
			},
		})
	}

	container := &cloudrunv2.ServiceTemplateContainerArgs{
		Image: pulumi.String(BootstrapImage),
		Resources: &cloudrunv2.ServiceTemplateContainerResourcesArgs{
			Limits: pulumi.StringMap{
				"cpu":    pulumi.String(args.CPU),
				"memory": pulumi.String(args.Memory),
			},
			// Bill for CPU only while a request is in flight.
			CpuIdle: pulumi.Bool(true),
			// Without this, CpuIdle throttles the container DURING startup too, and a cold start
			// does not finish inside the probe budget. It costs nothing extra: the boost applies
			// only while starting.
			StartupCpuBoost: pulumi.Bool(true),
		},
		Ports: &cloudrunv2.ServiceTemplateContainerPortsArgs{
			ContainerPort: pulumi.Int(args.Port),
		},
		Envs:         envs,
		VolumeMounts: mounts,
		// A probe that is marginally too tight fails deploys intermittently, which reads as "the
		// app is broken" rather than "the budget is wrong".
		StartupProbe: &cloudrunv2.ServiceTemplateContainerStartupProbeArgs{
			InitialDelaySeconds: pulumi.Int(5),
			PeriodSeconds:       pulumi.Int(5),
			FailureThreshold:    pulumi.Int(args.StartupFailureThreshold),
			HttpGet: &cloudrunv2.ServiceTemplateContainerStartupProbeHttpGetArgs{
				Path: pulumi.String(args.HealthPath),
				Port: pulumi.Int(args.Port),
			},
		},
	}

	service, err := cloudrunv2.NewService(ctx, args.App, &cloudrunv2.ServiceArgs{
		Project:            pulumi.String(cfg.Project),
		Location:           pulumi.String(cfg.Region),
		Name:               pulumi.String(cfg.Name(args.App)),
		Ingress:            pulumi.String("INGRESS_TRAFFIC_ALL"),
		DeletionProtection: pulumi.Bool(false),
		Template: &cloudrunv2.ServiceTemplateArgs{
			ServiceAccount:                args.RuntimeEmail,
			MaxInstanceRequestConcurrency: pulumi.Int(args.Concurrency),
			Scaling: &cloudrunv2.ServiceTemplateScalingArgs{
				MinInstanceCount: pulumi.Int(cfg.MinInstances),
				MaxInstanceCount: pulumi.Int(cfg.MaxInstances),
			},
			Volumes:    volumes,
			Containers: cloudrunv2.ServiceTemplateContainerArray{container},
		},
	}, child(c, opts, pulumi.IgnoreChanges(ServiceIgnoreChanges))...)
	if err != nil {
		return nil, err
	}
	c.Service = service

	if !args.Private {
		if _, err := cloudrunv2.NewServiceIamMember(ctx, args.App+"-public", &cloudrunv2.ServiceIamMemberArgs{
			Project:  pulumi.String(cfg.Project),
			Location: pulumi.String(cfg.Region),
			Name:     service.Name,
			Role:     pulumi.String("roles/run.invoker"),
			Member:   pulumi.String("allUsers"),
		}, child(c, opts)...); err != nil {
			return nil, err
		}
	}

	for _, spec := range args.Jobs {
		job, err := c.job(ctx, cfg, opts, args, spec)
		if err != nil {
			return nil, err
		}
		c.Jobs[spec.Name] = job
	}

	c.URL = service.Uri
	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"url": c.URL})
}

func (c *Service) job(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, opts []pulumi.ResourceOption, args ServiceArgs, spec JobSpec,
) (*cloudrunv2.Job, error) {
	volumes := cloudrunv2.JobTemplateTemplateVolumeArray{}
	mounts := cloudrunv2.JobTemplateTemplateContainerVolumeMountArray{}
	if args.ConnectionName != nil {
		volumes = append(volumes, &cloudrunv2.JobTemplateTemplateVolumeArgs{
			Name: pulumi.String("cloudsql"),
			CloudSqlInstance: &cloudrunv2.JobTemplateTemplateVolumeCloudSqlInstanceArgs{
				Instances: pulumi.StringArray{args.ConnectionName},
			},
		})
		mounts = append(mounts, &cloudrunv2.JobTemplateTemplateContainerVolumeMountArgs{
			Name:      pulumi.String("cloudsql"),
			MountPath: pulumi.String("/cloudsql"),
		})
	}

	envs := cloudrunv2.JobTemplateTemplateContainerEnvArray{}
	for _, name := range sortedKeys(spec.Env) {
		envs = append(envs, &cloudrunv2.JobTemplateTemplateContainerEnvArgs{
			Name:  pulumi.String(name),
			Value: pulumi.String(spec.Env[name]),
		})
	}
	for _, name := range sortedKeys(spec.SecretEnv) {
		ref := args.SecretRefs[spec.SecretEnv[name]]
		envs = append(envs, &cloudrunv2.JobTemplateTemplateContainerEnvArgs{
			Name: pulumi.String(name),
			ValueSource: &cloudrunv2.JobTemplateTemplateContainerEnvValueSourceArgs{
				SecretKeyRef: &cloudrunv2.JobTemplateTemplateContainerEnvValueSourceSecretKeyRefArgs{
					Secret:  ref.Secret,
					Version: ref.Version,
				},
			},
		})
	}

	container := &cloudrunv2.JobTemplateTemplateContainerArgs{
		Image:        pulumi.String(BootstrapImage),
		Commands:     pulumi.ToStringArray(spec.Command),
		Args:         pulumi.ToStringArray(spec.Args),
		Envs:         envs,
		VolumeMounts: mounts,
	}

	return cloudrunv2.NewJob(ctx, args.App+"-"+spec.Name, &cloudrunv2.JobArgs{
		Project:            pulumi.String(cfg.Project),
		Location:           pulumi.String(cfg.Region),
		Name:               pulumi.String(cfg.Name(args.App, spec.Name)),
		DeletionProtection: pulumi.Bool(false),
		Template: &cloudrunv2.JobTemplateArgs{
			// One attempt. A failed migration needs a human reading the log, not a retry that
			// fails identically and doubles the noise.
			TaskCount: pulumi.Int(1),
			Template: &cloudrunv2.JobTemplateTemplateArgs{
				ServiceAccount: args.RuntimeEmail,
				MaxRetries:     pulumi.Int(0),
				Timeout:        pulumi.String("900s"),
				Volumes:        volumes,
				Containers:     cloudrunv2.JobTemplateTemplateContainerArray{container},
			},
		},
	}, child(c, opts, pulumi.IgnoreChanges(JobIgnoreChanges))...)
}

// checkSecrets refuses a mount of a secret the service was never given.
func checkSecrets(owner string, secretEnv map[string]string, refs map[string]SecretRef) error {
	for _, name := range sortedKeys(secretEnv) {
		if _, ok := refs[secretEnv[name]]; !ok {
			return fmt.Errorf("%s: %s is mounted from secret %q, which was not passed to this service "+
				"(a manual secret has no reference until its version is in stack configuration)",
				owner, name, secretEnv[name])
		}
	}
	return nil
}

// sortedKeys returns a map's keys in order, so everything built from a map is built the same way
// on every run.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
