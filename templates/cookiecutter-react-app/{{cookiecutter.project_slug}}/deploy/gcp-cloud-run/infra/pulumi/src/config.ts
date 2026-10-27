/**
 * Typed stack configuration.
 *
 * Every value the stack reads is resolved here, once. Components take a frozen `StackConfig` rather
 * than constructing `pulumi.Config` themselves, which is what keeps a component constructible in a
 * test and stops configuration keys being invented at three different call sites.
 */
import { createHash } from "node:crypto";

import * as pulumi from "@pulumi/pulumi";

/**
 * GCP service-account ids are 6-30 characters. The role and environment suffix are what tell two
 * accounts apart, so the slug is what has to give when a name is long.
 */
export const MAX_ACCOUNT_ID = 30;

/** Workload Identity Pool ids are 4-32 characters, and the same squeeze applies. */
export const MAX_POOL_ID = 32;

/**
 * Characters of digest appended to a pool id. Four is 65,536 values, which is not a cryptographic
 * guarantee and does not need to be: the thing it separates is two repositories a human
 * deliberately deployed into one project, not an adversary hunting a collision.
 */
export const DISAMBIGUATOR = 4;

/**
 * Cloud Run's ceiling, raised only by a quota request. Two is this stack's default: a container
 * that serves files from disk saturates the network long before it saturates two instances.
 */
const MAX_INSTANCES_CEILING = 100;

/**
 * Lowercase letters, digits and single hyphens, starting with a letter. The slug starts every
 * resource name, and Artifact Registry, Cloud Run and service accounts all refuse anything else.
 */
const SLUG = /^[a-z](?:[a-z0-9]|-(?=[a-z0-9]))*$/;
const PROJECT = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/;
const REGION = /^[a-z]+-[a-z]+[0-9]+$/;

/** The values `Pulumi.<stack>.yaml` supplies. */
export interface StackInputs {
  readonly project: string;
  readonly region: string;
  /**
   * The STACK's environment. All it does is name resources — the service is `<slug>-dev`, and
   * every account and repository carries the same suffix. A static bundle has no runtime
   * environment for it to configure.
   */
  readonly environment: string;
  /** The base name every resource is derived from — the cookiecutter slug. */
  readonly slug: string;
  /**
   * `owner/repo`. The Workload Identity provider's attribute condition is built from this, and it
   * is the single line that stops any repository on GitHub from assuming the CI identities.
   */
  readonly githubRepository: string;
  /** Scaling to zero is the biggest cost lever there is: an idle service costs nothing. */
  readonly minInstances?: number;
  readonly maxInstances?: number;
}

export class StackConfig implements Required<StackInputs> {
  readonly project: string;
  readonly region: string;
  readonly environment: string;
  readonly slug: string;
  readonly githubRepository: string;
  readonly minInstances: number;
  readonly maxInstances: number;

  /** Validates, then freezes. A configuration that would fail half way through an apply fails here. */
  constructor(inputs: StackInputs) {
    this.project = inputs.project.trim();
    this.region = inputs.region.trim();
    this.environment = inputs.environment.trim();
    this.slug = inputs.slug.trim();
    this.githubRepository = inputs.githubRepository.trim();
    this.minInstances = inputs.minInstances ?? 0;
    this.maxInstances = inputs.maxInstances ?? 2;
    const problems = this.problems();
    if (problems.length > 0) {
      throw new Error(`invalid stack configuration:\n  ${problems.join("\n  ")}`);
    }
    Object.freeze(this);
  }

  private problems(): string[] {
    const problems: string[] = [];
    const required: [string, string][] = [
      ["project", this.project],
      ["region", this.region],
      ["environment", this.environment],
      ["slug", this.slug],
      ["githubRepository", this.githubRepository],
    ];
    for (const [key, value] of required) {
      if (value === "") problems.push(`${key} is required`);
    }
    if (this.project && !PROJECT.test(this.project)) {
      problems.push(`project ${JSON.stringify(this.project)} is not a GCP project id`);
    }
    if (this.region && !REGION.test(this.region)) {
      problems.push(`region ${JSON.stringify(this.region)} is not a region such as europe-west1`);
    }
    if (this.slug && !SLUG.test(this.slug)) {
      problems.push(
        `slug ${JSON.stringify(this.slug)} must be lowercase letters, digits and single hyphens, starting with a letter`,
      );
    }
    if (this.environment && !SLUG.test(this.environment)) {
      problems.push(`environment ${JSON.stringify(this.environment)} must be lowercase letters and digits`);
    }
    const [owner, repo, ...rest] = this.githubRepository.split("/");
    if (!owner || !repo || rest.length > 0) {
      problems.push(`githubRepository ${JSON.stringify(this.githubRepository)} is not owner/repo`);
    }
    const { minInstances: min, maxInstances: max } = this;
    if (!Number.isInteger(min) || !Number.isInteger(max) || min < 0 || max < Math.max(min, 1) || max > MAX_INSTANCES_CEILING) {
      problems.push(`minInstances ${min} / maxInstances ${max} is not a range Cloud Run accepts`);
    }
    return problems;
  }

  /** A resource name that carries the environment, so two stacks can share a project safely. */
  name(...parts: string[]): string {
    return [...parts, this.environment].join("-");
  }

  /**
   * A service-account id for `role`, trimmed to what GCP accepts. The slug is the part that gets
   * shortened; the role suffix is unique per account within a stack, so trimming cannot collide.
   */
  accountId(role: string): string {
    const suffix = `-${role}-${this.environment}`;
    const account = this.slug.slice(0, MAX_ACCOUNT_ID - suffix.length).replace(/-+$/, "") + suffix;
    if (account.length < 6 || account.length > MAX_ACCOUNT_ID) {
      throw new Error(`service account id ${JSON.stringify(account)} is not 6-30 characters`);
    }
    return account;
  }

  /**
   * A Workload Identity Pool id, unique within the *project* rather than the stack.
   *
   * A pool id is unique across a GCP project, and a site and its backend deployed into the same
   * project both want one — so an id built from the kind and the environment alone is already
   * taken the second time, by a pool this stack does not own and must not adopt. Deleting a pool to
   * free its name does not help either: GCP holds a deleted id for 30 days.
   *
   * The trailing digest is taken over the *untrimmed* slug and the repository, so what a trim
   * throws away is still represented in the result, and the same slug in two repositories is two
   * pools. Deterministic, which is the whole requirement: a random suffix would replace the pool on
   * every run and put each old id into that 30-day hold.
   */
  poolId(kind: string): string {
    const digest = createHash("sha256")
      .update(`${this.slug}\n${this.githubRepository}`)
      .digest("hex")
      .slice(0, DISAMBIGUATOR);
    const suffix = `-${kind}-${this.environment}-${digest}`;
    const pool = this.slug.slice(0, MAX_POOL_ID - suffix.length).replace(/-+$/, "") + suffix;
    if (pool.length < 4 || pool.length > MAX_POOL_ID) {
      throw new Error(`workload identity pool id ${JSON.stringify(pool)} is not 4-32 characters`);
    }
    if (pool.startsWith("gcp-")) {
      throw new Error(`workload identity pool id ${JSON.stringify(pool)} may not start with 'gcp-'`);
    }
    return pool;
  }

  /** Where Docker pushes for this region. */
  get registryHost(): string {
    return `${this.region}-docker.pkg.dev`;
  }

  /**
   * The Pulumi state bucket infra/bootstrap/state-bucket.sh creates — named for the project alone,
   * so every service and site deployed into it shares one.
   */
  get stateBucket(): string {
    return `${this.project}-pulumi-state`;
  }
}

/** Reads `Pulumi.<stack>.yaml` into a validated `StackConfig`. The only place a key is spelled. */
export function load(): StackConfig {
  const config = new pulumi.Config();
  return new StackConfig({
    project: config.require("project"),
    region: config.require("region"),
    environment: config.require("environment"),
    slug: config.require("slug"),
    githubRepository: config.require("githubRepository"),
    minInstances: config.getNumber("minInstances"),
    maxInstances: config.getNumber("maxInstances"),
  });
}
