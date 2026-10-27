// The configuration is validated once, when the stack loads — so a value AWS would refuse half way
// through an apply is refused here instead, with the key that is wrong.
import { describe, expect, it } from "vitest";

import { MAX_BUCKET_NAME, MAX_OAC_NAME, MAX_ROLE_NAME } from "../src/config";
import { ACCOUNT, REGION, config } from "./mocks";

describe("StackConfig", () => {
  it("accepts the generated configuration", () => {
    expect(() => config()).not.toThrow();
  });

  it.each([
    ["accountId", "12345", /accountId "12345" is not 12 digits/],
    ["region", "europe", /region "europe" is not a region code/],
    ["slug", "Example_App", /slug "Example_App" must be lowercase/],
    ["slug", "example--app", /slug "example--app" must be lowercase/],
    ["githubRepository", "acme", /githubRepository "acme" is not owner\/repo/],
    ["githubRepository", "acme/app/extra", /is not owner\/repo/],
    ["githubSubjectPrefix", "repo:acme/*", /githubSubjectPrefix .* is not a subject prefix/],
    ["githubSubjectPrefix", "acme/example-app", /githubSubjectPrefix .* is not a subject prefix/],
    ["githubSubjectPrefix", "repo:acme/example-app:", /githubSubjectPrefix .* is not a subject prefix/],
    ["environment", "", /environment is required/],
  ])("refuses %s = %j", (key, value, message) => {
    expect(() => config({ [key]: value })).toThrow(message);
  });

  it("is frozen, so no component can change what the next one sees", () => {
    const c = config();
    expect(Object.isFrozen(c)).toBe(true);
  });

  it("trusts the name-only subject unless a prefix is configured", () => {
    expect(config().subjectPrefix).toBe("repo:acme/example-app");
    const prefix = "repo:acme@1234/example-app@5678";
    expect(config({ githubSubjectPrefix: prefix }).subjectPrefix).toBe(prefix);
  });

  it("names the state bucket the bootstrap script creates", () => {
    expect(config().stateBucket).toBe(`pulumi-state-${ACCOUNT}-${REGION}`);
  });

  it("is production only for the prod stack", () => {
    expect(config().isProduction).toBe(false);
    expect(config({ environment: "prod" }).isProduction).toBe(true);
  });

  it("uses the GovCloud partition in a GovCloud region", () => {
    expect(config().partition).toBe("aws");
    expect(config({ region: "us-gov-west-1" }).partition).toBe("aws-us-gov");
  });

  it("trims a long slug, never the part that tells two resources apart", () => {
    const c = config({ slug: "a-very-long-product-name-that-somebody-chose-for-a-reason-x" });
    expect(c.siteBucketName.length).toBeLessThanOrEqual(MAX_BUCKET_NAME);
    expect(c.siteBucketName).toMatch(new RegExp(`-site-dev-${ACCOUNT}-${REGION}$`));
    expect(c.siteBucketName).not.toMatch(/--/);
    expect(c.roleName("preview").length).toBeLessThanOrEqual(MAX_ROLE_NAME);
    expect(c.roleName("preview")).toMatch(/-preview-dev$/);
    expect(c.oacName.length).toBeLessThanOrEqual(MAX_OAC_NAME);
  });

  it("names the bucket for the account and region, because bucket names are global", () => {
    expect(config().siteBucketName).toBe(`example-app-site-dev-${ACCOUNT}-${REGION}`);
  });
});
