/**
 * Typed stack configuration.
 *
 * Every value the stack reads is resolved here, once. Components take a frozen `StackConfig` rather
 * than constructing `pulumi.Config` themselves, which is what keeps a component constructible in a
 * test and stops configuration keys being invented at three different call sites.
 */
import * as pulumi from "@pulumi/pulumi";

/** IAM's upper bound on a role name. The slug is what gives when a name is long. */
export const MAX_ROLE_NAME = 64;

/** S3's upper bound on a bucket name — global across every AWS account, which is why it is long. */
export const MAX_BUCKET_NAME = 63;

/** CloudFront's upper bound on an origin access control's name. */
export const MAX_OAC_NAME = 64;

const ACCOUNT = /^[0-9]{12}$/;
const REGION = /^[a-z]{2}(-gov)?-[a-z]+-[0-9]+$/;
/**
 * Lowercase letters, digits and single hyphens, starting with a letter. The slug starts the bucket
 * name, and S3 refuses uppercase; a dot would make the bucket's virtual-hosted TLS name invalid.
 */
const SLUG = /^[a-z](?:[a-z0-9]|-(?=[a-z0-9]))*$/;

/** The values `Pulumi.<stack>.yaml` must supply. Everything else has a default. */
export interface StackInputs {
  /**
   * The AWS account the stack deploys into. The provider refuses to plan anything against
   * credentials for any other account — the wrong-account guard, before a single resource is read.
   */
  readonly accountId: string;
  readonly region: string;
  /**
   * The STACK's environment. It names resources — every bucket and role carries it — and decides
   * `isProduction`. The application never sees it: a static bundle has no runtime environment.
   */
  readonly environment: string;
  /** The base name every resource is derived from — the cookiecutter slug. */
  readonly slug: string;
  /**
   * `owner/repo`. The CI roles' trust policies are built from it, and it is the single value that
   * stops any repository on GitHub from assuming them.
   */
  readonly githubRepository: string;
  /**
   * The start of the `sub` claim GitHub puts in this repository's OIDC tokens, everything before
   * `:environment:…` or `:pull_request`. Empty means the name-only form, `repo:<owner>/<repo>`.
   *
   * GitHub now issues an immutable form for many repositories — `repo:<owner>@<owner-id>/<repo>@<repo-id>`
   * — so that a renamed or re-registered account cannot inherit the trust. A trust policy written
   * for the name-only form then matches no token at all, and CI fails with "Not authorized to
   * perform sts:AssumeRoleWithWebIdentity". The repository reports its own prefix:
   * `gh api repos/<owner>/<repo>/actions/oidc/customization/sub --jq .sub_claim_prefix`.
   * infra/README.md has the step, and sync-github.sh refuses to un-gate CD while the two disagree.
   */
  readonly githubSubjectPrefix?: string;
}

export class StackConfig implements Required<StackInputs> {
  readonly accountId: string;
  readonly region: string;
  readonly environment: string;
  readonly slug: string;
  readonly githubRepository: string;
  readonly githubSubjectPrefix: string;

  /** Validates, then freezes. A configuration that would fail half way through an apply fails here. */
  constructor(inputs: StackInputs) {
    this.accountId = inputs.accountId.trim();
    this.region = inputs.region.trim();
    this.environment = inputs.environment.trim();
    this.slug = inputs.slug.trim();
    this.githubRepository = inputs.githubRepository.trim();
    this.githubSubjectPrefix = (inputs.githubSubjectPrefix ?? "").trim();
    const problems = this.problems();
    if (problems.length > 0) {
      throw new Error(`invalid stack configuration:\n  ${problems.join("\n  ")}`);
    }
    Object.freeze(this);
  }

  private problems(): string[] {
    const problems: string[] = [];
    const required: [string, string][] = [
      ["accountId", this.accountId],
      ["region", this.region],
      ["environment", this.environment],
      ["slug", this.slug],
      ["githubRepository", this.githubRepository],
    ];
    for (const [key, value] of required) {
      if (value === "") problems.push(`${key} is required`);
    }
    if (this.accountId && !ACCOUNT.test(this.accountId)) {
      problems.push(`accountId ${JSON.stringify(this.accountId)} is not 12 digits`);
    }
    if (this.region && !REGION.test(this.region)) {
      problems.push(`region ${JSON.stringify(this.region)} is not a region code such as eu-west-1`);
    }
    if (this.slug && !SLUG.test(this.slug)) {
      problems.push(
        `slug ${JSON.stringify(this.slug)} must be lowercase letters, digits and single hyphens, starting with a letter`,
      );
    }
    if (this.environment && !SLUG.test(this.environment)) {
      problems.push(`environment ${JSON.stringify(this.environment)} must be lowercase letters and digits`);
    }
    // One slash, two non-empty halves. Anything else builds a trust-policy subject that matches
    // nothing — or, worse, something nobody meant.
    const [owner, repo, ...rest] = this.githubRepository.split("/");
    if (!owner || !repo || rest.length > 0) {
      problems.push(`githubRepository ${JSON.stringify(this.githubRepository)} is not owner/repo`);
    }
    // Exact, as the trust policy matches it with StringEquals: a wildcard here would be a trust
    // policy that admits more than one repository.
    const prefix = this.githubSubjectPrefix;
    if (prefix && (!prefix.startsWith("repo:") || /[*? ]/.test(prefix) || prefix.endsWith(":"))) {
      problems.push(
        `githubSubjectPrefix ${JSON.stringify(prefix)} is not a subject prefix such as repo:<owner>@<id>/<repo>@<id>`,
      );
    }
    return problems;
  }

  /**
   * What the CI roles' trust policies put before `:environment:…` and `:pull_request`: the
   * configured prefix, or the name-only form when none is configured.
   */
  get subjectPrefix(): string {
    return this.githubSubjectPrefix || `repo:${this.githubRepository}`;
  }

  /**
   * Whether this STACK is the production one. It decides one thing here: whether the site bucket
   * may be deleted with objects still in it.
   */
  get isProduction(): boolean {
    return this.environment === "prod";
  }

  /** The ARN partition. Every ARN this stack spells by hand has it, and `aws` is wrong in GovCloud. */
  get partition(): string {
    return this.region.startsWith("us-gov-") ? "aws-us-gov" : "aws";
  }

  /**
   * The Pulumi state bucket infra/bootstrap/state-bucket.sh creates: one per account and region,
   * shared by every service and every site deployed there.
   */
  get stateBucket(): string {
    return `pulumi-state-${this.accountId}-${this.region}`;
  }

  /** A resource name that carries the environment, so two stacks can share an account safely. */
  name(...parts: string[]): string {
    return [...parts, this.environment].join("-");
  }

  /**
   * An IAM role name for `role`, trimmed to what IAM accepts. The slug is the part that gets
   * shortened, because the role and the environment are what make two roles different from each
   * other — and the role suffix is unique per role within a stack, so trimming cannot collide.
   */
  roleName(role: string): string {
    return fit(this.slug, `-${role}-${this.environment}`, MAX_ROLE_NAME);
  }

  /**
   * The site bucket's name. Bucket names are global across every AWS account, so it carries the
   * account and region as well as the slug and the environment: a name that is merely unlikely to
   * be taken is a `BucketAlreadyExists` on somebody's first apply.
   */
  get siteBucketName(): string {
    return fit(this.slug, `-site-${this.environment}-${this.accountId}-${this.region}`, MAX_BUCKET_NAME);
  }

  /** The origin access control's name, unique per account. */
  get oacName(): string {
    return fit(this.slug, `-site-${this.environment}`, MAX_OAC_NAME);
  }
}

/** `head` trimmed so `head + suffix` fits in `max`, without leaving a trailing hyphen. */
function fit(head: string, suffix: string, max: number): string {
  const room = max - suffix.length;
  if (room < 1) throw new Error(`${suffix} leaves no room for a name in ${max} characters`);
  return head.slice(0, room).replace(/-+$/, "") + suffix;
}

/** Reads `Pulumi.<stack>.yaml` into a validated `StackConfig`. The only place a key is spelled. */
export function load(): StackConfig {
  const config = new pulumi.Config();
  return new StackConfig({
    accountId: config.require("accountId"),
    region: config.require("region"),
    environment: config.require("environment"),
    slug: config.require("slug"),
    githubRepository: config.require("githubRepository"),
    githubSubjectPrefix: config.get("githubSubjectPrefix") ?? "",
  });
}
