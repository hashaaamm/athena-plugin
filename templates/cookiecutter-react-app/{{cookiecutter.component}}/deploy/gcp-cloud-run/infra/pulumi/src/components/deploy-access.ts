/**
 * What the deploy account may do, granted on the resources themselves rather than on the project.
 *
 * Three grants and nothing more: push to this registry repository, update this Cloud Run service,
 * and deploy it to run as the runtime account. A project-level `run.developer` would be every
 * service in the project; a project-level `artifactregistry.writer` would be every repository —
 * including the backend's, in a project the two share.
 *
 * A component of its own because it names the registry and the service, which do not exist when
 * the accounts are created.
 */
import * as gcp from "@pulumi/gcp";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";

export interface DeployAccessArgs {
  deployEmail: pulumi.Input<string>;
  /** The runtime account's resource name, `projects/…/serviceAccounts/…`. */
  runtimeAccount: pulumi.Input<string>;
  repository: gcp.artifactregistry.Repository;
  /** Absent until the first deploy creates it; the grant on it comes with it. */
  service?: gcp.cloudrunv2.Service;
}

export class DeployAccess extends pulumi.ComponentResource {
  constructor(config: StackConfig, args: DeployAccessArgs, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:DeployAccess", config.name(config.slug, "deploy-access"), {}, opts);
    const child = { parent: this };
    const member = pulumi.interpolate`serviceAccount:${args.deployEmail}`;

    // `docker push` of the image CD just built, to this repository only.
    new gcp.artifactregistry.RepositoryIamMember(
      "deploy-pushes-images",
      {
        project: config.project,
        location: config.region,
        repository: args.repository.repositoryId,
        role: "roles/artifactregistry.writer",
        member,
      },
      child,
    );

    // `gcloud run services update --image`, on this service only — once it exists.
    if (args.service) {
      new gcp.cloudrunv2.ServiceIamMember(
        "deploy-updates-service",
        {
          project: config.project,
          location: config.region,
          name: args.service.name,
          role: "roles/run.developer",
          member,
        },
        child,
      );
    }

    // A revision runs *as* the runtime account, and deploying one needs explicit permission to act
    // as it. Without this the deploy fails with a permission error that does not mention
    // impersonation anywhere. Granted on the one account rather than on the project:
    // `iam.serviceAccountUser` on the project is the right to run anything as any account in it.
    new gcp.serviceaccount.IAMMember(
      "deploy-acts-as-runtime",
      { serviceAccountId: args.runtimeAccount, role: "roles/iam.serviceAccountUser", member },
      child,
    );

    this.registerOutputs({});
  }
}
