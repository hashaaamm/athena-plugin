/** Artifact Registry: one Docker repository, for the nginx image this repository builds. */
import * as gcp from "@pulumi/gcp";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";

/**
 * Images kept by the cleanup policy. Every merge pushes an immutable `:sha` tag, so without a policy
 * the repository grows without bound and storage quietly becomes a line on the bill.
 */
export const KEEP_IMAGES = 10;

/** Anything older than this is deleted, whatever the keep count says. Thirty days, in seconds. */
export const DELETE_OLDER_THAN = "2592000s";

export class Registry extends pulumi.ComponentResource {
  readonly repository: gcp.artifactregistry.Repository;
  /** What `docker push` targets, and what the CD workflow reads as `IMAGE_REPO`. */
  readonly url: pulumi.Output<string>;

  constructor(config: StackConfig, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:Registry", config.name(config.slug, "registry"), {}, opts);

    this.repository = new gcp.artifactregistry.Repository(
      "images",
      {
        project: config.project,
        location: config.region,
        repositoryId: config.name(config.slug),
        format: "DOCKER",
        description: `Container images for ${config.slug}`,
        cleanupPolicies: [
          { id: "keep-recent", action: "KEEP", mostRecentVersions: { keepCount: KEEP_IMAGES } },
          { id: "delete-old", action: "DELETE", condition: { olderThan: DELETE_OLDER_THAN } },
        ],
      },
      { parent: this },
    );

    this.url = pulumi.interpolate`${config.registryHost}/${config.project}/${this.repository.repositoryId}`;
    this.registerOutputs({ url: this.url });
  }
}
