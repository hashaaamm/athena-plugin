/**
 * The composition root.
 *
 * Reads top to bottom as a dependency graph: the provider first because everything goes through
 * it, then the bucket, then the distribution in front of it, then the roles that may deploy to
 * both. `deploy` is a function of a validated configuration, so the tests build exactly what
 * `pulumi up` builds, against mocks.
 *
 * Exports are named for what consumes them. Each one a pipeline reads becomes a GitHub repository
 * variable: infra/scripts/sync-github.sh reads them straight out of `pulumi stack output`, and the
 * names on the right of its mapping must match the `vars.` names in .github/workflows exactly. A
 * stack exporting `bucket` to a pipeline reading `vars.SITE_BUCKET` is a deploy that never runs and
 * never says why, which is why tests/contract.test.ts checks all three.
 */
import * as aws from "@pulumi/aws";
import * as pulumi from "@pulumi/pulumi";

import { Cdn } from "./components/cdn";
import { Identities } from "./components/identities";
import { SiteBucket } from "./components/site-bucket";
import type { StackConfig } from "./config";

/** Every stack output, by name. */
export type Exports = Record<string, pulumi.Input<string>>;

export function deploy(config: StackConfig): Exports {
  // One explicit provider. Pulumi.yaml disables the default AWS provider, because a default one
  // would pick up whatever profile and region the shell happens to have.
  //
  // allowedAccountIds is the wrong-account guard: credentials for any other account fail before
  // the provider reads or plans a single resource, rather than half way through creating this stack
  // somewhere it was never meant to be. The default tags are on every resource that takes tags, so
  // the console and the bill can both answer "what is this, and which stack owns it".
  const provider = new aws.Provider("aws", {
    region: config.region,
    allowedAccountIds: [config.accountId],
    defaultTags: {
      tags: {
        app: config.slug,
        environment: config.environment,
        "managed-by": "pulumi",
        "pulumi-stack": pulumi.getStack(),
      },
    },
  });
  // A component given a provider hands it to every child, so this one option reaches every
  // resource and every lookup below — and tests/stack.test.ts fails on any that it does not.
  const opts = { provider };

  const site = new SiteBucket(config, opts);
  const cdn = new Cdn(config, { bucket: site.bucket, bucketSettings: site.settings }, opts);
  const identities = new Identities(
    config,
    { bucketArn: site.bucket.arn, distributionArn: cdn.distribution.arn },
    opts,
  );

  // --- Outputs. These are the CD pipeline's configuration. ---------------------------------------
  return {
    aws_account_id: config.accountId,
    aws_region: config.region,
    site_bucket: site.bucket.bucket,
    cloudfront_distribution_id: cdn.distribution.id,
    // The address the smoke test calls, and the origin the backend's corsOrigins has to name — its
    // CSRF check compares every cookie request's Origin with that list, same-origin ones included.
    site_url: cdn.url,
    deploy_role_arn: identities.deploy.arn,
    preview_role_arn: identities.preview.arn,
    apply_role_arn: identities.apply.arn,
    // What the bundle is built with, as VITE_API_URL: `/` — the page's own origin — while the site
    // serves the API, the backend's own address otherwise. Empty until `apiUrl` is set;
    // sync-github.sh refuses to un-gate CD until it is not, because the bundle would call nothing.
    api_url: config.bundleApiUrl,
    // The backend's own address, as configured: what the distribution forwards /api/* and
    // /health/* to. The first deploy checks it against the configuration, so a changed apiUrl is
    // applied before a bundle is built for it. Read, never synced.
    api_upstream: config.apiUrl,
    // What the CI roles trust, so sync-github.sh can check it against the subject GitHub actually
    // issues for this repository before it un-gates the pipelines. Read, never synced.
    github_subject_prefix: config.subjectPrefix,
  };
}
