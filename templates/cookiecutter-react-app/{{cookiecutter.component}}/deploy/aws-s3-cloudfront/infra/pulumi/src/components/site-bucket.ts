/**
 * The bucket the built bundle lives in. Private, and nothing about it says "website".
 *
 * Not S3 static website hosting: a website endpoint is public HTTP, needs a public bucket, and
 * cannot sit behind Origin Access Control. The bucket here is reachable by exactly one principal —
 * CloudFront, on behalf of this stack's distribution — and that grant is the distribution's
 * component's business, because it names the distribution.
 */
import * as aws from "@pulumi/aws";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";

export class SiteBucket extends pulumi.ComponentResource {
  readonly bucket: aws.s3.Bucket;
  /**
   * The bucket-level settings that must be in place before anything grants access to it. S3
   * refuses some concurrent configuration calls on one bucket ("A conflicting conditional operation
   * is currently in progress"), and a policy applied before public access is blocked is a window,
   * however short, in which nothing blocks it.
   */
  readonly settings: pulumi.Resource[];

  constructor(config: StackConfig, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:SiteBucket", config.name(config.slug, "site"), {}, opts);
    const child = { parent: this };

    this.bucket = new aws.s3.Bucket(
      "site",
      {
        bucket: config.siteBucketName,
        // Every object here is a build output that CD can write again from any commit, so a
        // non-production bucket may be destroyed with its objects in it. In production it may
        // not: a `destroy` there should stop and make a human empty the bucket first.
        forceDestroy: !config.isProduction,
      },
      child,
    );

    // All four, not the account default and not some of them. A bucket that becomes public by one
    // policy edit is the most common way a "private" bucket stops being one.
    const publicAccess = new aws.s3.BucketPublicAccessBlock(
      "site-public-access",
      {
        bucket: this.bucket.id,
        blockPublicAcls: true,
        ignorePublicAcls: true,
        blockPublicPolicy: true,
        restrictPublicBuckets: true,
      },
      child,
    );

    // ACLs off. Access is the bucket policy and IAM, and nothing else can grant it by accident.
    const ownership = new aws.s3.BucketOwnershipControls(
      "site-ownership",
      { bucket: this.bucket.id, rule: { objectOwnership: "BucketOwnerEnforced" } },
      child,
    );

    // SSE-S3. A customer-managed KMS key would add a key policy CloudFront's service principal must
    // be granted, and a per-request KMS charge on every cache miss, to protect files whose whole
    // purpose is to be served to anyone who asks.
    const encryption = new aws.s3.BucketServerSideEncryptionConfiguration(
      "site-encryption",
      {
        bucket: this.bucket.id,
        rules: [{ applyServerSideEncryptionByDefault: { sseAlgorithm: "AES256" } }],
      },
      child,
    );

    // No versioning, deliberately. Every object is reproducible from git: a rollback is CD re-run
    // at the earlier commit, not a restore of old object versions. Versioned, every deploy's
    // `--delete` would leave the previous bundle behind as noncurrent versions, billed until a
    // lifecycle rule nobody remembers to write expires them.

    this.settings = [publicAccess, ownership, encryption];
    this.registerOutputs({ bucket: this.bucket.bucket, arn: this.bucket.arn });
  }
}
