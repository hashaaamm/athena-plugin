package components

import (
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/artifactregistry"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const (
	// KeepImages is how many images the cleanup policy keeps. Every merge pushes an immutable
	// :sha tag, so without a policy the repository grows without bound and storage quietly becomes
	// a line on the bill.
	KeepImages = 10

	// DeleteOlderThan removes anything older than thirty days, whatever the keep count says.
	// Artifact Registry takes a duration string in seconds, not a date.
	DeleteOlderThan = "2592000s"
)

// Registry is one Artifact Registry Docker repository for every image this repository builds.
type Registry struct {
	pulumi.ResourceState

	Repository *artifactregistry.Repository

	// URL is what `docker push` targets, and what the CD workflow reads as IMAGE_REPO.
	URL pulumi.StringOutput
}

// NewRegistry creates the repository and its cleanup policy.
func NewRegistry(ctx *pulumi.Context, cfg stackconfig.StackConfig, opts ...pulumi.ResourceOption) (*Registry, error) {
	c := &Registry{}
	if err := ctx.RegisterComponentResource("service:infra:Registry", cfg.Name("registry"), c, opts...); err != nil {
		return nil, err
	}

	keepRecent := &artifactregistry.RepositoryCleanupPolicyArgs{
		Id:     pulumi.String("keep-recent"),
		Action: pulumi.String("KEEP"),
		MostRecentVersions: &artifactregistry.RepositoryCleanupPolicyMostRecentVersionsArgs{
			KeepCount: pulumi.Int(KeepImages),
		},
	}
	deleteOld := &artifactregistry.RepositoryCleanupPolicyArgs{
		Id:     pulumi.String("delete-old"),
		Action: pulumi.String("DELETE"),
		Condition: &artifactregistry.RepositoryCleanupPolicyConditionArgs{
			OlderThan: pulumi.String(DeleteOlderThan),
		},
	}

	repository, err := artifactregistry.NewRepository(ctx, "images", &artifactregistry.RepositoryArgs{
		Project:         pulumi.String(cfg.Project),
		Location:        pulumi.String(cfg.Region),
		RepositoryId:    pulumi.String(cfg.Name(cfg.Slug)),
		Format:          pulumi.String("DOCKER"),
		Description:     pulumi.String("Container images for " + cfg.Slug),
		CleanupPolicies: artifactregistry.RepositoryCleanupPolicyArray{keepRecent, deleteOld},
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	c.Repository = repository
	c.URL = pulumi.Sprintf("%s/%s/%s", cfg.RegistryHost(), cfg.Project, repository.RepositoryId)

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"url": c.URL})
}
