// The configuration is validated once, when the stack loads — so a value GCP would refuse half way
// through an apply is refused here instead, with the key that is wrong.
import { describe, expect, it } from "vitest";

import { MAX_ACCOUNT_ID, MAX_POOL_ID } from "../src/config";
import { GCP_PROJECT, config } from "./mocks";

describe("StackConfig", () => {
  it("accepts the generated configuration, scaling from zero to two", () => {
    const c = config();
    expect(c.minInstances).toBe(0);
    expect(c.maxInstances).toBe(2);
  });

  it.each([
    ["project", "X", /project "X" is not a GCP project id/],
    ["region", "europe", /region "europe" is not a region/],
    ["slug", "Example_App", /slug "Example_App" must be lowercase/],
    ["githubRepository", "acme", /githubRepository "acme" is not owner\/repo/],
    ["environment", "", /environment is required/],
    ["minInstances", -1, /minInstances -1 \/ maxInstances 2/],
    ["maxInstances", 0, /maxInstances 0 is not a range/],
  ])("refuses %s = %j", (key, value, message) => {
    expect(() => config({ [key]: value })).toThrow(message);
  });

  it("is frozen, so no component can change what the next one sees", () => {
    expect(Object.isFrozen(config())).toBe(true);
  });

  it("names the state bucket the bootstrap script creates", () => {
    expect(config().stateBucket).toBe(`${GCP_PROJECT}-pulumi-state`);
  });

  it("fits service account ids into what GCP accepts, trimming the slug and nothing else", () => {
    const c = config({ slug: "a-very-long-product-name-for-a-site" });
    for (const role of ["web", "deploy", "preview"]) {
      const id = c.accountId(role);
      expect(id.length).toBeLessThanOrEqual(MAX_ACCOUNT_ID);
      expect(id).toMatch(new RegExp(`-${role}-dev$`));
      expect(id).not.toMatch(/--/);
    }
  });

  it("gives a pool id unique to the repository, the same every time it is computed", () => {
    const a = config();
    const b = config({ githubRepository: "someone-else/example-app" });
    expect(a.poolId("github")).toBe(config().poolId("github"));
    expect(a.poolId("github")).not.toBe(b.poolId("github"));
    expect(a.poolId("github")).toMatch(/^example-app-github-dev-[0-9a-f]{4}$/);
  });

  it("keeps two long slugs apart after the trim", () => {
    const one = config({ slug: "platform-billing-service-web-one" }).poolId("github");
    const two = config({ slug: "platform-billing-service-web-two" }).poolId("github");
    expect(one.length).toBeLessThanOrEqual(MAX_POOL_ID);
    expect(one).not.toBe(two);
  });
});
