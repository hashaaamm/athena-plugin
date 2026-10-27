/**
 * IAM policy documents, as plain data.
 *
 * Every grant in the stack is built here by a pure function, and the components only attach the
 * result. A policy is the part of an AWS stack most worth reading and most often wrong, and a
 * function from ARNs to statements can be asserted on directly — no mocks, no outputs.
 */
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "./config";

/** GitHub Actions' token issuer, without the scheme: IAM's condition-key prefix for it. */
export const GITHUB_OIDC_HOST = "token.actions.githubusercontent.com";
/** The issuer the account's OIDC provider is registered with — what the stack looks it up by. */
export const GITHUB_OIDC_URL = `https://${GITHUB_OIDC_HOST}`;
/** The client id the provider accepts, and the `aud` every token must carry. */
export const GITHUB_AUDIENCE = "sts.amazonaws.com";
/**
 * The KMS key infra/bootstrap/state-bucket.sh creates once per account. It encrypts the state
 * bucket and every stack's secrets; the stack looks it up by this alias.
 */
export const STATE_KEY_ALIAS = "alias/pulumi-state";
/**
 * The GitHub environment the CD workflow's job runs in. The deploy role trusts a token from that environment
 * and nothing else, so its protection rules — not the branch — stand between a push and a deploy.
 */
export const DEPLOY_ENVIRONMENT = "production";
/**
 * The GitHub environment the infra workflow's apply job runs in. The apply role trusts a token from
 * that environment and nothing else, so its protection rules — required reviewers, and main as the
 * only branch that may use it — gate every apply.
 */
export const APPLY_ENVIRONMENT = "infrastructure";

export interface Statement {
  Sid?: string;
  Effect: "Allow" | "Deny";
  Principal?: Record<string, string>;
  Action: string[];
  Resource?: string[];
  Condition?: Record<string, Record<string, string | string[]>>;
}

export function allow(sid: string, actions: string[], ...resources: string[]): Statement {
  return { Sid: sid, Effect: "Allow", Action: actions, Resource: resources };
}

/** A policy document from statements, as the JSON string IAM and S3 take. */
export function document(statements: Statement[]): string {
  return JSON.stringify({ Version: "2012-10-17", Statement: statements });
}

/**
 * The same, for statements built from outputs: the document exists once every ARN does. The
 * statements are built by a pure function of plain strings, so the function is what the tests read.
 */
export function documentOf(
  inputs: pulumi.Input<string>[],
  build: (...values: string[]) => Statement[],
): pulumi.Output<string> {
  return pulumi.all(inputs).apply((values) => document(build(...values)));
}

/**
 * The `sub` claim of a token from the CD workflow's job. A job that names an environment gets this subject
 * whatever branch it ran from, so the environment's protection rules are the gate on a deploy.
 */
export function deploySubject(prefix: string): string {
  return `${prefix}:environment:${DEPLOY_ENVIRONMENT}`;
}

/**
 * The `sub` claim of a token from a pull_request workflow. GitHub mints no OIDC token for a pull
 * request from a fork, so this is the repository's own branches only.
 */
export function previewSubject(prefix: string): string {
  return `${prefix}:pull_request`;
}

/**
 * The `sub` claim of a token from the infra workflow's apply job. The branch is not in it, as for
 * the deploy role: the environment's deployment-branch rule is what keeps an apply to main.
 */
export function applySubject(prefix: string): string {
  return `${prefix}:environment:${APPLY_ENVIRONMENT}`;
}

/**
 * Admits a web-identity token from GitHub's OIDC provider with exactly this subject, minted for
 * STS. StringEquals rather than StringLike: a wildcard in a subject is how a trust policy written
 * for one repository ends up admitting every repository in the organisation.
 */
export function githubTrust(providerArn: string, subject: string): Statement {
  return {
    Effect: "Allow",
    Principal: { Federated: providerArn },
    Action: ["sts:AssumeRoleWithWebIdentity"],
    Condition: {
      StringEquals: {
        [`${GITHUB_OIDC_HOST}:aud`]: GITHUB_AUDIENCE,
        [`${GITHUB_OIDC_HOST}:sub`]: subject,
      },
    },
  };
}

/**
 * The site bucket's policy: CloudFront may read objects, on behalf of this one distribution, and
 * nobody else is granted anything. Without the SourceArn condition every distribution in every
 * account could be pointed at this bucket and serve it.
 */
export function siteReadStatements(bucketArn: string, distributionArn: string): Statement[] {
  return [
    {
      Sid: "CloudFrontReadsThisSite",
      Effect: "Allow",
      Principal: { Service: "cloudfront.amazonaws.com" },
      Action: ["s3:GetObject"],
      Resource: [`${bucketArn}/*`],
      Condition: { StringEquals: { "AWS:SourceArn": distributionArn } },
    },
  ];
}

/**
 * What the CD workflow does, and nothing more: `aws s3 sync` lists, reads, writes and deletes objects in
 * the site bucket, and a deploy invalidates this distribution and waits on that invalidation. No
 * bucket-level write — the pipeline cannot change the bucket's policy, encryption or ownership —
 * and no other bucket or distribution.
 */
export function deployStatements(bucketArn: string, distributionArn: string): Statement[] {
  return [
    allow("ListSite", ["s3:ListBucket"], bucketArn),
    allow("WriteSite", ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"], `${bucketArn}/*`),
    allow("InvalidateSite", ["cloudfront:CreateInvalidation", "cloudfront:GetInvalidation"], distributionArn),
  ];
}

/**
 * The preview role's access to this project's state, and to nothing else that could not already
 * be read. The same grants as the backend templates' preview role, line for line: the bucket and
 * the key are shared by every project in the account, so each grant is scoped to this one.
 */
export function previewStatements(config: StackConfig, project: string, stateKeyArn: string): Statement[] {
  const bucket = `arn:${config.partition}:s3:::${config.stateBucket}`;
  return [
    // IfExists, because the backend's first call is a HeadBucket — "is the bucket there and may I
    // use it" — which IAM authorises as s3:ListBucket with no prefix at all. A plain StringLike
    // fails that check and the preview reports a bucket that does not exist.
    {
      Sid: "ListState",
      Effect: "Allow",
      Action: ["s3:ListBucket"],
      Resource: [bucket],
      Condition: { StringLikeIfExists: { "s3:prefix": [".pulumi/*"] } },
    },
    // meta.yaml is the backend's format version, read before anything else; stacks/<project>/ is
    // this project's state and nobody else's.
    allow("ReadOwnState", ["s3:GetObject"], `${bucket}/.pulumi/meta.yaml`, `${bucket}/.pulumi/stacks/${project}/*`),
    // The DIY backend's lock objects. A preview takes none today; a refresh may, and a lock it
    // cannot write fails as a Pulumi error rather than as the missing permission it is. The path
    // is what pulumi v3.263 writes: .pulumi/locks/organization/<project>/<stack>/.
    allow("WriteOwnLocks", ["s3:PutObject", "s3:DeleteObject"], `${bucket}/.pulumi/locks/organization/${project}/*`),
    // Decrypt for the state objects and the stack's secrets; GenerateDataKey because the bucket's
    // default encryption is SSE-KMS, so writing a lock object asks the key for one.
    allow("UseStateKey", ["kms:Decrypt", "kms:GenerateDataKey"], stateKeyArn),
  ];
}
