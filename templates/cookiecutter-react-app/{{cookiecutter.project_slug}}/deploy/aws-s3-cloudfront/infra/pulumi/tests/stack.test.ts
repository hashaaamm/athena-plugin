// The whole stack, built against the recorder in mocks.ts — what `pulumi up` would send, read back.
import * as pulumi from "@pulumi/pulumi";
import { beforeEach, describe, expect, it } from "vitest";

import { CACHE_POLICY, HSTS_MAX_AGE } from "../src/components/cdn";
import { siteReadStatements } from "../src/policy";
import { deploy, type Exports } from "../src/stack";
import {
  ACCOUNT,
  CACHING_OPTIMIZED_ID,
  DISTRIBUTION_ARN,
  DISTRIBUTION_DOMAIN,
  EXPORTS,
  OIDC_PROVIDER_ARN,
  SITE_BUCKET,
  type Recorder,
  REGION,
  config,
  mock,
  settled,
  valueOf,
} from "./mocks";

let recorder: Recorder;
let exports: Exports;
/** Resource options as the program declared them, by logical name — what the engine is sent. */
let options: Map<string, pulumi.CustomResourceOptions>;

/**
 * Builds the stack the way `pulumi up` runs the program: inside a root stack resource. That root is
 * what lets a stack transformation see every resource's options — the mocks are sent only the
 * inputs, and `dependsOn` is an option.
 */
async function build(overrides: Parameters<typeof config>[0] = {}): Promise<void> {
  await pulumi.runtime.runInPulumiStack(async () => {
    pulumi.runtime.registerStackTransformation((args) => {
      options.set(args.name, args.opts as pulumi.CustomResourceOptions);
      return undefined;
    });
    exports = deploy(config(overrides));
    return {};
  });
  await settled();
}

function policyOf(name: string): { Statement: Record<string, unknown>[] } {
  return JSON.parse(recorder.named(name).inputs.policy as string);
}

function trustOf(name: string): Record<string, unknown> {
  return JSON.parse(recorder.named(name).inputs.assumeRolePolicy as string).Statement[0];
}

beforeEach(async () => {
  recorder = await mock();
  options = new Map();
});

describe("exports", () => {
  it("are exactly the names the pipelines are built against", async () => {
    await build();
    expect(Object.keys(exports).sort()).toEqual(EXPORTS);
  });

  it("carry the values a pipeline needs", async () => {
    await build();
    expect(await valueOf(exports.aws_account_id!)).toBe(ACCOUNT);
    expect(await valueOf(exports.aws_region!)).toBe(REGION);
    expect(await valueOf(exports.site_bucket!)).toBe(SITE_BUCKET);
    expect(await valueOf(exports.site_url!)).toBe(`https://${DISTRIBUTION_DOMAIN}`);
    expect(await valueOf(exports.github_subject_prefix!)).toBe("repo:acme/example-app");
  });
});

describe("the provider", () => {
  it("is the only one, and refuses any other account", async () => {
    await build();
    const providers = recorder.ofType("pulumi:providers:aws");
    expect(providers).toHaveLength(1);
    // A provider's configuration reaches the engine as strings, a list as its JSON.
    expect(JSON.parse(providers[0]!.inputs.allowedAccountIds as string)).toEqual([ACCOUNT]);
    expect(providers[0]!.inputs.region).toBe(REGION);
  });

  it("is passed explicitly to every resource and every lookup", async () => {
    // Pulumi.yaml disables the default provider, so a resource without this one fails at apply —
    // in the account of whoever is logged in, rather than here.
    await build();
    const resources = recorder.resources.filter((r) => r.custom && !r.type.startsWith("pulumi:providers:"));
    expect(resources.length).toBeGreaterThan(0);
    for (const resource of resources) {
      expect(resource.provider, `${resource.type} ${resource.name}`).toMatch(/pulumi:providers:aws::aws::/);
    }
    expect(recorder.calls.length).toBeGreaterThan(0);
    for (const call of recorder.calls) {
      expect(call.provider, call.token).toMatch(/pulumi:providers:aws::aws::/);
    }
  });
});

describe("the site bucket", () => {
  it("blocks every kind of public access and turns ACLs off", async () => {
    await build();
    expect(recorder.named("site-public-access").inputs).toMatchObject({
      blockPublicAcls: true,
      ignorePublicAcls: true,
      blockPublicPolicy: true,
      restrictPublicBuckets: true,
    });
    expect(recorder.named("site-ownership").inputs.rule).toEqual({ objectOwnership: "BucketOwnerEnforced" });
  });

  it("is encrypted with SSE-S3", async () => {
    await build();
    expect(recorder.named("site-encryption").inputs.rules).toEqual([
      { applyServerSideEncryptionByDefault: { sseAlgorithm: "AES256" } },
    ]);
  });

  it("is not a website, and is not versioned", async () => {
    await build();
    expect(recorder.ofType("aws:s3/bucketWebsiteConfiguration:BucketWebsiteConfiguration")).toEqual([]);
    expect(recorder.ofType("aws:s3/bucketVersioning:BucketVersioning")).toEqual([]);
  });

  it("can be destroyed with objects in it outside production, and not in it", async () => {
    await build();
    expect(recorder.named("site").inputs.forceDestroy).toBe(true);
    recorder = await mock();
    await build({ environment: "prod" });
    expect(recorder.named("site").inputs.forceDestroy).toBe(false);
  });

  it("is readable by CloudFront for this distribution and by nobody else", async () => {
    await build();
    const distributionArn = DISTRIBUTION_ARN;
    expect(policyOf("site-read").Statement).toEqual(
      siteReadStatements(`arn:aws:s3:::${SITE_BUCKET}`, distributionArn),
    );
    const [statement] = policyOf("site-read").Statement;
    expect(statement).toMatchObject({
      Effect: "Allow",
      Principal: { Service: "cloudfront.amazonaws.com" },
      Action: ["s3:GetObject"],
      Condition: { StringEquals: { "AWS:SourceArn": distributionArn } },
    });
    expect(policyOf("site-read").Statement).toHaveLength(1);
  });

  it("gets its policy only once public access is blocked and ACLs are off", async () => {
    await build();
    const after = options.get("site-read")?.dependsOn as pulumi.Resource[];
    expect(after).toHaveLength(3);
  });
});

describe("the distribution", () => {
  async function distribution(): Promise<Record<string, unknown>> {
    await build();
    return recorder.named("site-distribution").inputs;
  }

  it("reads the bucket through Origin Access Control, signing every request", async () => {
    const inputs = await distribution();
    const oac = recorder.ofType("aws:cloudfront/originAccessControl:OriginAccessControl");
    expect(oac).toHaveLength(1);
    expect(oac[0]!.inputs).toMatchObject({
      originAccessControlOriginType: "s3",
      signingBehavior: "always",
      signingProtocol: "sigv4",
    });
    const [origin] = inputs.origins as Record<string, unknown>[];
    expect(origin).toMatchObject({
      domainName: `${SITE_BUCKET}.s3.${REGION}.amazonaws.com`,
      originAccessControlId: `${oac[0]!.name}-id`,
    });
    // The legacy identity is the other way in. Neither half of it is configured.
    expect(origin!.s3OriginConfig).toBeUndefined();
  });

  it("redirects HTTP to HTTPS and serves only reads", async () => {
    const behavior = (await distribution()).defaultCacheBehavior as Record<string, unknown>;
    expect(behavior.viewerProtocolPolicy).toBe("redirect-to-https");
    expect(behavior.allowedMethods).toEqual(["GET", "HEAD"]);
  });

  it("falls back to index.html with a 200 for any path S3 has no object for", async () => {
    const inputs = await distribution();
    expect(inputs.defaultRootObject).toBe("index.html");
    expect(inputs.customErrorResponses).toEqual(
      [403, 404].map((errorCode) => ({
        errorCode,
        responseCode: 200,
        responsePagePath: "/index.html",
        errorCachingMinTtl: 0,
      })),
    );
  });

  it("caches with the managed CachingOptimized policy and compresses", async () => {
    const behavior = (await distribution()).defaultCacheBehavior as Record<string, unknown>;
    expect(behavior.cachePolicyId).toBe(CACHING_OPTIMIZED_ID);
    expect(behavior.compress).toBe(true);
    const lookup = recorder.calls.find((c) => c.token === "aws:cloudfront/getCachePolicy:getCachePolicy");
    expect(lookup?.inputs.name).toBe(CACHE_POLICY);
  });

  it("sends HSTS, nosniff, DENY framing and a strict referrer policy", async () => {
    const behavior = (await distribution()).defaultCacheBehavior as Record<string, unknown>;
    const [policy] = recorder.ofType("aws:cloudfront/responseHeadersPolicy:ResponseHeadersPolicy");
    expect(behavior.responseHeadersPolicyId).toBe(`${policy!.name}-id`);
    expect(policy!.inputs.securityHeadersConfig).toEqual({
      strictTransportSecurity: { accessControlMaxAgeSec: HSTS_MAX_AGE, includeSubdomains: true, override: true },
      contentTypeOptions: { override: true },
      frameOptions: { frameOption: "DENY", override: true },
      referrerPolicy: { referrerPolicy: "strict-origin-when-cross-origin", override: true },
    });
  });

  it("is the cheapest price class, on the default certificate, with HTTP/3 and IPv6", async () => {
    const inputs = await distribution();
    expect(inputs).toMatchObject({
      enabled: true,
      priceClass: "PriceClass_100",
      httpVersion: "http2and3",
      isIpv6Enabled: true,
      viewerCertificate: { cloudfrontDefaultCertificate: true },
    });
  });
});

describe("the CI roles", () => {
  it("trust GitHub's provider, for this repository's production environment and pull requests", async () => {
    await build();
    for (const [role, subject] of [
      ["deploy-role", "repo:acme/example-app:environment:production"],
      ["preview-role", "repo:acme/example-app:pull_request"],
    ] as const) {
      expect(trustOf(role)).toEqual({
        Effect: "Allow",
        Principal: { Federated: OIDC_PROVIDER_ARN },
        Action: ["sts:AssumeRoleWithWebIdentity"],
        Condition: {
          StringEquals: {
            "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
            "token.actions.githubusercontent.com:sub": subject,
          },
        },
      });
    }
  });

  it("trust the subject GitHub issues when the repository uses the immutable form", async () => {
    const prefix = "repo:acme@1234/example-app@5678";
    await build({ githubSubjectPrefix: prefix });
    expect(trustOf("deploy-role")).toMatchObject({
      Condition: { StringEquals: { "token.actions.githubusercontent.com:sub": `${prefix}:environment:production` } },
    });
    expect(trustOf("preview-role")).toMatchObject({
      Condition: { StringEquals: { "token.actions.githubusercontent.com:sub": `${prefix}:pull_request` } },
    });
    expect(await valueOf(exports.github_subject_prefix!)).toBe(prefix);
  });

  it("let deploy write this bucket's objects and invalidate this distribution, and nothing else", async () => {
    await build();
    const bucket = `arn:aws:s3:::${SITE_BUCKET}`;
    expect(policyOf("deploy-writes-site").Statement).toEqual([
      { Sid: "ListSite", Effect: "Allow", Action: ["s3:ListBucket"], Resource: [bucket] },
      {
        Sid: "WriteSite",
        Effect: "Allow",
        Action: ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
        Resource: [`${bucket}/*`],
      },
      {
        Sid: "InvalidateSite",
        Effect: "Allow",
        Action: ["cloudfront:CreateInvalidation", "cloudfront:GetInvalidation"],
        Resource: [DISTRIBUTION_ARN],
      },
    ]);
    // No managed policy rides along with it.
    const attachments = recorder.ofType("aws:iam/rolePolicyAttachment:RolePolicyAttachment");
    expect(attachments.map((a) => a.inputs.role)).not.toContain("example-app-deploy-dev");
  });

  it("let preview read the account and this project's state, and write only its own locks", async () => {
    await build();
    const attachments = recorder.ofType("aws:iam/rolePolicyAttachment:RolePolicyAttachment");
    expect(attachments.map((a) => [a.inputs.role, a.inputs.policyArn])).toEqual([
      ["example-app-preview-dev", "arn:aws:iam::aws:policy/ReadOnlyAccess"],
    ]);
    const state = `arn:aws:s3:::pulumi-state-${ACCOUNT}-${REGION}`;
    const statements = policyOf("preview-reads-state").Statement;
    expect(statements).toContainEqual({
      Sid: "ListState",
      Effect: "Allow",
      Action: ["s3:ListBucket"],
      Resource: [state],
      Condition: { StringLikeIfExists: { "s3:prefix": [".pulumi/*"] } },
    });
    expect(statements).toContainEqual({
      Sid: "WriteOwnLocks",
      Effect: "Allow",
      Action: ["s3:PutObject", "s3:DeleteObject"],
      Resource: [`${state}/.pulumi/locks/organization/site/*`],
    });
    expect(statements).toContainEqual({
      Sid: "UseStateKey",
      Effect: "Allow",
      Action: ["kms:Decrypt", "kms:GenerateDataKey"],
      Resource: [expect.stringMatching(/^arn:aws:kms:/)],
    });
    const writes = statements.flatMap((s) => s.Action as string[]).filter((a) => /Put|Delete/.test(a));
    expect(writes).toEqual(["s3:PutObject", "s3:DeleteObject"]);
  });
});
