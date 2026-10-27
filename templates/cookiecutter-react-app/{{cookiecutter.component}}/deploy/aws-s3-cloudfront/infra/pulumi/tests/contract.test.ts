// The contract between the stack, the scripts around it, and the pipelines.
//
// The failure this file exists to prevent: a stack exporting `bucket` and a pipeline reading
// `vars.WEB_SITE_BUCKET`. Nothing errors. The deploy job is simply skipped forever, or uploads to
// nowhere, because it is reading a variable nothing ever sets.
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

import {
  APPLY_ENVIRONMENT,
  DEPLOY_ENVIRONMENT,
  GITHUB_AUDIENCE,
  GITHUB_OIDC_HOST,
  STATE_KEY_ALIAS,
} from "../src/policy";
import { EXPORTS } from "./mocks";

/** This component's folder name, and the prefix of its workflows and repository variables. */
const COMPONENT = "{{ cookiecutter.component }}";
const PREFIX = "{{ cookiecutter.__component_var }}_";

const INFRA = join(__dirname, "..", "..");

/**
 * The repository root: the nearest directory above this one that holds `.github`. The component is
 * one folder of that repository, and its workflows live at the root, where GitHub reads them.
 */
function repositoryRoot(): string {
  let dir = INFRA;
  while (!existsSync(join(dir, ".github"))) {
    const parent = dirname(dir);
    if (parent === dir) throw new Error(`no .github directory above ${INFRA}`);
    dir = parent;
  }
  return dir;
}

const WORKFLOWS = join(repositoryRoot(), ".github", "workflows");
const NAMES = ["ci.yml", "cd.yml", "infra.yml"];

/** Exports no pipeline reads. Listed, so a new export that should have been synced fails a test. */
const NOT_SYNCED = [
  // Read by sync-github.sh to check the trust policies against GitHub, never copied into a variable.
  "github_subject_prefix",
  // The backend's own address, read by the first deploy to check the stack was applied with the
  // configured apiUrl. The bundle is built with api_url, which is synced.
  "api_upstream",
];

function read(...path: string[]): string {
  return readFileSync(join(...path), "utf8");
}

const syncScript = (): string => read(INFRA, "scripts", "sync-github.sh");
const publishScript = (): string => read(INFRA, "scripts", "publish.sh");
const firstDeployScript = (): string => read(INFRA, "scripts", "first-deploy.sh");
/** One of this component's workflows, by its name in the template: `cd.yml` is `<component>-cd.yml`. */
const workflow = (name: string): string => read(WORKFLOWS, `${COMPONENT}-${name}`);

/** output -> variable, for every row in sync-github.sh. */
function syncMapping(): Map<string, string> {
  const rows = [...syncScript().matchAll(/"([a-z_]+):([A-Z0-9_]+)"/g)];
  expect(rows.length).toBeGreaterThan(0);
  return new Map(rows.map((row) => [row[1]!, row[2]!]));
}

function referenced(text: string): string[] {
  return [...new Set([...text.matchAll(/(?:vars|secrets)\.([A-Za-z0-9_]+)/g)].map((m) => m[1]!))];
}

/** Each job's text under `jobs:`, by its key: from its two-space key to the next one. */
function jobsOf(text: string): Map<string, string> {
  const parts = (text.split("\njobs:\n")[1] ?? "").split(/^ {2}([a-z][a-z0-9_-]*):\n/m);
  const jobs = new Map<string, string>();
  for (let i = 1; i + 1 < parts.length; i += 2) jobs.set(parts[i]!, parts[i + 1]!);
  return jobs;
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

  it("prefixes every variable it writes with the component's name", () => {
    // Two components in one repository each sync a deploy role; unprefixed, the second overwrites
    // the first.
    for (const variable of syncMapping().values()) expect(variable.startsWith(PREFIX), variable).toBe(true);
  });

  it.each(NAMES)("sets every variable %s reads", (name) => {
    const provided = [...syncMapping().values()];
    expect(referenced(workflow(name)).filter((v) => !provided.includes(v))).toEqual([]);
  });

  it("copies the API's address from the stack, so nobody sets it by hand", () => {
    expect(syncMapping().get("api_url")).toBe(`${PREFIX}API_URL`);
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
  it.each(NAMES)("%s is installed at the repository root under the component's name, with no token left", (name) => {
    const text = workflow(name);
    // The post-generation hook replaces both tokens; one that survived is a path or a variable
    // name that matches nothing.
    expect(text).not.toContain("__COMPONENT");
    expect(text).toContain(`.github/workflows/${COMPONENT}-${name}`);
  });

  it.each(NAMES)("%s is filtered to this component, runs in its folder, and is named for it", (name) => {
    // Without a filter, a typo fix in another component rebuilds and republishes this one.
    const text = workflow(name);
    // The infra workflow watches only the stack and the workflows it reads: an application-only
    // merge must not start an apply that waits for the infrastructure environment's reviewers.
    expect(text).toContain(name === "infra.yml" ? `- '${COMPONENT}/infra/**'` : `- '${COMPONENT}/**'`);
    expect(text).toMatch(new RegExp(`^name: ${COMPONENT} `, "m"));
    expect(text).toMatch(new RegExp(`^  group: ${COMPONENT}-`, "m"));
    expect(text).toMatch(new RegExp(`^    working-directory: ${COMPONENT}(/|$)`, "m"));
  });

  it.each(NAMES)("%s checks the repository out before any other step of any job", (name) => {
    // `defaults.run.working-directory` is the component's folder, which does not exist until then.
    const jobs = code(workflow(name)).split(/^ {2}[a-z-]+:\n {4}name:/m).slice(1);
    expect(jobs.length).toBeGreaterThan(0);
    for (const job of jobs) {
      const first = job.split(/^ {6}- /m)[1] ?? "";
      expect(first.trim()).toMatch(/^uses: actions\/checkout@/);
    }
  });

  it("stay inert until the stack has been applied and synced", () => {
    expect(workflow("cd.yml")).toContain(`if: vars.${PREFIX}AWS_DEPLOY_ROLE_ARN != ''`);
    const infra = workflow("infra.yml");
    expect(infra).toContain(`vars.${PREFIX}AWS_PREVIEW_ROLE_ARN != ''`);
    expect(infra).toContain(`vars.${PREFIX}AWS_APPLY_ROLE_ARN != ''`);
    // A pull request gets the read-only preview role, never the one that can publish.
    expect(infra).not.toContain("AWS_DEPLOY_ROLE_ARN");
  });

  it("apply every merge to main as the apply role, in the environment it trusts, never cancelled", () => {
    // Pulumi standards MUST-9: after the environment is set up from a machine, every apply runs
    // from CI. It has to refresh, or it reverts what the pipeline last changed out of band.
    const apply = jobsOf(workflow("infra.yml")).get("apply") ?? "";
    for (const want of [
      "github.event_name == 'push' && github.ref == 'refs/heads/main'",
      `environment: ${APPLY_ENVIRONMENT}`,
      `vars.${PREFIX}AWS_APPLY_ROLE_ARN`,
      "command: up",
      "stack-name: dev",
      "refresh: true",
      `group: ${COMPONENT}-infra-apply`,
      "cancel-in-progress: false",
      "needs: [static]",
    ]) {
      expect(apply, want).toContain(want);
    }
    expect(apply).not.toMatch(/PREVIEW_ROLE|DEPLOY_ROLE/);
    expect(APPLY_ENVIRONMENT).not.toBe(DEPLOY_ENVIRONMENT);
  });

  it("run only the preview on a pull request, and never cancel a push", () => {
    // The preview is the review; the apply role is never in reach of a pull request. Cancelling a
    // push could stop an apply halfway through, leaving the stack locked.
    const infra = workflow("infra.yml");
    const preview = jobsOf(infra).get("preview") ?? "";
    expect(preview).toContain("github.event_name == 'pull_request'");
    expect(preview).toContain("command: preview");
    expect(preview).not.toMatch(/APPLY_ROLE|command: up/);
    expect(infra).not.toContain("cancel-in-progress: true");
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

  it("publish with the script the first deploy runs, after the build and with the deploy role", () => {
    const cd = code(workflow("cd.yml"));
    const publish = cd.indexOf("run: ./infra/scripts/publish.sh dist");
    expect(publish).toBeGreaterThan(cd.indexOf("run: pnpm build"));
    expect(publish).toBeGreaterThan(cd.indexOf("aws-actions/configure-aws-credentials"));
    // The sequence lives in one place: nothing in the workflow talks to S3 or CloudFront itself.
    expect(cd).not.toContain("aws s3");
    expect(cd).not.toContain("aws cloudfront");
    expect(code(firstDeployScript())).toContain('"${HERE}/publish.sh" "${APP_DIR}/dist"');
  });

  it("scan for secrets with the gitleaks CLI over the component's whole history, with its own config", () => {
    const ci = code(workflow("ci.yml"));
    expect(ci).toContain("fetch-depth: 0");
    expect(ci).toContain("ghcr.io/gitleaks/gitleaks:v");
    expect(ci).toContain(`git --log-opts="-- ${COMPONENT}" --config ${COMPONENT}/.gitleaks.toml`);
    expect(ci).not.toContain("gitleaks-action");
  });

  it.each(NAMES)("%s carries no cookiecutter variable", (name) => {
    // Workflows are _copy_without_render, because an Actions expression and a Jinja one are written
    // with the same two braces. A cookiecutter variable here would survive as literal text. The
    // patterns put the second character in a class so this file — which IS rendered — holds no
    // Jinja delimiter of its own.
    expect(workflow(name)).not.toMatch(/\{[{]\s*cookiecutter|\{[%#]/);
  });
});

describe("the publish script", () => {
  it("caches hashed assets forever and the index never, uploading assets first", () => {
    const script = code(publishScript());
    const assets = script.indexOf('--cache-control "public,max-age=31536000,immutable"');
    const index = script.indexOf('--exclude "assets/*" --cache-control "no-cache" --delete');
    expect(assets).toBeGreaterThan(-1);
    expect(index).toBeGreaterThan(assets);
  });

  it("invalidates the index and waits for it with a deadline, not a fixed-cap waiter", () => {
    const script = code(publishScript());
    expect(script).toContain('--paths "/index.html" "/"');
    expect(script).toContain("aws cloudfront get-invalidation");
    expect(script).toContain("DEADLINE=");
    expect(script).not.toContain("wait invalidation-completed");
  });

  it("prunes the previous deploy's assets only after the invalidation completed", () => {
    const script = code(publishScript());
    const prune = script.lastIndexOf("--delete --only-show-errors");
    expect(prune).toBeGreaterThan(script.indexOf('"Completed"'));
  });

  it("smoke-tests this build's index and a route only the client knows", () => {
    const script = code(publishScript());
    expect(script).toContain('"${SITE_URL}/"');
    expect(script).toContain('"${SITE_URL}/some/deep/route"');
    expect(script).toContain('grep -o \'assets/[^"]*\\.js\' "${DIST}/index.html"');
  });

  it("smoke-tests the API on the site's own origin as JSON, not as the app's shell, when the bundle calls it there", () => {
    const script = code(publishScript());
    const routed = script.indexOf('if [[ "${API_URL}" == "/" ]]');
    expect(routed).toBeGreaterThan(-1);
    expect(script.slice(routed)).toContain('"${SITE_URL}/health/live"');
    expect(script.slice(routed)).toContain('"200 application/json"');
  });
});

describe("the API's address", () => {
  it("is built into the bundle, published and smoke-tested as one value: the stack's api_url", () => {
    const cd = code(workflow("cd.yml"));
    // The same step output feeds the build and the publish script's smoke test.
    expect(cd.match(/: \$\{\{ steps\.origin\.outputs\.url \}\}/g)).toHaveLength(2);
    expect(code(firstDeployScript())).toContain('SITE_URL="${SITE_URL}" API_URL="${API_URL}"');
  });

  it("is read from the applied stack by the first deploy, which refuses a configuration not yet applied", () => {
    const script = code(firstDeployScript());
    expect(script).toContain("read_output api_url");
    expect(script).toContain("read_output api_upstream");
    expect(script).toContain('"${UPSTREAM}" != "${CONFIGURED}"');
    expect(script.indexOf("read_output api_upstream")).toBeLessThan(script.indexOf("pnpm --dir"));
  });

  it("tells the operator what the backend still needs, by whether the site serves it", () => {
    const script = syncScript();
    expect(script).toContain('if [[ "${API_URL}" == "/" ]]');
    expect(script).toContain("corsOrigins");
  });
});

describe("the first deploy", () => {
  it("refuses without an API address, an applied stack, or a committed application", () => {
    const script = code(firstDeployScript());
    expect(script).toContain("config get apiUrl");
    expect(script).toContain("read_output site_bucket");
    expect(script).toContain("status --porcelain");
  });

  it("builds with the same commands as CD", () => {
    const script = code(firstDeployScript());
    expect(script).toContain("install --frozen-lockfile");
    expect(script).toContain('VITE_API_URL="${API_URL}" pnpm --dir "${APP_DIR}" build');
  });

  it("asks before it publishes, and refuses when nothing can answer", () => {
    const script = code(firstDeployScript());
    expect(script).toContain("! -t 0");
    expect(script.indexOf("read -r -p")).toBeLessThan(script.indexOf("publish.sh"));
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

  it("run the first deploy from the script, passing your flags through", () => {
    expect(line("first-deploy.sh")).toContain("./scripts/first-deploy.sh {{ '{{' }}STACK}} {{ '{{' }}FLAGS}}");
  });
});
