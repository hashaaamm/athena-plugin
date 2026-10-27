/**
 * The composition root.
 *
 * Reads top to bottom as a dependency graph: the provider, then the APIs because everything needs
 * them, then the things with no other dependencies, then the service that ties them together, and
 * last the deploy account's grants, which name the registry and the service. `deploy` is a function
 * of a validated configuration, so the tests build exactly what `pulumi up` builds, against mocks.
 *
 * Exports are named for what consumes them. Each one becomes a GitHub repository variable:
 * infra/scripts/sync-github.sh reads them straight out of `pulumi stack output`, and the names on
 * the right of its mapping must match the `vars.` names in .github/workflows exactly. A stack
 * exporting `workload_identity_provider` to a pipeline reading `vars.WIF_PROVIDER` is a deploy that
 * never runs and never says why, which is why tests/contract.test.ts checks all three.
 */
import * as gcp from "@pulumi/gcp";
import * as pulumi from "@pulumi/pulumi";

import { Apis } from "./components/apis";
import { DeployAccess } from "./components/deploy-access";
import { Identities } from "./components/identities";
import { Registry } from "./components/registry";
import { Service } from "./components/service";
import type { StackConfig } from "./config";

/** Every stack output, by name. */
export type Exports = Record<string, pulumi.Input<string>>;

export function deploy(config: StackConfig): Exports {
  // One explicit provider. Pulumi.yaml disables the default GCP provider, because a default one
  // would pick up ambient gcloud configuration and make two machines plan differently.
  const provider = new gcp.Provider("gcp", { project: config.project, region: config.region });
  // A component given a provider hands it to every child, so this one option reaches every
  // resource and every lookup below — and tests/stack.test.ts fails on any that it does not.
  const opts = { provider };

  const project = gcp.organizations.getProjectOutput({ projectId: config.project }, opts);

  const apis = new Apis(config, opts);
  // Everything below needs the APIs enabled first, or the first `up` fails halfway through.
  const afterApis = { ...opts, dependsOn: apis.services };

  const registry = new Registry(config, afterApis);
  const identities = new Identities(config, { projectNumber: project.number }, afterApis);
  // The service only once there is a real image to run: `just infra-first-deploy` builds it, sets
  // serviceImage and applies again. Until then everything else exists, and the exports that name
  // the service are empty — which is what stops sync-github.sh un-gating CD too early.
  const site = config.hasService
    ? new Service(config, { runtimeEmail: identities.runtime.email }, afterApis)
    : undefined;
  new DeployAccess(
    config,
    {
      deployEmail: identities.deploy.email,
      runtimeAccount: identities.runtime.name,
      repository: registry.repository,
      service: site?.service,
    },
    afterApis,
  );

  // --- Outputs. These are the CD pipeline's configuration. ---------------------------------------
  return {
    gcp_project: config.project,
    gcp_region: config.region,
    image_repo: registry.url,
    // The value google-github-actions/auth takes as `workload_identity_provider`.
    wif_provider: identities.provider.name,
    deploy_sa: identities.deploy.email,
    preview_sa: identities.preview.email,
    apply_sa: identities.apply.email,
    cloud_run_service: site?.service.name ?? "",
    // The address the smoke test calls, and the origin the backend's corsOrigins has to name — its
    // CSRF check compares every cookie request's Origin with that list, same-origin ones included.
    site_url: site?.url ?? "",
    // What every image CD builds compiles in as VITE_API_URL: `/` — the page's own origin — while
    // the site serves the API, the backend's own address otherwise. Empty until `apiUrl` is set.
    api_url: config.bundleApiUrl,
    // The backend's own address, as configured: where nginx proxies /api/ and /health/. The first
    // deploy checks it against the configuration, so a changed apiUrl is applied before a bundle is
    // built for it. Read, never synced.
    api_upstream: config.apiUrl,
  };
}
