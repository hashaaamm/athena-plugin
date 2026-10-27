/**
 * The two roles GitHub Actions assumes, keyless, and what each may do.
 *
 * **Deploy** trusts a token from the `production` environment of one repository and may write the
 * site bucket's objects and invalidate this distribution — nothing else. **Preview** trusts a
 * token from a pull request in the same repository and may read the account and this project's
 * state, and change nothing but its own lock objects.
 *
 * Neither has a long-lived credential: GitHub's OIDC token is exchanged for a session of an hour,
 * and each role's trust policy says exactly whose token it accepts — ask Athena for the GitHub
 * Actions standards. The OIDC provider those tokens are checked against is looked up rather than
 * created: there is one per account, shared by every service and site in it, so the bootstrap
 * script owns it (ADR-0021). A stack that created it would delete it from under its neighbours on
 * its own `destroy`.
 */
import * as aws from "@pulumi/aws";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";
import {
  GITHUB_OIDC_URL,
  STATE_KEY_ALIAS,
  deploySubject,
  deployStatements,
  documentOf,
  githubTrust,
  previewStatements,
  previewSubject,
} from "../policy";

/** AWS's maintained read-everything policy, which a `pulumi preview --refresh` needs. */
export const READ_ONLY_POLICY = "ReadOnlyAccess";

export interface IdentitiesArgs {
  bucketArn: pulumi.Input<string>;
  distributionArn: pulumi.Input<string>;
}

export class Identities extends pulumi.ComponentResource {
  readonly deploy: aws.iam.Role;
  readonly preview: aws.iam.Role;

  constructor(config: StackConfig, args: IdentitiesArgs, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:Identities", config.name(config.slug, "identities"), {}, opts);
    const child = { parent: this };

    const provider = aws.iam.getOpenIdConnectProviderOutput({ url: GITHUB_OIDC_URL }, child).arn;

    this.deploy = new aws.iam.Role(
      "deploy-role",
      {
        name: config.roleName("deploy"),
        description: `GitHub Actions deploys ${config.slug} (${config.environment}) from cd.yml`,
        assumeRolePolicy: documentOf([provider], (arn) => [githubTrust(arn, deploySubject(config.subjectPrefix))]),
      },
      child,
    );
    new aws.iam.RolePolicy(
      "deploy-writes-site",
      {
        role: this.deploy.name,
        name: "write-site-and-invalidate",
        policy: documentOf([args.bucketArn, args.distributionArn], (bucketArn, distributionArn) =>
          deployStatements(bucketArn, distributionArn),
        ),
      },
      child,
    );

    this.preview = new aws.iam.Role(
      "preview-role",
      {
        name: config.roleName("preview"),
        description: `GitHub Actions previews ${config.slug} (${config.environment}) on a pull request`,
        assumeRolePolicy: documentOf([provider], (arn) => [githubTrust(arn, previewSubject(config.subjectPrefix))]),
      },
      child,
    );
    // `pulumi preview --refresh` reads every resource in the stack, so the preview role needs read
    // on every resource type the stack declares. ReadOnlyAccess is AWS's maintained list of exactly
    // that; a hand-written list breaks every time the stack gains a resource type, and breaks as an
    // AccessDenied in a pull request nobody can fix from the pull request.
    //
    // What it widens, stated plainly: CI can read resource metadata across the account, not only
    // this stack's. What it does NOT grant is any write.
    new aws.iam.RolePolicyAttachment(
      "preview-read-only",
      {
        role: this.preview.name,
        policyArn: `arn:${config.partition}:iam::aws:policy/${READ_ONLY_POLICY}`,
      },
      child,
    );

    // Reading the account is not reading the STACK. The state is an object in the bootstrap
    // bucket, encrypted with the bootstrap key, so a preview that can list resources still fails on
    // s3:GetObject, then on kms:Decrypt, one permission further in each time. Both are scoped to
    // this Pulumi project: the bucket is shared by every project in the account.
    const stateKey = aws.kms.getKeyOutput({ keyId: STATE_KEY_ALIAS }, child).arn;
    new aws.iam.RolePolicy(
      "preview-reads-state",
      {
        role: this.preview.name,
        name: "read-own-state",
        policy: documentOf([stateKey], (keyArn) => previewStatements(config, pulumi.getProject(), keyArn)),
      },
      child,
    );

    this.registerOutputs({ deployRoleArn: this.deploy.arn, previewRoleArn: this.preview.arn });
  }
}
