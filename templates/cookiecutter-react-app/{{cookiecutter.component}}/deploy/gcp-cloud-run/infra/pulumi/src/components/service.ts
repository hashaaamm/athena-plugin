/**
 * Cloud Run: the nginx image that serves the built bundle.
 *
 * Created on the first real image and never before. `just infra-first-deploy` builds and pushes it,
 * sets `serviceImage`, and applies; until then the stack has no service at all, rather than one
 * serving a placeholder that answers 200 and is not the site.
 *
 * Division of ownership, which is what keeps Pulumi and the CD pipeline from fighting: **this stack
 * owns the shape** — memory, scaling, the runtime account, the probe, who may call it — and **the
 * pipeline owns the image** from the first CD run on. The image is declared with `ignoreChanges`.
 *
 * `ignoreChanges` alone is **not sufficient**, and getting this wrong silently rolls the site back
 * to the first deploy's image. It suppresses the *diff* between the program and Pulumi's state — but
 * when CD updates the image out of band, state is never told. Program and state both still say
 * `serviceImage`, so there is no diff to ignore, and the next update of any unrelated field sends the
 * whole template, stale image included. The missing half is `--refresh`: it pulls the live image into
 * state first, and `ignoreChanges` then preserves *that*. Every apply path here passes it, and the
 * justfile recipes are the enforcement.
 */
import * as gcp from "@pulumi/gcp";
import * as pulumi from "@pulumi/pulumi";

import type { StackConfig } from "../config";

/** The field CD writes. Spelled once, because a typo here is a silent rollback on the next apply. */
export const PIPELINE_OWNED = ["template.containers[0].image", "client", "clientVersion"];

/**
 * `nginx-unprivileged` binds $PORT, which the production image defaults to 8080 and Cloud Run sets
 * to the container port below.
 */
export const PORT = 8080;

/**
 * `/` rather than a health endpoint. A static bundle has no dependency it could be un-ready about,
 * so it ships none and needs none — nginx answers `/` from disk the moment it is listening, which
 * is exactly what the probe is asking.
 */
export const HEALTH_PATH = "/";

/**
 * The variable the image's nginx reads the API's host from (docker/default.conf.template). Set, it
 * proxies `/api/` and `/health/` there; unset — the image's default is empty — those paths are 404s.
 * Runtime configuration, not part of the image: the image CD builds is the same whichever backend
 * it fronts, and changing the backend is an apply, not a rebuild.
 */
export const API_UPSTREAM_ENV = "API_UPSTREAM_HOST";

export interface ServiceArgs {
  runtimeEmail: pulumi.Input<string>;
}

export class Service extends pulumi.ComponentResource {
  readonly service: gcp.cloudrunv2.Service;
  readonly url: pulumi.Output<string>;

  constructor(config: StackConfig, args: ServiceArgs, opts?: pulumi.ComponentResourceOptions) {
    super("site:infra:Service", config.name(config.slug, "service"), {}, opts);
    if (!config.hasService) throw new Error("the Cloud Run service needs serviceImage: run just infra-first-deploy");

    this.service = new gcp.cloudrunv2.Service(
      "site",
      {
        project: config.project,
        location: config.region,
        name: config.name(config.slug),
        ingress: "INGRESS_TRAFFIC_ALL",
        // Nothing here holds data: the image is in the registry and the bundle is in the image.
        deletionProtection: false,
        template: {
          // Its own account, never the default compute one — which holds Editor on the project.
          serviceAccount: args.runtimeEmail,
          // Serving a file holds nothing per request: no ORM, no model client, no import graph.
          // The limit is bandwidth, so one small instance takes many requests at once.
          maxInstanceRequestConcurrency: 80,
          scaling: { minInstanceCount: config.minInstances, maxInstanceCount: config.maxInstances },
          containers: [
            {
              image: config.serviceImage,
              ports: { containerPort: PORT },
              // The API on the site's own origin: nginx proxies /api/ and /health/ to this host, so
              // the app and the API are one site. Absent when the browser calls the API directly.
              envs: config.apiOrigin ? [{ name: API_UPSTREAM_ENV, value: config.apiOrigin }] : [],
              resources: {
                limits: { cpu: "1", memory: "256Mi" },
                cpuIdle: true, // bill for CPU only while a request is in flight
                // Without this, `cpuIdle` throttles the container during startup too. It costs
                // nothing extra: the boost applies only while starting.
                startupCpuBoost: true,
              },
              // Tight: nginx is listening in under a second, so a broken revision should report in
              // half a minute rather than after a budget sized for an application server.
              startupProbe: {
                initialDelaySeconds: 5,
                periodSeconds: 5,
                failureThreshold: 6,
                httpGet: { path: HEALTH_PATH, port: PORT },
              },
            },
          ],
        },
      },
      // The pipeline owns the image; this stack owns everything around it.
      { parent: this, ignoreChanges: PIPELINE_OWNED },
    );

    // Public, because a website nothing can reach is not yet a website.
    new gcp.cloudrunv2.ServiceIamMember(
      "site-public",
      {
        project: config.project,
        location: config.region,
        name: this.service.name,
        role: "roles/run.invoker",
        member: "allUsers",
      },
      { parent: this },
    );

    this.url = this.service.uri;
    this.registerOutputs({ url: this.url });
  }
}
