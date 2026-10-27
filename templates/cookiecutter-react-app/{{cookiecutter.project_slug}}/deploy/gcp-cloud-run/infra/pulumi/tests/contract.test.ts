// The contract between the stack, the scripts around it, and the pipelines.
//
// The failure this file exists to prevent: a stack exporting `workload_identity_provider` and a
// pipeline reading `vars.WIF_PROVIDER`. Nothing errors. The deploy job is simply skipped forever,
// because it is gated on a variable nothing ever sets.
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { BOOTSTRAP } from "../src/components/apis";
import { KEY, KEYRING } from "../src/components/identities";
import { EXPORTS } from "./mocks";

const INFRA = join(__dirname, "..", "..");
const WORKFLOWS = join(INFRA, "..", ".github", "workflows");

/** Repository variables a human sets by hand, because they come from outside this stack. */
const MANUAL_VARIABLES = [
  // The backend's address. It belongs to another repository's stack, so nothing here can export it.
  "API_URL",
];

/** Exports no pipeline reads. Listed, so a new export that should have been synced fails a test. */
const NOT_SYNCED: string[] = [];

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
});

describe("the pipelines", () => {
  it("stay inert until the stack has been applied and synced", () => {
    expect(workflow("cd.yml")).toContain("if: vars.DEPLOY_SA != ''");
    const infra = workflow("infra.yml");
    expect(infra).toContain("vars.PREVIEW_SA != ''");
    // A pull request impersonates the read-only preview account, never the one that can deploy.
    expect(infra).not.toContain("DEPLOY_SA");
  });

  it("deploy from the protected production environment", () => {
    expect(workflow("cd.yml")).toContain("environment: production");
  });

  it("refuse to build a bundle with no API origin compiled into it", () => {
    const cd = code(workflow("cd.yml"));
    const refusal = cd.indexOf('if [ -z "$API_URL" ]');
    expect(refusal).toBeGreaterThan(-1);
    expect(refusal).toBeLessThan(cd.indexOf("docker/build-push-action"));
    // Escaped braces: this file is rendered by cookiecutter, and two unescaped ones are Jinja.
    expect(cd).toMatch(/build-args: VITE_API_URL=\$\{\{ steps\.origin\.outputs\.url \}\}/);
  });

  it("tag the image with the commit, never :latest", () => {
    const cd = code(workflow("cd.yml"));
    expect(cd).toContain("github.sha");
    expect(cd).not.toContain(":latest");
  });

  it("update the image and nothing else about the service", () => {
    const cd = code(workflow("cd.yml"));
    expect(cd).toContain("gcloud run services update");
    expect(cd).not.toContain("gcloud run deploy");
    expect(cd).not.toContain("pulumi up");
  });

  it("smoke-test the index and a route only the client knows", () => {
    const cd = code(workflow("cd.yml"));
    expect(cd).toContain('"$SITE_URL/"');
    expect(cd).toContain('"$SITE_URL/some/deep/route"');
  });

  it.each(["ci.yml", "cd.yml", "infra.yml"])("%s is filtered to what it builds", (name) => {
    // Without a filter, a typo fix in a README rebuilds and redeploys the site.
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
  it("enables in the bootstrap what the stack cannot enable for itself", () => {
    const source = read(INFRA, "bootstrap", "state-bucket.sh");
    for (const api of BOOTSTRAP) expect(source).toContain(api);
  });

  it.each(["state-bucket.sh", "teardown.sh"])("%s names what the stack and the backend templates use", (script) => {
    // The same names as the backend templates' bootstrap, so a site and a service in one project
    // share one backend and one key instead of creating a second of each.
    const source = read(INFRA, "bootstrap", script);
    expect(source).toContain('BUCKET="gs://${PROJECT}-pulumi-state"');
    expect(source).toContain(`KEYRING="${KEYRING}"`);
    expect(source).toContain(`KEY="${KEY}"`);
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
    const deletion = source.indexOf("gcloud storage rm");
    expect(listing).toBeGreaterThan(-1);
    expect(listing).toBeLessThan(deletion);
    expect(source).toContain("PULUMI_PROJECT");
  });
});

describe("the recipes", () => {
  const justfile = read(INFRA, "justfile");
  const line = (needle: string): string =>
    justfile.split("\n").find((l) => l.includes(needle) && !l.trim().startsWith("#")) ?? "";

  it.each(["pulumi preview", "pulumi up", "pulumi destroy"])("%s refreshes", (command) => {
    // Load-bearing: CD changes the image out of band, and without a refresh the next apply rolls
    // the service back to the placeholder.
    expect(line(command)).toContain("--refresh");
  });

  it("never approve an apply for you", () => {
    expect(line("--yes")).toBe("");
  });
});
