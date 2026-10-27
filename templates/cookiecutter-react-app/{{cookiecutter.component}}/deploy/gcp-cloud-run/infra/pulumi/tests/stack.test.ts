// The whole stack, built against the recorder in mocks.ts — what `pulumi up` would send, read back.
import * as pulumi from "@pulumi/pulumi";
import { beforeEach, describe, expect, it } from "vitest";

import { BOOTSTRAP, REQUIRED } from "../src/components/apis";
import { APPLY_ENVIRONMENT, APPLY_ROLES, PREVIEW_ROLES, RUNTIME_ROLES } from "../src/components/identities";
import { API_UPSTREAM_ENV, HEALTH_PATH, PORT } from "../src/components/service";
import { deploy, type Exports } from "../src/stack";
import {
  EXPORTS,
  GCP_PROJECT,
  PROJECT_NUMBER,
  type Recorder,
  REGION,
  SERVICE_URL,
  config,
  mock,
  settled,
  valueOf,
} from "./mocks";

let recorder: Recorder;
let exports: Exports;
/** Resource options as the program declared them, by logical name — what the engine is sent. */
let options: Map<string, pulumi.CustomResourceOptions>;

/** The image `just infra-first-deploy` pushes and sets: <image_repo>/<service>:<commit>. */
const IMAGE = `${REGION}-docker.pkg.dev/${GCP_PROJECT}/example-app-dev/example-app-dev:0123456789abcdef`;

/**
 * Builds the stack the way `pulumi up` runs the program: inside a root stack resource. That root is
 * what lets a stack transformation see every resource's options — the mocks are sent only the
 * inputs, and `ignoreChanges` and `dependsOn` are options.
 *
 * By default, the stack as it is after the first deploy, with `serviceImage` set; `before()` is the
 * stack `just infra-up` creates before that.
 */
async function build(overrides: Parameters<typeof config>[0] = {}): Promise<void> {
  await pulumi.runtime.runInPulumiStack(async () => {
    pulumi.runtime.registerStackTransformation((args) => {
      options.set(args.name, args.opts as pulumi.CustomResourceOptions);
      return undefined;
    });
    exports = deploy(config({ serviceImage: IMAGE, apiUrl: "https://api.example.com", ...overrides }));
    return {};
  });
  await settled();
}

const before = (): Promise<void> => build({ serviceImage: "" });

beforeEach(async () => {
  recorder = await mock();
  options = new Map();
});

const sa = (role: string): string => `serviceAccount:example-app-${role}-dev@${GCP_PROJECT}.iam.gserviceaccount.com`;

describe("exports", () => {
  it("are exactly the names the pipelines are built against", async () => {
    await build();
    expect(Object.keys(exports).sort()).toEqual(EXPORTS);
  });

  it("carry the values a pipeline needs", async () => {
    await build();
    expect(await valueOf(exports.gcp_project!)).toBe(GCP_PROJECT);
    expect(await valueOf(exports.gcp_region!)).toBe(REGION);
    expect(await valueOf(exports.image_repo!)).toBe(`${REGION}-docker.pkg.dev/${GCP_PROJECT}/example-app-dev`);
    expect(await valueOf(exports.wif_provider!)).toMatch(
      new RegExp(`^projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/example-app-github-dev-[0-9a-f]{4}/providers/github$`),
    );
    expect(await valueOf(exports.deploy_sa!)).toBe(`example-app-deploy-dev@${GCP_PROJECT}.iam.gserviceaccount.com`);
    expect(await valueOf(exports.preview_sa!)).toBe(`example-app-preview-dev@${GCP_PROJECT}.iam.gserviceaccount.com`);
    expect(await valueOf(exports.cloud_run_service!)).toBe("example-app-dev");
    expect(await valueOf(exports.site_url!)).toBe(SERVICE_URL);
    // The site serves the API by default, so the bundle calls its own origin.
    expect(await valueOf(exports.api_url!)).toBe("/");
    expect(await valueOf(exports.api_upstream!)).toBe("https://api.example.com");
  });

  it("build the bundle against the backend's own address when it calls it directly", async () => {
    await build({ apiSameOrigin: false });
    expect(await valueOf(exports.api_url!)).toBe("https://api.example.com");
    expect(await valueOf(exports.api_upstream!)).toBe("https://api.example.com");
  });

  it("carry nothing for the bundle to call before apiUrl is set", async () => {
    await build({ apiUrl: "" });
    expect(await valueOf(exports.api_url!)).toBe("");
    expect(await valueOf(exports.api_upstream!)).toBe("");
  });

  it("name the image the way CD does: the service's name inside a registry repository of the same name", async () => {
    // first-deploy.sh pushes <image_repo>/<last segment of image_repo>:<commit> before the service
    // exists; CD pushes <image_repo>/<service>:<sha>. The two are one name only while this holds.
    await build();
    const repo = await valueOf(exports.image_repo!);
    expect(repo.split("/").pop()).toBe(await valueOf(exports.cloud_run_service!));
  });

  it("before the first deploy, leave the service and everything naming it empty", async () => {
    await before();
    expect(Object.keys(exports).sort()).toEqual(EXPORTS);
    expect(await valueOf(exports.cloud_run_service!)).toBe("");
    expect(await valueOf(exports.site_url!)).toBe("");
    expect(await valueOf(exports.image_repo!)).toBe(`${REGION}-docker.pkg.dev/${GCP_PROJECT}/example-app-dev`);
  });
});

describe("the provider", () => {
  it("is the only one, for this project and region", async () => {
    await build();
    const providers = recorder.ofType("pulumi:providers:gcp");
    expect(providers).toHaveLength(1);
    expect(providers[0]!.inputs).toMatchObject({ project: GCP_PROJECT, region: REGION });
  });

  it("is passed explicitly to every resource and every lookup", async () => {
    // Pulumi.yaml disables the default provider, so a resource without this one fails at apply.
    await build();
    const resources = recorder.resources.filter((r) => r.custom && !r.type.startsWith("pulumi:providers:"));
    expect(resources.length).toBeGreaterThan(0);
    for (const resource of resources) {
      expect(resource.provider, `${resource.type} ${resource.name}`).toMatch(/pulumi:providers:gcp::gcp::/);
    }
    expect(recorder.calls.length).toBeGreaterThan(0);
    for (const call of recorder.calls) {
      expect(call.provider, call.token).toMatch(/pulumi:providers:gcp::gcp::/);
    }
  });
});

describe("the APIs", () => {
  it("enable what the stack uses, and leave the bootstrap's to the bootstrap", async () => {
    await build();
    const enabled = recorder.ofType("gcp:projects/service:Service").map((r) => r.inputs.service);
    expect(enabled.sort()).toEqual([...REQUIRED].sort());
    // Enabling an API from two places is a race on a fresh project and a confusing diff after.
    expect(enabled.filter((api) => (BOOTSTRAP as readonly unknown[]).includes(api))).toEqual([]);
    for (const api of recorder.ofType("gcp:projects/service:Service")) {
      expect(api.inputs.disableOnDestroy).toBe(false);
    }
  });

  it("are a dependency of every other component, so nothing is created before its API is on", async () => {
    await build();
    for (const component of ["registry", "identities", "service", "deploy-access"]) {
      const name = `example-app-${component}-dev`;
      expect(options.get(name)?.dependsOn, name).toHaveLength(REQUIRED.length);
    }
  });
});

describe("the Cloud Run service", () => {
  async function service(): Promise<Record<string, unknown>> {
    await build();
    return recorder.named("site").inputs;
  }

  it("is created on the first deploy's real image, and the pipeline owns the image from then on", async () => {
    const inputs = await service();
    const [container] = (inputs.template as { containers: Record<string, unknown>[] }).containers;
    expect(container!.image).toBe(IMAGE);
    expect(options.get("site")?.ignoreChanges).toContain("template.containers[0].image");
  });

  it("does not exist before the first deploy, and nor does anything granted on it", async () => {
    await before();
    expect(recorder.ofType("gcp:cloudrunv2/service:Service")).toEqual([]);
    expect(recorder.ofType("gcp:cloudrunv2/serviceIamMember:ServiceIamMember")).toEqual([]);
    // Everything else is there: the registry the first image is pushed to, and the accounts.
    expect(recorder.ofType("gcp:artifactregistry/repository:Repository")).toHaveLength(1);
    expect(recorder.named("deploy-pushes-images").inputs.role).toBe("roles/artifactregistry.writer");
  });

  it("runs nginx on 8080, probed at / , as its own account", async () => {
    const template = (await service()).template as Record<string, unknown>;
    const [container] = template.containers as Record<string, unknown>[];
    expect(container!.ports).toEqual({ containerPort: PORT });
    expect(container!.startupProbe).toMatchObject({ httpGet: { path: HEALTH_PATH, port: PORT } });
    expect(HEALTH_PATH).toBe("/");
    expect(template.serviceAccount).toBe(`example-app-web-dev@${GCP_PROJECT}.iam.gserviceaccount.com`);
  });

  it("scales from zero to two, billing CPU only while a request is in flight", async () => {
    const template = (await service()).template as Record<string, unknown>;
    expect(template.scaling).toEqual({ minInstanceCount: 0, maxInstanceCount: 2 });
    const [container] = template.containers as { resources: Record<string, unknown> }[];
    expect(container!.resources).toMatchObject({ cpuIdle: true, startupCpuBoost: true });
  });

  it("tells nginx the API's host, so the site serves the API on its own origin", async () => {
    const template = (await service()).template as { containers: Record<string, unknown>[] };
    expect(template.containers[0]!.envs).toEqual([{ name: API_UPSTREAM_ENV, value: "api.example.com" }]);
    expect(API_UPSTREAM_ENV).toBe("API_UPSTREAM_HOST");
  });

  it("tells nginx nothing when the browser calls the API directly", async () => {
    await build({ apiSameOrigin: false });
    const template = recorder.named("site").inputs.template as { containers: Record<string, unknown>[] };
    expect(template.containers[0]!.envs).toEqual([]);
  });

  it("is public: anyone may invoke it", async () => {
    await build();
    expect(recorder.named("site-public").inputs).toMatchObject({ role: "roles/run.invoker", member: "allUsers" });
  });
});

describe("the identities", () => {
  it("pin Workload Identity Federation to this repository", async () => {
    await build();
    const provider = recorder.named("github-provider").inputs;
    expect(provider.attributeCondition).toBe('assertion.repository == "acme/example-app"');
    expect(provider.oidc).toEqual({ issuerUri: "https://token.actions.githubusercontent.com" });
    for (const name of ["github-impersonates-deploy", "github-impersonates-preview"]) {
      expect(recorder.named(name).inputs.member).toMatch(
        new RegExp(`^principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/[^/]+/attribute.repository/acme/example-app$`),
      );
    }
  });

  it("let deploy push to this repository, update this service and run it as the runtime account — nothing more", async () => {
    await build();
    const grants = recorder.resources
      .filter((r) => r.custom && r.inputs.member === sa("deploy"))
      .map((r) => [r.type, r.inputs.role]);
    expect(grants.sort()).toEqual(
      [
        ["gcp:artifactregistry/repositoryIamMember:RepositoryIamMember", "roles/artifactregistry.writer"],
        ["gcp:cloudrunv2/serviceIamMember:ServiceIamMember", "roles/run.developer"],
        ["gcp:serviceaccount/iAMMember:IAMMember", "roles/iam.serviceAccountUser"],
      ].sort(),
    );
    expect(recorder.named("deploy-updates-service").inputs.name).toBe("example-app-dev");
    expect(recorder.named("deploy-pushes-images").inputs.repository).toBe("example-app-dev");
    expect(recorder.named("deploy-acts-as-runtime").inputs.serviceAccountId).toBe(
      `projects/${GCP_PROJECT}/serviceAccounts/example-app-web-dev@${GCP_PROJECT}.iam.gserviceaccount.com`,
    );
  });

  it("let preview read the project and this backend's state, and deploy nothing", async () => {
    await build();
    const grants = recorder.resources
      .filter((r) => r.custom && r.inputs.member === sa("preview"))
      .map((r) => r.inputs.role);
    expect(grants.sort()).toEqual(
      [...PREVIEW_ROLES, "roles/storage.objectUser", "roles/cloudkms.cryptoKeyDecrypter"].sort(),
    );
    expect(recorder.named("preview-reads-state").inputs.bucket).toBe(`${GCP_PROJECT}-pulumi-state`);
    expect(recorder.named("preview-decrypts-stack-config").inputs.cryptoKeyId).toBe(
      `projects/${GCP_PROJECT}/locations/${REGION}/keyRings/pulumi/cryptoKeys/stack`,
    );
  });

  it("give the runtime account its logs and nothing else", async () => {
    await build();
    const grants = recorder.resources.filter((r) => r.custom && r.inputs.member === sa("web")).map((r) => r.inputs.role);
    expect(grants).toEqual([...RUNTIME_ROLES]);
  });

  it("hold no project-wide write role, the apply account apart", async () => {
    await build();
    const projectRoles = recorder
      .ofType("gcp:projects/iAMMember:IAMMember")
      .filter((r) => r.inputs.member !== sa("apply"))
      .map((r) => r.inputs.role as string);
    for (const role of projectRoles) {
      expect(["roles/logging.logWriter", "roles/viewer", "roles/iam.securityReviewer"]).toContain(role);
    }
  });

  it("let only the infrastructure environment impersonate the apply account", async () => {
    // The apply account can change anything in the project. What stands between it and a pull
    // request's preview, the CD job or any other workflow is the environment its token names —
    // mapped from GitHub's `environment` claim — and that environment's protection rules.
    await build();
    const provider = recorder.named("github-provider").inputs;
    expect((provider.attributeMapping as Record<string, string>)["attribute.environment"]).toBe(
      "assertion.environment",
    );
    expect(APPLY_ENVIRONMENT).toBe("infrastructure");
    const binding = recorder.named("github-impersonates-apply").inputs;
    expect(binding.role).toBe("roles/iam.workloadIdentityUser");
    expect(binding.member).toMatch(
      new RegExp(`^principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/[^/]+/attribute.environment/infrastructure$`),
    );
    expect(binding.serviceAccountId).toBe(
      `projects/${GCP_PROJECT}/serviceAccounts/example-app-apply-dev@${GCP_PROJECT}.iam.gserviceaccount.com`,
    );
  });

  it("let apply hold predefined roles, not basic ones, and administer only this backend's state", async () => {
    // Basic roles are ruled out in production by the GCP infrastructure standards. The apply account
    // gets one admin role per service the stack manages, on the project, and the state bucket and
    // key on those two resources alone.
    await build();
    expect(APPLY_ROLES).not.toContain("roles/owner");
    expect(APPLY_ROLES).not.toContain("roles/editor");
    const grants = recorder.resources
      .filter((r) => r.custom && r.inputs.member === sa("apply"))
      .map((r) => [r.type, r.inputs.role]);
    expect(grants.sort()).toEqual(
      [
        ...APPLY_ROLES.map((role) => ["gcp:projects/iAMMember:IAMMember", role]),
        ["gcp:serviceaccount/iAMMember:IAMMember", "roles/iam.serviceAccountUser"],
        ["gcp:storage/bucketIAMMember:BucketIAMMember", "roles/storage.admin"],
        ["gcp:kms/cryptoKeyIAMMember:CryptoKeyIAMMember", "roles/cloudkms.admin"],
        ["gcp:kms/cryptoKeyIAMMember:CryptoKeyIAMMember", "roles/cloudkms.cryptoKeyEncrypterDecrypter"],
      ].sort(),
    );
    expect(recorder.named("apply-administers-state").inputs.bucket).toBe(`${GCP_PROJECT}-pulumi-state`);
    for (const name of ["apply-administers-state-key", "apply-uses-state-key"]) {
      expect(recorder.named(name).inputs.cryptoKeyId).toBe(
        `projects/${GCP_PROJECT}/locations/${REGION}/keyRings/pulumi/cryptoKeys/stack`,
      );
    }
    expect(recorder.named("apply-acts-as-runtime").inputs.serviceAccountId).toBe(
      `projects/${GCP_PROJECT}/serviceAccounts/example-app-web-dev@${GCP_PROJECT}.iam.gserviceaccount.com`,
    );
  });
});
