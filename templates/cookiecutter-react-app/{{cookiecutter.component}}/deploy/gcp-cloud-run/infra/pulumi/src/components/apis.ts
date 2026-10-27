/**
 * Enable the GCP service APIs everything else depends on.
 *
 * First in the graph, and everything else depends on it. Without that ordering the first `pulumi
 * up` fails halfway through with "API not enabled" errors that are individually obvious and
 * collectively a twenty-minute detour.
 *
 * **Three APIs cannot be enabled here.** Enabling an API is itself an API call — `serviceusage` —
 * authorised through `cloudresourcemanager`, and the GCP provider resolves the project's default
 * region and zone through `compute` before it plans a single resource. All three are needed
 * *before* this component can run, so infra/bootstrap/state-bucket.sh enables them, in plain
 * `gcloud`, which has no such problem.
 */
import * as gcp from "@pulumi/gcp";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";

/** Enabled by the bootstrap script, before Pulumi runs at all. Asserted against it by a test. */
export const BOOTSTRAP = [
  "cloudresourcemanager.googleapis.com", // authorises enabling anything else
  "serviceusage.googleapis.com", // the API that enables APIs
  "compute.googleapis.com", // the provider reads default region and zone from it
] as const;

/**
 * Everything the stack itself needs. No Secret Manager and no Cloud SQL: a static bundle reads
 * nothing at runtime, and nothing in it may be a secret.
 */
export const REQUIRED = [
  "run.googleapis.com",
  "artifactregistry.googleapis.com",
  "iam.googleapis.com",
  "iamcredentials.googleapis.com", // WIF mints the CI accounts' short-lived tokens through it
  "sts.googleapis.com", // and exchanges GitHub's token through this one
] as const;

export class Apis extends pulumi.ComponentResource {
  readonly services: gcp.projects.Service[];

  constructor(config: StackConfig, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:Apis", config.name(config.slug, "apis"), {}, opts);
    this.services = REQUIRED.map(
      (service) =>
        new gcp.projects.Service(
          `api-${service.split(".")[0]}`,
          {
            project: config.project,
            service,
            // Disabling an API on destroy would break anything else in the project using it —
            // the backend this site calls, most likely.
            disableOnDestroy: false,
          },
          { parent: this },
        ),
    );
    this.registerOutputs({});
  }
}
