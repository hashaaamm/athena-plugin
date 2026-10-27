package components

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecr"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// KeepImages is how many images the lifecycle policy keeps per repository. Every merge pushes an
// immutable :sha tag, so without a policy a repository grows without bound and storage quietly
// becomes a line on the bill. Thirty is a month of daily deploys to roll back into.
const KeepImages = 30

// Registry is the stack's ECR repository, for the backend's image.
//
// ECR scopes permissions, the lifecycle policy and tag immutability per repository, and an image's
// repository URL is what CD tags it with, so `$IMAGE_REPO:$sha` names exactly one thing. A second
// image this stack deploys gets a repository of its own through the same helper.
type Registry struct {
	pulumi.ResourceState

	Backend *ecr.Repository

	// URL is what `docker push` targets for the backend, and what the CD workflow reads as
	// IMAGE_REPO.
	URL pulumi.StringOutput
}

// NewRegistry creates the repository and its lifecycle policy.
func NewRegistry(ctx *pulumi.Context, cfg stackconfig.StackConfig, opts ...pulumi.ResourceOption) (*Registry, error) {
	c := &Registry{}
	if err := ctx.RegisterComponentResource("service:infra:Registry", cfg.Name("registry"), c, opts...); err != nil {
		return nil, err
	}

	var err error
	if c.Backend, err = c.repository(ctx, cfg, opts, "images", cfg.Slug); err != nil {
		return nil, err
	}
	c.URL = c.Backend.RepositoryUrl

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{"url": c.URL})
}

func (c *Registry) repository(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, opts []pulumi.ResourceOption, name, app string,
) (*ecr.Repository, error) {
	repository, err := ecr.NewRepository(ctx, name, &ecr.RepositoryArgs{
		Name: pulumi.String(cfg.Name(app)),
		// A tag that can be overwritten is a rollback target that can change under you. CD only
		// ever pushes :<sha>, so immutability costs nothing and makes every tag mean one image.
		ImageTagMutability: pulumi.String("IMMUTABLE"),
		ImageScanningConfiguration: &ecr.RepositoryImageScanningConfigurationArgs{
			ScanOnPush: pulumi.Bool(true),
		},
		// AES256 is ECR's own key, at no charge. A customer-managed KMS key would add a key
		// policy to maintain and a per-request charge for nothing an image needs.
		EncryptionConfigurations: ecr.RepositoryEncryptionConfigurationArray{
			&ecr.RepositoryEncryptionConfigurationArgs{EncryptionType: pulumi.String("AES256")},
		},
		// ECR refuses to delete a repository that still holds images, so outside production a
		// `pulumi destroy` would stop here with everything else already gone. In production the
		// refusal is the point: the images are what a rollback runs.
		ForceDelete: pulumi.Bool(!cfg.IsProduction()),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	if _, err := ecr.NewLifecyclePolicy(ctx, name+"-lifecycle", &ecr.LifecyclePolicyArgs{
		Repository: repository.Name,
		Policy:     pulumi.String(LifecyclePolicy(KeepImages)),
	}, child(c, opts)...); err != nil {
		return nil, err
	}
	return repository, nil
}

// LifecyclePolicy is the ECR lifecycle document that expires everything beyond the newest keep
// images, tagged or not.
func LifecyclePolicy(keep int) string {
	return fmt.Sprintf(`{"rules":[{"rulePriority":1,"description":"Keep the newest %d images",`+
		`"selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":%d},`+
		`"action":{"type":"expire"}}]}`, keep, keep)
}
