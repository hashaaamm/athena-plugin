/**
 * Pulumi's engine, replaced by a recorder. No credentials, no network, no cloud.
 *
 * `pulumi.runtime.setMocks` routes every resource registration and every provider function call
 * here instead of to a deployment engine. The recorder keeps what it was sent, answers with the
 * properties GCP would have computed — an email, a URL, a resource name — and the tests read both.
 */
import * as pulumi from "@pulumi/pulumi";

import { StackConfig, type StackInputs } from "../src/config";

export const GCP_PROJECT = "acme-web-prod";
export const PROJECT_NUMBER = "123456789012";
export const REGION = "europe-west1";
export const PROJECT = "site";
export const STACK = "dev";
export const SERVICE_URL = "https://example-app-dev-abc123-ew.a.run.app";

/** Every output the stack has. tests/contract.test.ts holds the scripts and workflows to it. */
export const EXPORTS = [
  "gcp_project",
  "gcp_region",
  "image_repo",
  "wif_provider",
  "deploy_sa",
  "preview_sa",
  "apply_sa",
  "cloud_run_service",
  "site_url",
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
    if (args.token === "gcp:organizations/getProject:getProject") {
      return { number: PROJECT_NUMBER, projectId: args.inputs.projectId };
    }
    throw new Error(`unmocked call ${args.token}`);
  }

  ofType(type: string): Registration[] {
    return this.resources.filter((r) => r.type === type);
  }

  named(name: string): Registration {
    const found = this.resources.filter((r) => r.name === name);
    if (found.length !== 1) throw new Error(`${found.length} resources named ${name}`);
    return found[0]!;
  }
}

/** What GCP computes on create, for the properties the stack reads back. */
function computed(args: pulumi.runtime.MockResourceArgs): Record<string, unknown> {
  const inputs = args.inputs as Record<string, string>;
  switch (args.type) {
    case "gcp:serviceaccount/account:Account": {
      const email = `${inputs.accountId}@${inputs.project}.iam.gserviceaccount.com`;
      return { email, name: `projects/${inputs.project}/serviceAccounts/${email}` };
    }
    case "gcp:iam/workloadIdentityPoolProvider:WorkloadIdentityPoolProvider":
      return {
        name: `projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${inputs.workloadIdentityPoolId}/providers/${inputs.workloadIdentityPoolProviderId}`,
      };
    case "gcp:cloudrunv2/service:Service":
      return { uri: SERVICE_URL };
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
    project: GCP_PROJECT,
    region: REGION,
    environment: "dev",
    slug: "example-app",
    githubRepository: "acme/example-app",
    ...overrides,
  });
}
