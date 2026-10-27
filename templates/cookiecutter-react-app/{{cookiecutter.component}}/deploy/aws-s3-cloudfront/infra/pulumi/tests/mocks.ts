/**
 * Pulumi's engine, replaced by a recorder. No credentials, no network, no cloud.
 *
 * `pulumi.runtime.setMocks` routes every resource registration and every provider function call
 * here instead of to a deployment engine. The recorder keeps what it was sent, answers with the
 * properties AWS would have computed — an ARN, a domain name — and the tests read both.
 */
import * as pulumi from "@pulumi/pulumi";

import { StackConfig, type StackInputs } from "../src/config";

export const ACCOUNT = "123456789012";
export const REGION = "eu-west-1";
export const PROJECT = "site";
export const STACK = "dev";

/** The managed policies' ids, as AWS documents them. */
export const CACHING_OPTIMIZED_ID = "658327ea-f89d-4fab-a63d-7e88639e58f6";
export const CACHING_DISABLED_ID = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad";
export const ALL_VIEWER_EXCEPT_HOST_ID = "b689b0a8-53d0-40ab-baf2-68738e2966ac";
const CACHE_POLICY_IDS: Record<string, string> = {
  "Managed-CachingOptimized": CACHING_OPTIMIZED_ID,
  "Managed-CachingDisabled": CACHING_DISABLED_ID,
};
export const OIDC_PROVIDER_ARN = `arn:aws:iam::${ACCOUNT}:oidc-provider/token.actions.githubusercontent.com`;
export const STATE_KEY_ARN = `arn:aws:kms:${REGION}:${ACCOUNT}:key/00000000-0000-0000-0000-000000000000`;
export const DISTRIBUTION_ARN = `arn:aws:cloudfront::${ACCOUNT}:distribution/EDFDVBD6EXAMPLE`;
export const DISTRIBUTION_DOMAIN = "d111111abcdef8.cloudfront.net";
export const SITE_BUCKET = `example-app-site-dev-${ACCOUNT}-${REGION}`;

/** Every output the stack has. tests/contract.test.ts holds the scripts and workflows to it. */
export const EXPORTS = [
  "aws_account_id",
  "aws_region",
  "site_bucket",
  "cloudfront_distribution_id",
  "site_url",
  "deploy_role_arn",
  "preview_role_arn",
  "apply_role_arn",
  "github_subject_prefix",
  "api_url",
  "api_upstream",
].sort();

export interface Registration {
  type: string;
  name: string;
  inputs: Record<string, unknown>;
  /** The provider reference — `<urn>::<id>` — or undefined when the default provider was used. */
  provider?: string;
  custom: boolean;
}

export interface Invocation {
  token: string;
  inputs: Record<string, unknown>;
  provider?: string;
}

export class Recorder implements pulumi.runtime.Mocks {
  readonly resources: Registration[] = [];
  readonly calls: Invocation[] = [];

  newResource(args: pulumi.runtime.MockResourceArgs): pulumi.runtime.MockResourceResult {
    this.resources.push({
      type: args.type,
      name: args.name,
      inputs: args.inputs,
      provider: args.provider,
      custom: args.custom ?? true,
    });
    return { id: `${args.name}-id`, state: { ...args.inputs, ...computed(args) } };
  }

  call(args: pulumi.runtime.MockCallArgs): pulumi.runtime.MockCallResult {
    this.calls.push({ token: args.token, inputs: args.inputs, provider: args.provider });
    switch (args.token) {
      case "aws:cloudfront/getCachePolicy:getCachePolicy":
        return { id: CACHE_POLICY_IDS[args.inputs.name as string], name: args.inputs.name };
      case "aws:cloudfront/getOriginRequestPolicy:getOriginRequestPolicy":
        return {
          id: args.inputs.name === "Managed-AllViewerExceptHostHeader" ? ALL_VIEWER_EXCEPT_HOST_ID : undefined,
          name: args.inputs.name,
        };
      case "aws:iam/getOpenIdConnectProvider:getOpenIdConnectProvider":
        return { arn: OIDC_PROVIDER_ARN, id: OIDC_PROVIDER_ARN, url: args.inputs.url };
      case "aws:kms/getKey:getKey":
        return { arn: STATE_KEY_ARN, id: STATE_KEY_ARN, keyId: args.inputs.keyId };
      default:
        throw new Error(`unmocked call ${args.token}`);
    }
  }

  /** Every registration of one resource type. */
  ofType(type: string): Registration[] {
    return this.resources.filter((r) => r.type === type);
  }

  /** The one registration with this logical name. */
  named(name: string): Registration {
    const found = this.resources.filter((r) => r.name === name);
    if (found.length !== 1) throw new Error(`${found.length} resources named ${name}`);
    return found[0]!;
  }
}

/** What AWS computes on create, for the properties the stack reads back. */
function computed(args: pulumi.runtime.MockResourceArgs): Record<string, unknown> {
  const inputs = args.inputs as Record<string, string>;
  switch (args.type) {
    case "aws:s3/bucket:Bucket":
      return {
        arn: `arn:aws:s3:::${inputs.bucket}`,
        bucketRegionalDomainName: `${inputs.bucket}.s3.${REGION}.amazonaws.com`,
      };
    case "aws:cloudfront/distribution:Distribution":
      return { arn: DISTRIBUTION_ARN, domainName: DISTRIBUTION_DOMAIN };
    case "aws:iam/role:Role":
      return { arn: `arn:aws:iam::${ACCOUNT}:role/${inputs.name}` };
    case "aws:cloudfront/function:Function":
      return { arn: `arn:aws:cloudfront::${ACCOUNT}:function/${inputs.name}` };
    default:
      return {};
  }
}

/**
 * Installs a fresh recorder as Pulumi's engine. Each test gets its own, because the stack is built
 * once per test and a recorder shared between them would hold both builds.
 */
export async function mock(): Promise<Recorder> {
  const recorder = new Recorder();
  await pulumi.runtime.setMocks(recorder, PROJECT, STACK, false);
  return recorder;
}

/**
 * Waits until every resource constructed so far has been registered with the recorder. Pulumi
 * holds the process open for each registration it has started — this is the same wait the engine
 * does before a program exits — so a test can then read the recorder as the whole stack.
 */
export async function settled(): Promise<void> {
  await pulumi.runtime.disconnect();
}

/** The value of an output, once the mocks have resolved it. */
export function valueOf<T>(input: pulumi.Input<T>): Promise<T> {
  return new Promise((resolve) => pulumi.output(input).apply((value) => resolve(value as T)));
}

/** A valid configuration, with any field overridden. */
export function config(overrides: Partial<StackInputs> = {}): StackConfig {
  return new StackConfig({
    accountId: ACCOUNT,
    region: REGION,
    environment: "dev",
    slug: "example-app",
    githubRepository: "acme/example-app",
    githubSubjectPrefix: "",
    ...overrides,
  });
}
