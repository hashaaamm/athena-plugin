/**
 * CloudFront in front of the site bucket: the only way in.
 *
 * Division of ownership, which is what keeps Pulumi and the CD pipeline from fighting: **this stack
 * owns the shape** — the bucket, the distribution, its caching and headers, who may read and who
 * may write — and **the pipeline owns the bucket's contents**. Nothing here declares an object, so
 * there is nothing for `ignoreChanges` to protect and nothing an apply could roll back; `--refresh`
 * on every preview and apply still keeps the distribution's live settings and the plan honest.
 *
 * The default `*.cloudfront.net` certificate needs nothing from us. A domain of your own needs an
 * ACM certificate in us-east-1 — CloudFront reads certificates, and WAF web ACLs, from that region
 * only — which is a second, us-east-1 provider and an `aliases` entry here. infra/README.md has it.
 */
import * as aws from "@pulumi/aws";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";
import { documentOf, siteReadStatements } from "../policy";

/** The managed cache policy for static content: long TTLs, compression keys, no cookies or query. */
export const CACHE_POLICY = "Managed-CachingOptimized";

/** The bundle's entry point, and the answer to every path S3 has no object for. */
export const INDEX = "/index.html";

/**
 * Two years, in seconds — the figure hstspreload.org recommends, and long enough that a visitor who
 * has been here once never makes a plain-HTTP first request again.
 */
export const HSTS_MAX_AGE = 63_072_000;

export interface CdnArgs {
  bucket: aws.s3.Bucket;
  /** What must exist before the bucket policy is applied — `SiteBucket.settings`. */
  bucketSettings: pulumi.Resource[];
}

export class Cdn extends pulumi.ComponentResource {
  readonly distribution: aws.cloudfront.Distribution;
  readonly bucketPolicy: aws.s3.BucketPolicy;
  /** `https://<id>.cloudfront.net` — the site's address until a domain of your own replaces it. */
  readonly url: pulumi.Output<string>;

  constructor(config: StackConfig, args: CdnArgs, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:Cdn", config.name(config.slug, "cdn"), {}, opts);
    const child = { parent: this };

    // Origin Access Control, not the legacy origin access identity: OAC signs every request to S3
    // with SigV4, works with SSE-KMS and every region, and is what AWS recommends. `always`, so an
    // unsigned request never reaches the bucket — the bucket would refuse it anyway, but a request
    // CloudFront forwards unsigned is a request a viewer's own headers could shape.
    const oac = new aws.cloudfront.OriginAccessControl(
      "site-oac",
      {
        name: config.oacName,
        description: `${config.slug} (${config.environment}): CloudFront reads the site bucket`,
        originAccessControlOriginType: "s3",
        signingBehavior: "always",
        signingProtocol: "sigv4",
      },
      child,
    );

    const caching = aws.cloudfront.getCachePolicyOutput({ name: CACHE_POLICY }, child);

    // The headers a static site can set without knowing anything about the application. No
    // Content-Security-Policy: its connect-src is the API's origin, which is a build-time value
    // this stack never sees, and a CSP that is wrong breaks the page more thoroughly than a missing
    // one. Add it here once the API's origin is a domain you own.
    const headers = new aws.cloudfront.ResponseHeadersPolicy(
      "site-headers",
      {
        name: config.name(config.slug, "site-headers"),
        comment: `Security headers for ${config.slug} (${config.environment})`,
        securityHeadersConfig: {
          // Not `preload`: getting onto the browsers' preload list is a commitment for every
          // subdomain of a domain you own, and *.cloudfront.net is not yours.
          strictTransportSecurity: {
            accessControlMaxAgeSec: HSTS_MAX_AGE,
            includeSubdomains: true,
            override: true,
          },
          contentTypeOptions: { override: true },
          // Nothing here is meant to be framed. DENY, not SAMEORIGIN: the cheapest clickjacking
          // defence is the one with no exceptions to reason about.
          frameOptions: { frameOption: "DENY", override: true },
          referrerPolicy: { referrerPolicy: "strict-origin-when-cross-origin", override: true },
        },
      },
      child,
    );

    const originId = "site-bucket";
    this.distribution = new aws.cloudfront.Distribution(
      "site-distribution",
      {
        enabled: true,
        comment: `${config.slug} (${config.environment})`,
        defaultRootObject: INDEX.slice(1),
        // Both, because a client that speaks HTTP/3 saves a round trip and one that does not falls
        // back without noticing.
        httpVersion: "http2and3",
        isIpv6Enabled: true,
        // North America and Europe edges only — the cheapest class, and the right one until the
        // users are somewhere else. Visitors elsewhere are still served, from a farther edge.
        priceClass: "PriceClass_100",
        origins: [
          {
            originId,
            // The REGIONAL name: the global one sends the first requests after creation through a
            // redirect to the region, which CloudFront then caches.
            domainName: args.bucket.bucketRegionalDomainName,
            originAccessControlId: oac.id,
          },
        ],
        defaultCacheBehavior: {
          targetOriginId: originId,
          viewerProtocolPolicy: "redirect-to-https",
          allowedMethods: ["GET", "HEAD"],
          cachedMethods: ["GET", "HEAD"],
          compress: true,
          cachePolicyId: caching.apply((policy) => required(policy.id, CACHE_POLICY)),
          responseHeadersPolicyId: headers.id,
        },
        // The SPA fallback. The router lives in the browser, so /account is a route and not an
        // object. S3 answers a key it does not have with 403, not 404, when the reader has no
        // s3:ListBucket — which CloudFront deliberately does not — so both are mapped to the index
        // with a 200, and the browser's router takes it from there.
        //
        // The cost of mapping 403: a real access-denied from the bucket is served as the app's
        // shell rather than as an error. The bucket policy is tested so that does not happen
        // silently.
        //
        // Not cached: the fallback's body is index.html, and index.html must never be older than
        // the deploy. A cached error response would serve the previous deploy's shell for a deep
        // link until it expired, invalidation or not.
        customErrorResponses: [403, 404].map((errorCode) => ({
          errorCode,
          responseCode: 200,
          responsePagePath: INDEX,
          errorCachingMinTtl: 0,
        })),
        restrictions: { geoRestriction: { restrictionType: "none" } },
        viewerCertificate: { cloudfrontDefaultCertificate: true },
      },
      child,
    );

    this.bucketPolicy = new aws.s3.BucketPolicy(
      "site-read",
      {
        bucket: args.bucket.id,
        policy: documentOf([args.bucket.arn, this.distribution.arn], (bucketArn, distributionArn) =>
          siteReadStatements(bucketArn, distributionArn),
        ),
      },
      { ...child, dependsOn: args.bucketSettings },
    );

    this.url = pulumi.interpolate`https://${this.distribution.domainName}`;
    this.registerOutputs({ url: this.url, distributionId: this.distribution.id });
  }
}

function required(value: string | undefined, what: string): string {
  if (!value) throw new Error(`${what} was not found`);
  return value;
}
