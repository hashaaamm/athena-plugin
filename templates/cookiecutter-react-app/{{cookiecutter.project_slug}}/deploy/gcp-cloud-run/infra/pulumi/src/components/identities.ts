/**
 * Service accounts and Workload Identity Federation.
 *
 * Three accounts, and the split is the point. The **runtime** account is what nginx runs as, and
 * it holds one role: writing its own logs. The **deploy** account is what cd.yml impersonates; it
 * can push to this repository's registry, update this service and run it as the runtime account,
 * and those grants are attached in `deploy-access.ts` once the registry and the service exist.
 * The **preview** account is what a pull request's `pulumi preview` impersonates; it reads, and
 * writes nothing but the state backend's lock objects.
 *
 * None has a downloadable key. A service-account JSON key is the most-stolen credential class
 * there is, and the house rule is that CI authenticates with Workload Identity Federation instead
 * — ask Athena for the Pulumi standards and the GitHub Actions standards.
 */
import * as gcp from "@pulumi/gcp";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";

/** GitHub Actions' token issuer. */
export const GITHUB_ISSUER = "https://token.actions.githubusercontent.com";

/**
 * The KMS key ring and key infra/bootstrap/state-bucket.sh creates beside the state bucket. Named
 * here rather than passed in, because the script names them the same way and a test checks the two
 * agree — and named exactly as the backend templates name them, so a site and a service in one
 * project share one backend.
 */
export const KEYRING = "pulumi";
export const KEY = "stack";

/** Granted to the Cloud Run service. nginx serving files reads no database and no secret. */
export const RUNTIME_ROLES = ["roles/logging.logWriter"] as const;

/**
 * Granted to the account a pull request's preview runs as, across the project.
 *
 * A preview runs with `--refresh`, so it reads every resource in the stack, and refreshing an IAM
 * binding means reading an IAM policy — which `viewer` deliberately excludes, hence
 * `iam.securityReviewer` beside it. A hand-maintained list of narrower read roles breaks every time
 * the stack gains a resource type. What this widens, stated plainly: the preview can read resource
 * metadata across the project. Both roles are read-only.
 */
export const PREVIEW_ROLES = ["roles/viewer", "roles/iam.securityReviewer"] as const;

const member = (email: pulumi.Input<string>): pulumi.Output<string> => pulumi.interpolate`serviceAccount:${email}`;

export interface IdentitiesArgs {
  /** The project's number, which a `principalSet://` member is spelled with. */
  projectNumber: pulumi.Input<string>;
}

export class Identities extends pulumi.ComponentResource {
  readonly runtime: gcp.serviceaccount.Account;
  readonly deploy: gcp.serviceaccount.Account;
  readonly preview: gcp.serviceaccount.Account;
  readonly pool: gcp.iam.WorkloadIdentityPool;
  readonly provider: gcp.iam.WorkloadIdentityPoolProvider;

  constructor(config: StackConfig, args: IdentitiesArgs, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:Identities", config.name(config.slug, "identities"), {}, opts);
    const child = { parent: this };

    const account = (name: string, role: string, displayName: string): gcp.serviceaccount.Account =>
      new gcp.serviceaccount.Account(
        name,
        { project: config.project, accountId: config.accountId(role), displayName },
        child,
      );

    this.runtime = account("runtime-sa", "web", `${config.slug} runtime`);
    for (const role of RUNTIME_ROLES) {
      new gcp.projects.IAMMember(
        `runtime-${role.split("/")[1]}`,
        { project: config.project, role, member: member(this.runtime.email) },
        child,
      );
    }

    this.deploy = account("deploy-sa", "deploy", `${config.slug} deploy (CD)`);

    this.preview = account("preview-sa", "preview", `${config.slug} preview (pull requests)`);
    for (const role of PREVIEW_ROLES) {
      new gcp.projects.IAMMember(
        `preview-${role.split("/")[1]}`,
        { project: config.project, role, member: member(this.preview.email) },
        child,
      );
    }

    // Read and write the state bucket, so the preview can select the stack. Bucket-scoped rather
    // than a project storage role, because a project-level storage role is access to every bucket in
    // the project. `objectUser` rather than `objectViewer`: the self-managed backend writes a lock
    // object, and a read-only grant fails on the lock — which looks like a Pulumi bug rather than a
    // missing permission. The bucket is the bootstrap script's; only the binding is the stack's.
    new gcp.storage.BucketIAMMember(
      "preview-reads-state",
      { bucket: config.stateBucket, role: "roles/storage.objectUser", member: member(this.preview.email) },
      child,
    );

    // Reading the state is not enough to read the *stack*. Stack configuration is encrypted with the
    // key named by `secretsprovider`, so a preview that can list the bucket then fails on
    // `cloudkms.cryptoKeyVersions.useToDecrypt`. Decrypter and not encrypter: a preview reads
    // configuration and never writes any.
    new gcp.kms.CryptoKeyIAMMember(
      "preview-decrypts-stack-config",
      {
        cryptoKeyId: `projects/${config.project}/locations/${config.region}/keyRings/${KEYRING}/cryptoKeys/${KEY}`,
        role: "roles/cloudkms.cryptoKeyDecrypter",
        member: member(this.preview.email),
      },
      child,
    );

    this.pool = new gcp.iam.WorkloadIdentityPool(
      "github-pool",
      {
        project: config.project,
        workloadIdentityPoolId: config.poolId("github"),
        displayName: "GitHub Actions",
      },
      child,
    );

    this.provider = new gcp.iam.WorkloadIdentityPoolProvider(
      "github-provider",
      {
        project: config.project,
        workloadIdentityPoolId: this.pool.workloadIdentityPoolId,
        workloadIdentityPoolProviderId: "github",
        displayName: "GitHub OIDC",
        attributeMapping: {
          "google.subject": "assertion.sub",
          "attribute.repository": "assertion.repository",
          "attribute.ref": "assertion.ref",
        },
        // Without this condition, *any* GitHub repository in the world could mint a token for this
        // pool. It is the single most important line in this file.
        attributeCondition: `assertion.repository == "${config.githubRepository}"`,
        oidc: { issuerUri: GITHUB_ISSUER },
      },
      child,
    );

    // Only workflows in that repository may impersonate either CI account — scoped by repository,
    // as the backend templates' binding is. It does not tell cd.yml from a pull request's workflow:
    // what keeps a pull request on the preview account is that infra.yml asks for that one, and what
    // gates a deploy is the `production` environment's protection rules on cd.yml's job.
    const repository = pulumi.interpolate`principalSet://iam.googleapis.com/projects/${args.projectNumber}/locations/global/workloadIdentityPools/${this.pool.workloadIdentityPoolId}/attribute.repository/${config.githubRepository}`;
    for (const [name, sa] of [
      ["github-impersonates-deploy", this.deploy],
      ["github-impersonates-preview", this.preview],
    ] as const) {
      new gcp.serviceaccount.IAMMember(
        name,
        { serviceAccountId: sa.name, role: "roles/iam.workloadIdentityUser", member: repository },
        child,
      );
    }

    this.registerOutputs({
      runtimeEmail: this.runtime.email,
      deployEmail: this.deploy.email,
      previewEmail: this.preview.email,
    });
  }
}
