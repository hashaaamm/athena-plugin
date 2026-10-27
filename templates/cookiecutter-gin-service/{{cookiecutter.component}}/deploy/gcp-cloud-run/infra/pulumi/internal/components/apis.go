// Package components holds the stack's building blocks, one per concern.
//
// Each component is a Pulumi ComponentResource taking a stackconfig.StackConfig by value and the
// handful of outputs it genuinely depends on, so main.go reads as a dependency graph rather than a
// script. Nothing here calls config.New itself — that is what makes a component constructible in a
// test.
package components

import (
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/projects"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// BootstrapAPIs are enabled by infra/bootstrap/state-bucket.sh, before Pulumi runs at all.
//
// Enabling an API is itself an API call — serviceusage — authorised through cloudresourcemanager,
// and the GCP provider resolves the project's default region and zone through compute before it
// plans a single resource. All three are needed before this component can run, so this component
// cannot turn them on. Listed here so the full set is visible in one place, and asserted against
// the bootstrap script by a test.
//
// The symptom when that is missed is a `pulumi preview` that fails with SERVICE_DISABLED for
// compute.googleapis.com while pointing at no resource in particular.
var BootstrapAPIs = []string{
	"cloudresourcemanager.googleapis.com", // authorises enabling anything else
	"serviceusage.googleapis.com",         // the API that enables APIs
	"compute.googleapis.com",              // the provider reads default region/zone from it
}

// RequiredAPIs is everything the stack itself needs, enabled by this component.
var RequiredAPIs = []string{
	"run.googleapis.com",
	"artifactregistry.googleapis.com",
	"secretmanager.googleapis.com",
	"iam.googleapis.com",
	"iamcredentials.googleapis.com",
	"sts.googleapis.com",
{%- if cookiecutter.use_postgres == "yes" %}
	"sqladmin.googleapis.com",
{%- endif %}
}

// Apis enables the GCP service APIs everything else depends on.
//
// First in the graph, and everything else depends on it. Without that ordering the first
// `pulumi up` fails halfway through with "API not enabled" errors that are individually obvious and
// collectively a twenty-minute detour.
type Apis struct {
	pulumi.ResourceState

	// Services is what everything after this component passes to pulumi.DependsOn.
	Services []pulumi.Resource
}

// NewApis enables RequiredAPIs.
func NewApis(ctx *pulumi.Context, cfg stackconfig.StackConfig, opts ...pulumi.ResourceOption) (*Apis, error) {
	c := &Apis{}
	if err := ctx.RegisterComponentResource("service:infra:Apis", cfg.Name("apis"), c, opts...); err != nil {
		return nil, err
	}

	for _, api := range RequiredAPIs {
		service, err := projects.NewService(ctx, "api-"+strings.Split(api, ".")[0], &projects.ServiceArgs{
			Project: pulumi.String(cfg.Project),
			Service: pulumi.String(api),
			// Disabling an API on destroy would break anything else in the project using it.
			DisableOnDestroy: pulumi.Bool(false),
		}, child(c, opts)...)
		if err != nil {
			return nil, err
		}
		c.Services = append(c.Services, service)
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{})
}

// child is the options every resource inside a component is created with: the component's own
// options, then the component as parent.
//
// The component's options are passed down rather than left on the component alone, because they
// carry the DependsOn that orders this component after the APIs, and this program does not rely
// on a component's dependencies reaching its children by inheritance — every child is given them
// directly. The explicit GCP provider travels the same way; a resource from another package
// ignores a provider that is not its own.
func child(parent pulumi.Resource, opts []pulumi.ResourceOption, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := make([]pulumi.ResourceOption, 0, len(opts)+1+len(extra))
	out = append(out, opts...)
	out = append(out, pulumi.Parent(parent))
	return append(out, extra...)
}
