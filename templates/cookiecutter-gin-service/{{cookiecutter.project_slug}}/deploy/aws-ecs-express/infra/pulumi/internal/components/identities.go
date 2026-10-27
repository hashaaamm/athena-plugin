package components

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// The things infra/bootstrap/state-bucket.sh creates, once per account, before Pulumi runs at all.
// Named here rather than passed in, because the script names them the same way and the two must
// agree — a test reads both scripts and checks.
const (
	// StateKeyAlias is the KMS key that encrypts the state bucket and the stack's secrets.
	StateKeyAlias = "alias/pulumi-state"

	// GitHubOIDCHost is GitHub Actions' token issuer, without the scheme — the form IAM uses as
	// the condition-key prefix and in the provider's ARN.
	GitHubOIDCHost = "token.actions.githubusercontent.com"
	// GitHubOIDCURL is the issuer the OIDC provider is registered with.
	GitHubOIDCURL = "https://" + GitHubOIDCHost
	// GitHubAudience is the client id the provider accepts, and the `aud` every token must carry.
	GitHubAudience = "sts.amazonaws.com"

	// DeployEnvironment is the GitHub environment cd.yml's job runs in. The deploy role trusts a
	// token from that environment and nothing else, so a workflow that is not the deploy job —
	// or a branch pushing without the environment's protection rules — cannot assume it.
	DeployEnvironment = "production"
)

// Managed policies, by name. The partition is added by managedPolicy.
const (
	InfrastructurePolicy = "service-role/AmazonECSInfrastructureRoleforExpressGatewayServices"
	ExecutionPolicy      = "service-role/AmazonECSTaskExecutionRolePolicy"
	ReadOnlyPolicy       = "ReadOnlyAccess"
)

// IdentitiesArgs is what Identities needs beyond the configuration.
type IdentitiesArgs struct {
	// SecretARNs is every secret a task mounts. The execution role may read exactly these.
	SecretARNs []pulumi.StringOutput
	// GeneratedSecretARNs is the subset whose value the stack writes; the preview role reads
	// these on refresh.
	GeneratedSecretARNs []pulumi.StringOutput
}

// Identities is every IAM role in the stack, and who may assume each.
//
// Five roles, and the split is the point. ECS gets two kinds: the INFRASTRUCTURE role, which Express
// Mode uses to build the load balancer, target groups and scaling policies on your behalf, and an
// EXECUTION role per service, which pulls the image, writes logs and resolves secrets before the
// container starts. The application gets a RUNTIME role of its own — the AWS identity its code
// runs as — which holds nothing at all until the application needs something. CI gets two, keyless:
// a DEPLOY role that can push an image and roll a service and nothing else, and a PREVIEW role that
// can read the account and this project's state and change nothing.
//
// Neither CI role has a long-lived credential. GitHub Actions exchanges its own OIDC token for a
// short-lived session, and each role's trust policy says exactly which workflow's token it
// accepts — ask Athena for the GitHub Actions standards. The OIDC provider those tokens are
// checked against is looked up rather than created: there is one per account, shared by every
// service, so the bootstrap script owns it (ADR-0021). A stack that created it would delete it from
// under every other service in the account on its own `destroy`.
type Identities struct {
	pulumi.ResourceState

	Infrastructure *iam.Role
	Execution      *iam.Role
	Runtime        *iam.Role
	Deploy         *iam.Role
	Preview        *iam.Role

	// ServiceDependencies is what an Express service must depend on: the infrastructure role's
	// policy attachment, and the backend execution role's grants. AWS documents the first — delete
	// the policy before the service and the service sticks in DRAINING, because Express Mode can no
	// longer tear down what it built. The second is the same race on the way up: a task whose
	// execution role cannot yet read its secrets fails to start.
	ServiceDependencies []pulumi.Resource
}

// NewIdentities creates the roles, their trust policies and their grants. The deploy role's
// permissions are not here: they name the services and the cluster, which do not exist yet — see
// DeployAccess.
func NewIdentities(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, args IdentitiesArgs, opts ...pulumi.ResourceOption,
) (*Identities, error) {
	c := &Identities{}
	if err := ctx.RegisterComponentResource("service:infra:Identities", cfg.Name("identities"), c, opts...); err != nil {
		return nil, err
	}

	var err error
	tasks := serviceTrust("ecs-tasks.amazonaws.com", cfg.AccountID)

	// --- ECS ------------------------------------------------------------------------------------

	if c.Infrastructure, err = c.role(ctx, cfg, opts, "infrastructure-role", "ecs-infra",
		pulumi.String(serviceTrust("ecs.amazonaws.com", cfg.AccountID))); err != nil {
		return nil, err
	}
	attachment, err := c.attach(ctx, cfg, opts, "infrastructure-managed", c.Infrastructure, InfrastructurePolicy)
	if err != nil {
		return nil, err
	}
	c.ServiceDependencies = append(c.ServiceDependencies, attachment)

	if c.Execution, err = c.role(ctx, cfg, opts, "execution-role", "exec", pulumi.String(tasks)); err != nil {
		return nil, err
	}
	if attachment, err = c.attach(ctx, cfg, opts, "execution-managed", c.Execution, ExecutionPolicy); err != nil {
		return nil, err
	}
	c.ServiceDependencies = append(c.ServiceDependencies, attachment)

	// The managed policy pulls images and writes logs; it reads no secret. Granted per secret ARN,
	// so a secret added to the account later is not readable by this role because of a wildcard
	// written today.
	if len(args.SecretARNs) > 0 {
		read, err := iam.NewRolePolicy(ctx, "execution-reads-secrets", &iam.RolePolicyArgs{
			Role: c.Execution.Name,
			Name: pulumi.String("read-own-secrets"),
			Policy: DocumentOf(func(arns []string) []Statement {
				return []Statement{Allow("ReadOwnSecrets", []string{"secretsmanager:GetSecretValue"}, arns...)}
			}, stringInputs(args.SecretARNs)...),
		}, child(c, opts)...)
		if err != nil {
			return nil, err
		}
		c.ServiceDependencies = append(c.ServiceDependencies, read)
	}

	// The application's own identity, with no policy at all. This is where an S3 grant goes when
	// the application needs a bucket: an iam.RolePolicy on this role, scoped to that bucket. Not
	// on the execution role — that one is ECS's, it acts before the container starts, and
	// anything granted to it is granted to every task that pulls with it.
	if c.Runtime, err = c.role(ctx, cfg, opts, "runtime-role", "runtime", pulumi.String(tasks)); err != nil {
		return nil, err
	}

	// --- CI, keyless --------------------------------------------------------------------------

	provider := iam.LookupOpenIdConnectProviderOutput(ctx, iam.LookupOpenIdConnectProviderOutputArgs{
		Url: pulumi.String(GitHubOIDCURL),
	}, invokeOpts(opts)...).Arn()

	if c.Deploy, err = c.role(ctx, cfg, opts, "deploy-role", "deploy",
		DocumentOf(func(v []string) []Statement {
			return []Statement{GitHubTrust(v[0], DeploySubject(cfg.SubjectPrefix()))}
		}, provider)); err != nil {
		return nil, err
	}

	if c.Preview, err = c.role(ctx, cfg, opts, "preview-role", "preview",
		DocumentOf(func(v []string) []Statement {
			return []Statement{GitHubTrust(v[0], PreviewSubject(cfg.SubjectPrefix()))}
		}, provider)); err != nil {
		return nil, err
	}
	// `pulumi preview --refresh` reads every resource in the stack, so the preview role needs read
	// on every resource type the stack declares. ReadOnlyAccess is AWS's maintained list of
	// exactly that; a hand-written list breaks every time the stack gains a resource type, and
	// breaks as an AccessDenied in a pull request nobody can fix from the pull request.
	//
	// What it widens, stated plainly: CI can read resource metadata across the account, not only
	// this stack's. What it does NOT grant is any write, or the value of any secret.
	if _, err := c.attach(ctx, cfg, opts, "preview-read-only", c.Preview, ReadOnlyPolicy); err != nil {
		return nil, err
	}

	// Reading the account is not reading the STACK. The state is an object in the bootstrap
	// bucket, encrypted with the bootstrap key, and the stack's secrets are encrypted with the
	// same key — so a preview that can list resources still fails on s3:GetObject, then on
	// kms:Decrypt, one permission further in each time. Both are scoped to this Pulumi project:
	// the bucket is shared by every service in the account, and another project's state is not
	// this pipeline's to read.
	stateKey := kms.LookupKeyOutput(ctx, kms.LookupKeyOutputArgs{
		KeyId: pulumi.String(StateKeyAlias),
	}, invokeOpts(opts)...).Arn()

	previewInputs := append([]pulumi.StringInput{stateKey}, stringInputs(args.GeneratedSecretARNs)...)
	if _, err := iam.NewRolePolicy(ctx, "preview-reads-state", &iam.RolePolicyArgs{
		Role: c.Preview.Name,
		Name: pulumi.String("read-own-state"),
		Policy: DocumentOf(func(v []string) []Statement {
			return PreviewStatements(cfg, ctx.Project(), v[0], v[1:])
		}, previewInputs...),
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{
		"deployRoleArn":  c.Deploy.Arn,
		"previewRoleArn": c.Preview.Arn,
		"runtimeRoleArn": c.Runtime.Arn,
	})
}

// DeploySubject is the `sub` claim of a token from cd.yml's job: the repository's subject prefix,
// in the production environment. A job that names an environment gets this subject whatever branch
// it ran from, so the environment's protection rules — not the branch — are what stand between a
// push and a deploy.
func DeploySubject(prefix string) string {
	return fmt.Sprintf("%s:environment:%s", prefix, DeployEnvironment)
}

// PreviewSubject is the `sub` claim of a token from a pull_request workflow in the repository.
// GitHub mints no OIDC token for a pull request from a fork, so this is the repository's own
// branches only.
func PreviewSubject(prefix string) string {
	return prefix + ":pull_request"
}

// GitHubTrust admits a web-identity token from GitHub's OIDC provider with exactly this subject,
// minted for STS. StringEquals rather than StringLike: a wildcard in a subject is how a trust
// policy written for one repository ends up admitting every repository in the organisation.
func GitHubTrust(providerARN, subject string) Statement {
	return Statement{
		Effect:    "Allow",
		Principal: map[string]string{"Federated": providerARN},
		Action:    []string{"sts:AssumeRoleWithWebIdentity"},
		Condition: map[string]map[string]any{
			"StringEquals": {
				GitHubOIDCHost + ":aud": GitHubAudience,
				GitHubOIDCHost + ":sub": subject,
			},
		},
	}
}

// PreviewStatements is the preview role's access to this project's state, and to nothing else
// that could not already be read.
func PreviewStatements(cfg stackconfig.StackConfig, project, stateKeyARN string, generatedSecretARNs []string) []Statement {
	bucket := fmt.Sprintf("arn:%s:s3:::%s", cfg.Partition(), cfg.StateBucket())
	statements := []Statement{
		// IfExists, because the backend's first call is a HeadBucket — "is the bucket there and
		// may I use it" — which IAM authorises as s3:ListBucket with no prefix at all. A plain
		// StringLike fails that check and the preview reports a bucket that does not exist.
		{
			Sid:      "ListState",
			Effect:   "Allow",
			Action:   []string{"s3:ListBucket"},
			Resource: []string{bucket},
			Condition: map[string]map[string]any{
				"StringLikeIfExists": {"s3:prefix": []string{".pulumi/*"}},
			},
		},
		// meta.yaml is the backend's format version, read before anything else; stacks/<project>/
		// is this project's state and nobody else's.
		Allow("ReadOwnState", []string{"s3:GetObject"},
			bucket+"/.pulumi/meta.yaml",
			bucket+"/.pulumi/stacks/"+project+"/*"),
		// The DIY backend's lock objects. A preview takes none today; a refresh may, and a lock
		// it cannot write fails as a Pulumi error rather than as the missing permission it is.
		// The path is what pulumi v3.263 writes: .pulumi/locks/organization/<project>/<stack>/.
		Allow("WriteOwnLocks", []string{"s3:PutObject", "s3:DeleteObject"},
			bucket+"/.pulumi/locks/organization/"+project+"/*"),
		// Decrypt for the state objects and the stack's secrets; GenerateDataKey because the
		// bucket's default encryption is SSE-KMS, so writing a lock object asks the key for one.
		Allow("UseStateKey", []string{"kms:Decrypt", "kms:GenerateDataKey"}, stateKeyARN),
	}
	if len(generatedSecretARNs) > 0 {
		// Refreshing a secretsmanager.SecretVersion reads its value — that is how the provider
		// tells whether it changed. This is not a new exposure: these are the values the stack
		// generated, they are already in the state this role can decrypt, and GitHub mints no
		// OIDC token for a pull request from a fork. Manual secrets — values a human put in, which
		// are NOT in state — are deliberately not on this list.
		statements = append(statements, Allow("RefreshGeneratedSecrets",
			[]string{"secretsmanager:GetSecretValue"}, generatedSecretARNs...))
	}
	return statements
}

func (c *Identities) role(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, opts []pulumi.ResourceOption,
	name, role string, trust pulumi.StringInput,
) (*iam.Role, error) {
	roleName, err := cfg.RoleName(role)
	if err != nil {
		return nil, err
	}
	return iam.NewRole(ctx, name, &iam.RoleArgs{
		Name:             pulumi.String(roleName),
		AssumeRolePolicy: trust,
	}, child(c, opts)...)
}

func (c *Identities) attach(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, opts []pulumi.ResourceOption,
	name string, role *iam.Role, policy string,
) (*iam.RolePolicyAttachment, error) {
	return iam.NewRolePolicyAttachment(ctx, name, &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String(ManagedPolicyARN(cfg, policy)),
	}, child(c, opts)...)
}

// ManagedPolicyARN is an AWS managed policy's ARN in the stack's partition.
func ManagedPolicyARN(cfg stackconfig.StackConfig, policy string) string {
	return fmt.Sprintf("arn:%s:iam::aws:policy/%s", cfg.Partition(), policy)
}

func stringInputs(outputs []pulumi.StringOutput) []pulumi.StringInput {
	inputs := make([]pulumi.StringInput, len(outputs))
	for i, output := range outputs {
		inputs[i] = output
	}
	return inputs
}
