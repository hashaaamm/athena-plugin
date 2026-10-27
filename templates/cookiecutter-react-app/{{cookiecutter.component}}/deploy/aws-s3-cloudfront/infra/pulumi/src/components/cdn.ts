/**
 * CloudFront in front of the site bucket: the only way in — and, by default, the way to the API too.
 *
 * Division of ownership, which is what keeps Pulumi and the CD pipeline from fighting: **this stack
 * owns the shape** — the bucket, the distribution, its caching and headers, who may read and who
 * may write — and **the pipeline owns the bucket's contents**. Nothing here declares an object, so
 * there is nothing for `ignoreChanges` to protect and nothing an apply could roll back; `--refresh`
 * on every preview and apply still keeps the distribution's live settings and the plan honest.
 *
 * **The API on the site's own origin.** With `apiSameOrigin` on — the default — the distribution has
 * the API as a second origin behind `/api/*` and `/health/*`, and the bundle calls the page's own
 * origin. `*.cloudfront.net` and the API's own hostname are two sites, so a `SameSite=Strict`
 * cookie the API sets — the backend templates' refresh cookie — would never be sent back to it, and
 * every reload would sign the user out; one origin is one site, and needs no CORS either. The API
 * is still reachable at its own address; the browser just no longer uses it.
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

/** The managed cache policy for the API: nothing cached, so no answer is ever served to the wrong caller. */
export const API_CACHE_POLICY = "Managed-CachingDisabled";

/**
 * The managed origin request policy for the API: every viewer header, cookie and query string —
 * `Authorization`, the refresh cookie and `Origin` included — except `Host`. The API's platform
 * routes on `Host` (ECS Express Mode's load balancer, Cloud Run's front end), and its certificate
 * names its own host, so it must be sent its own name and not the site's.
 */
export const API_ORIGIN_REQUEST_POLICY = "Managed-AllViewerExceptHostHeader";

/**
 * The paths the API answers on, from its root: its routes under `/api/v1/…`, and the health
 * endpoints the dashboard reads. Everything else is the site — so a client-side route under either
 * prefix would never reach the app.
 */
export const API_PATHS = ["/api/*", "/health/*"] as const;

/** The second origin's id, which the API's behaviours target. */
export const API_ORIGIN_ID = "api";

/**
 * How long CloudFront waits for the API to answer, in seconds. Sixty is the most CloudFront allows
 * without a quota increase, and the nginx proxy on the Cloud Run target waits the same.
 */
export const API_READ_TIMEOUT = 60;

/** Every method, because the API takes writes; the site's own behaviour takes only reads. */
export const API_METHODS = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"];

/** The bundle's entry point, and the answer to every client-side route. */
export const INDEX = "/index.html";

/**
 * The SPA fallback, as a viewer-request function on the site's own behaviour only. The router lives
 * in the browser, so `/account` is a route and not an object: a path whose last segment has no
 * extension is served the index, and a file — `/assets/index-abc123.js`, `/favicon.svg` — is served
 * as itself.
 *
 * Not the distribution's custom error responses, mapping S3's 403 and 404 to the index: they apply
 * to every behaviour, so the API's own 403s and 404s would reach the browser as the app's shell
 * with a 200.
 */
export const SPA_ROUTING_CODE = `function handler(event) {
  var request = event.request;
  var last = request.uri.split("/").pop();
  if (last.indexOf(".") === -1) {
    request.uri = "${INDEX}";
  }
  return request;
}
`;

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

    const spaRouting = new aws.cloudfront.Function(
      "site-spa-routing",
      {
        name: config.spaRoutingName,
        comment: `Serve index.html for ${config.slug} routes (${config.environment})`,
        runtime: "cloudfront-js-2.0",
        publish: true,
        code: SPA_ROUTING_CODE,
      },
      child,
    );

    // The headers a static site can set without knowing anything about the application. No
    // Content-Security-Policy yet: a CSP that is wrong breaks the page more thoroughly than a
    // missing one, and its connect-src depends on whether the API is on this origin. With the API
    // served here, `connect-src 'self'` is the one to start from.
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
    const api = config.apiOrigin ? apiRoute(config.apiOrigin, headers.id, child) : undefined;
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
          ...(api ? [api.origin] : []),
        ],
        defaultCacheBehavior: {
          targetOriginId: originId,
          viewerProtocolPolicy: "redirect-to-https",
          allowedMethods: ["GET", "HEAD"],
          cachedMethods: ["GET", "HEAD"],
          compress: true,
          cachePolicyId: caching.apply((policy) => required(policy.id, CACHE_POLICY)),
          responseHeadersPolicyId: headers.id,
          // The SPA fallback, on this behaviour and no other. The rewritten request is cached as
          // /index.html, which carries no-cache from the publish script and is what it
          // invalidates, so a deep link never serves an older deploy's shell.
          functionAssociations: [{ eventType: "viewer-request", functionArn: spaRouting.arn }],
        },
        // Matched before the default behaviour, in this order. Empty when the site does not serve
        // the API.
        orderedCacheBehaviors: api ? api.behaviors : [],
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

/**
 * The API as a second origin, and a behaviour per path it answers on. Nothing cached; every viewer
 * header but `Host` forwarded; HTTPS to the API with TLS 1.2, and HTTPS only from the viewer —
 * a redirect would drop a POST's body.
 */
function apiRoute(hostname: string, headersPolicyId: pulumi.Input<string>, child: pulumi.InvokeOutputOptions) {
  const caching = aws.cloudfront.getCachePolicyOutput({ name: API_CACHE_POLICY }, child);
  const forwarding = aws.cloudfront.getOriginRequestPolicyOutput({ name: API_ORIGIN_REQUEST_POLICY }, child);
  const origin: aws.types.input.cloudfront.DistributionOrigin = {
    originId: API_ORIGIN_ID,
    domainName: hostname,
    customOriginConfig: {
      httpPort: 80,
      httpsPort: 443,
      originProtocolPolicy: "https-only",
      originSslProtocols: ["TLSv1.2"],
      originReadTimeout: API_READ_TIMEOUT,
    },
  };
  const behaviors: aws.types.input.cloudfront.DistributionOrderedCacheBehavior[] = API_PATHS.map(
    (pathPattern) => ({
      pathPattern,
      targetOriginId: API_ORIGIN_ID,
      viewerProtocolPolicy: "https-only",
      allowedMethods: API_METHODS,
      cachedMethods: ["GET", "HEAD"],
      compress: true,
      cachePolicyId: caching.apply((policy) => required(policy.id, API_CACHE_POLICY)),
      originRequestPolicyId: forwarding.apply((policy) => required(policy.id, API_ORIGIN_REQUEST_POLICY)),
      responseHeadersPolicyId: headersPolicyId,
    }),
  );
  return { origin, behaviors };
}

function required(value: string | undefined, what: string): string {
  if (!value) throw new Error(`${what} was not found`);
  return value;
}
