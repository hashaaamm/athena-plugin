// The contract between the stack, the scripts around it, and the pipelines.
//
// The failure this file exists to prevent: a stack exporting `bucket` and a pipeline reading
// `vars.SITE_BUCKET`. Nothing errors. The deploy job is simply skipped forever, or uploads to
// nowhere, because it is reading a variable nothing ever sets.
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { DEPLOY_ENVIRONMENT, GITHUB_AUDIENCE, GITHUB_OIDC_HOST, STATE_KEY_ALIAS } from "../src/policy";
import { EXPORTS } from "./mocks";

const INFRA = join(__dirname, "..", "..");
const WORKFLOWS = join(INFRA, "..", ".github", "workflows");

/** Repository variables a human sets by hand, because they come from outside this stack. */
const MANUAL_VARIABLES = [
  // The backend's address. It belongs to another repository's stack, so nothing here can export it.
  "API_URL",
];

/** Exports no pipeline reads. Listed, so a new export that should have been synced fails a test. */
const NOT_SYNCED = [
  // Read by sync-github.sh to check the trust policies against GitHub, never copied into a variable.
  "github_subject_prefix",
];

function read(...path: string[]): string {
  return readFileSync(join(...path), "utf8");
}

const syncScript = (): string => read(INFRA, "scripts", "sync-github.sh");
const workflow = (name: string): string => read(WORKFLOWS, name);

/** output -> variable, for every row in sync-github.sh. */
function syncMapping(): Map<string, string> {
  const rows = [...syncScript().matchAll(/"([a-z_]+):([A-Z0-9_]+)"/g)];
  expect(rows.length).toBeGreaterThan(0);
  return new Map(rows.map((row) => [row[1]!, row[2]!]));
}

function provided(): string[] {
  return [...syncMapping().values(), ...MANUAL_VARIABLES];
}

function referenced(text: string): string[] {
  return [...new Set([...text.matchAll(/vars\.([A-Z0-9_]+)/g)].map((m) => m[1]!))];
}

/** Lines that are not comments — what a shell or the runner would actually execute. */
function code(text: string): string {
  return text
    .split("\n")
    .filter((line) => !line.trim().startsWith("#"))
    .join("\n");
}

describe("stack -> script -> pipeline", () => {
  it("syncs only outputs the stack exports", () => {
    expect([...syncMapping().keys()].filter((o) => !EXPORTS.includes(o))).toEqual([]);
  });

  it("syncs every export, or lists it as deliberately not synced", () => {
    const mapping = syncMapping();
    for (const name of EXPORTS) {
      expect(mapping.has(name), `${name}: add it to sync-github.sh or to NOT_SYNCED`).toBe(!NOT_SYNCED.includes(name));
    }
  });

  it.each(["cd.yml", "infra.yml", "ci.yml"])("sets every variable %s reads", (name) => {
    expect(referenced(workflow(name)).filter((v) => !provided().includes(v))).toEqual([]);
  });

  it("sets no GitHub secret: every value is a public identifier", () => {
    expect(syncScript()).not.toContain("gh secret set");
  });

  it("checks the subject GitHub issues before it un-gates anything", () => {
    const script = syncScript();
    expect(script).toContain("actions/oidc/customization/sub");
    expect(script).toContain("read_output github_subject_prefix");
    expect(script.indexOf("MISMATCH")).toBeLessThan(script.indexOf("gh variable set"));
  });
});

describe("the pipelines", () => {
  it("stay inert until the stack has been applied and synced", () => {
    expect(workflow("cd.yml")).toContain("if: vars.AWS_DEPLOY_ROLE_ARN != ''");
    const infra = workflow("infra.yml");
    expect(infra).toContain("vars.AWS_PREVIEW_ROLE_ARN != ''");
    // A pull request gets the read-only preview role, never the one that can publish.
    expect(infra).not.toContain("AWS_DEPLOY_ROLE_ARN");
  });

  it("deploy from the GitHub environment the deploy role trusts", () => {
    expect(workflow("cd.yml")).toContain(`environment: ${DEPLOY_ENVIRONMENT}`);
  });

  it("refuse to build a bundle with no API origin compiled into it", () => {
    const cd = code(workflow("cd.yml"));
    const refusal = cd.indexOf('if [ -z "$API_URL" ]');
    expect(refusal).toBeGreaterThan(-1);
    expect(refusal).toBeLessThan(cd.indexOf("run: pnpm build"));
    // Escaped braces: this file is rendered by cookiecutter, and two unescaped ones are Jinja.
    expect(cd).toMatch(/VITE_API_URL: \$\{\{ steps\.origin\.outputs\.url \}\}/);
  });

  it("cache hashed assets forever and the index never, uploading assets first", () => {
    const cd = code(workflow("cd.yml"));
    const assets = cd.indexOf('--cache-control "public,max-age=31536000,immutable"');
    const index = cd.indexOf('--exclude "assets/*" --cache-control "no-cache" --delete');
    expect(assets).toBeGreaterThan(-1);
    expect(index).toBeGreaterThan(assets);
  });

  it("invalidate the index and wait for it with a deadline, not a fixed-cap waiter", () => {
    const cd = code(workflow("cd.yml"));
    expect(cd).toContain('--paths "/index.html" "/"');
    expect(cd).toContain("aws cloudfront get-invalidation");
    expect(cd).toContain("DEADLINE=");
    expect(cd).not.toContain("wait invalidation-completed");
  });

  it("smoke-test the index and a route only the client knows", () => {
    const cd = code(workflow("cd.yml"));
    expect(cd).toContain('"$SITE_URL/"');
    expect(cd).toContain('"$SITE_URL/some/deep/route"');
  });

  it.each(["ci.yml", "cd.yml", "infra.yml"])("%s is filtered to what it builds", (name) => {
    // Without a filter, a typo fix in a README rebuilds and republishes the site.
    expect(workflow(name)).toContain("paths:");
  });

  it.each(["ci.yml", "cd.yml", "infra.yml"])("%s carries no cookiecutter variable", (name) => {
    // .github/workflows/ is _copy_without_render, because an Actions expression and a Jinja one are
    // written with the same two braces. A cookiecutter variable here would survive as literal text.
    // The patterns put the second character in a class so this file — which IS rendered — holds no
    // Jinja delimiter of its own.
    expect(workflow(name)).not.toMatch(/\{[{]\s*cookiecutter|\{[%#]/);
  });
});

describe("bootstrap and teardown", () => {
  it.each(["state-bucket.sh", "teardown.sh"])("%s names what the stack looks up", (script) => {
    // The same names as the backend templates' bootstrap, so a site and a service in one account
    // share one backend, one key and one OIDC provider instead of creating a second of each.
    const source = read(INFRA, "bootstrap", script);
    expect(source).toContain('BUCKET="pulumi-state-${ACCOUNT}-${REGION}"');
    expect(source).toContain(`KEY_ALIAS="${STATE_KEY_ALIAS}"`);
    expect(source).toContain(GITHUB_OIDC_HOST);
  });

  it("bootstraps a versioned, KMS-encrypted, TLS-only bucket and the OIDC provider", () => {
    const source = read(INFRA, "bootstrap", "state-bucket.sh");
    for (const want of [GITHUB_AUDIENCE, "enable-key-rotation", "BucketKeyEnabled", "aws:SecureTransport", "Status=Enabled"]) {
      expect(source).toContain(want);
    }
    // S3 and CloudFront need no service-linked role created in advance; ECS Express Mode does.
    expect(source).not.toContain("create-service-linked-role");
  });

  it.each(["state-bucket.sh", "teardown.sh"])("%s refuses credentials for another account", (script) => {
    const source = read(INFRA, "bootstrap", script);
    expect(source).toContain("sts get-caller-identity");
    expect(source).toContain('!= "${ACCOUNT}"');
  });

  it("will not strand a stack: every stack in the backend must be empty, and a human confirms", () => {
    const source = read(INFRA, "bootstrap", "teardown.sh");
    for (const want of ["stack export", "stack ls --all", "! -t 0", '"${reply}" != "${BUCKET}"']) {
      expect(source).toContain(want);
    }
  });

  it("will not delete a backend another project shares (Pulumi standards MUST-8)", () => {
    const source = read(INFRA, "bootstrap", "teardown.sh");
    const listing = source.indexOf(".pulumi/stacks/");
    const deletion = source.indexOf("s3api delete-bucket ");
    expect(listing).toBeGreaterThan(-1);
    expect(listing).toBeLessThan(deletion);
    expect(source).toContain("PULUMI_PROJECT");
  });

  it("keeps an OIDC provider some other role still trusts", () => {
    const source = read(INFRA, "bootstrap", "teardown.sh");
    expect(source.indexOf("iam list-roles")).toBeGreaterThan(-1);
    expect(source.indexOf("iam list-roles")).toBeLessThan(source.indexOf("delete-open-id-connect-provider"));
  });
});

describe("the recipes", () => {
  const justfile = read(INFRA, "justfile");
  const line = (needle: string): string =>
    justfile.split("\n").find((l) => l.includes(needle) && !l.trim().startsWith("#")) ?? "";

  it.each(["pulumi preview", "pulumi up", "pulumi destroy"])("%s refreshes", (command) => {
    expect(line(command)).toContain("--refresh");
  });

  it("never approve an apply for you", () => {
    expect(line("--yes")).toBe("");
  });
});
