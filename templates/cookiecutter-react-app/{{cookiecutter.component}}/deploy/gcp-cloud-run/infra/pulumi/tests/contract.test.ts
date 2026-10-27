// The contract between the stack, the scripts around it, and the pipelines.
//
// The failure this file exists to prevent: a stack exporting `workload_identity_provider` and a
// pipeline reading `vars.WEB_WIF_PROVIDER`. Nothing errors. The deploy job is simply skipped
// forever, because it is gated on a variable nothing ever sets.
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { describe, expect, it } from "vitest";

import { BOOTSTRAP } from "../src/components/apis";
import { APPLY_ENVIRONMENT, KEY, KEYRING } from "../src/components/identities";
import { API_UPSTREAM_ENV } from "../src/components/service";
import { EXPORTS } from "./mocks";

/** This component's folder name, and the prefix of its workflows and repository variables. */
const COMPONENT = "{{ cookiecutter.component }}";
const PREFIX = "{{ cookiecutter.__component_var }}_";

const INFRA = join(__dirname, "..", "..");
const APP = join(INFRA, "..");

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
  // The backend's own address, read by the first deploy to check the stack was applied with the
  // configured apiUrl. The bundle is built with api_url, which is synced.
  "api_upstream",
];

function read(...path: string[]): string {
  return readFileSync(join(...path), "utf8");
}

const syncScript = (): string => read(INFRA, "scripts", "sync-github.sh");
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
    // Two components in one repository each sync a deploy account; unprefixed, the second
    // overwrites the first.
    for (const variable of syncMapping().values()) expect(variable.startsWith(PREFIX), variable).toBe(true);
  });

  it.each(NAMES)("sets every variable %s reads", (name) => {
    const provided = [...syncMapping().values()];
    expect(referenced(workflow(name)).filter((v) => !provided.includes(v))).toEqual([]);
  });

  it("copies the API's address from the stack, so nobody sets it by hand", () => {
    expect(syncMapping().get("api_url")).toBe(`${PREFIX}API_URL`);
  });

  it("refuses to un-gate CD before the first deploy has created the service", () => {
    const script = syncScript();
    expect(script).toContain("just infra-first-deploy");
    expect(script.indexOf("just infra-first-deploy")).toBeLessThan(script.indexOf('gh variable set "${variable}"'));
  });

  it("sets no GitHub secret: every value is a public identifier", () => {
    expect(syncScript()).not.toContain("gh secret set");
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
    // Without a filter, a typo fix in another component rebuilds and redeploys this one.
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
    expect(workflow("cd.yml")).toContain(`if: vars.${PREFIX}DEPLOY_SA != ''`);
    const infra = workflow("infra.yml");
    expect(infra).toContain(`vars.${PREFIX}PREVIEW_SA != ''`);
    expect(infra).toContain(`vars.${PREFIX}APPLY_SA != ''`);
    // A pull request impersonates the read-only preview account, never the one that can deploy.
    expect(infra).not.toContain("DEPLOY_SA");
  });

  it("apply every merge to main as the apply account, in the environment it trusts, never cancelled", () => {
    // Pulumi standards MUST-9: after the environment is set up from a machine, every apply runs
    // from CI. It has to refresh, or it rolls the site back to the image the state last saw.
    const apply = jobsOf(workflow("infra.yml")).get("apply") ?? "";
    for (const want of [
      "github.event_name == 'push' && github.ref == 'refs/heads/main'",
      `environment: ${APPLY_ENVIRONMENT}`,
      "service_account:",
      `vars.${PREFIX}APPLY_SA`,
      "command: up",
      "stack-name: dev",
      "refresh: true",
      `group: ${COMPONENT}-infra-apply`,
      "cancel-in-progress: false",
      "needs: [static]",
    ]) {
      expect(apply, want).toContain(want);
    }
    expect(apply).not.toMatch(/PREVIEW_SA|DEPLOY_SA/);
  });

  it("run only the preview on a pull request, and never cancel a push", () => {
    // The preview is the review; the apply account is never in reach of a pull request. Cancelling
    // a push could stop an apply halfway through, leaving the stack locked.
    const infra = workflow("infra.yml");
    const preview = jobsOf(infra).get("preview") ?? "";
    expect(preview).toContain("github.event_name == 'pull_request'");
    expect(preview).toContain("command: preview");
    expect(preview).not.toMatch(/APPLY_SA|command: up/);
    expect(infra).not.toContain("cancel-in-progress: true");
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

  it("build the component's own folder with its own Dockerfile", () => {
    const cd = code(workflow("cd.yml"));
    expect(cd).toContain(`context: ${COMPONENT}\n`);
    expect(cd).toContain(`file: ${COMPONENT}/docker/Dockerfile.prod`);
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

describe("the first deploy", () => {
  it("builds the image CD builds, for the platform Cloud Run runs", () => {
    const script = code(firstDeployScript());
    expect(script).toContain("docker buildx build");
    expect(script).toContain("--platform linux/amd64");
    expect(script).toContain('-f "${APP_DIR}/docker/Dockerfile.prod"');
    expect(script).toContain("--target production");
    expect(script).toContain('--build-arg "VITE_API_URL=${API_URL}"');
  });

  it("refuses without an applied stack, an API address, or a committed application", () => {
    const script = code(firstDeployScript());
    expect(script).toContain("read_output image_repo");
    expect(script).toContain("config get apiUrl");
    expect(script).toContain("status --porcelain");
  });

  it("builds with the applied stack's api_url, and refuses an apiUrl not yet applied", () => {
    const script = code(firstDeployScript());
    expect(script).toContain("read_output api_url");
    expect(script).toContain("read_output api_upstream");
    expect(script).toContain('"${UPSTREAM}" != "${CONFIGURED}"');
    expect(script.indexOf("read_output api_upstream")).toBeLessThan(script.indexOf("docker buildx build"));
  });

  it("smoke-tests the API through the site as JSON when the bundle calls it there", () => {
    for (const text of [code(firstDeployScript()), code(workflow("cd.yml"))]) {
      expect(text).toMatch(/"\$\{?SITE_URL\}?\/health\/live"/);
      expect(text).toContain('"200 application/json"');
    }
    // CD smoke-tests with the value it built with.
    expect(code(workflow("cd.yml")).match(/steps\.origin\.outputs\.url/g)).toHaveLength(2);
  });

  it("creates the service by setting serviceImage and applying with a refresh, your flags passed through", () => {
    const script = code(firstDeployScript());
    const set = script.indexOf("config set --plaintext serviceImage");
    const up = script.indexOf("up --stack");
    expect(set).toBeGreaterThan(script.indexOf("--push"));
    expect(up).toBeGreaterThan(set);
    expect(script.slice(up).split("\n")[0]).toContain("--refresh");
    // pulumi asks for confirmation; nothing here answers it.
    expect(script).not.toContain("--yes");
  });

  it("builds its stage on the build machine's own platform", () => {
    const dockerfile = read(APP, "docker", "Dockerfile.prod");
    expect(dockerfile).toMatch(/^FROM --platform=\$BUILDPLATFORM node:\S+ AS build$/m);
  });
});

describe("the image's nginx", () => {
  // The stack sets API_UPSTREAM_HOST on the service; the image's template is what reads it. These
  // tests run without Docker, so they hold the template to its shape; the smoke tests after every
  // deploy are what prove the proxy answers.
  const template = (): string => code(read(APP, "docker", "default.conf.template"));
  const dockerfile = (): string => read(APP, "docker", "Dockerfile.prod");
  /** One location block's body, from its opening line to the first line closing it at its depth. */
  const location = (opening: string): string => {
    const text = template();
    const start = text.indexOf(opening);
    expect(start, opening).toBeGreaterThan(-1);
    return text.slice(start, text.indexOf("\n    }\n", start));
  };

  it("reads the variable the stack sets, with an image default so it always renders", () => {
    expect(template()).toContain('set $api_host "${' + API_UPSTREAM_ENV + '}";');
    expect(dockerfile()).toContain(`ENV ${API_UPSTREAM_ENV}=""`);
    // The entrypoint exports the container's resolvers only when this is set.
    expect(dockerfile()).toContain("ENV NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1");
    expect(template()).toContain("resolver ${NGINX_LOCAL_RESOLVERS}");
  });

  it("proxies the API's two prefixes to its own host over verified TLS, its name as Host and SNI", () => {
    const api = location("location ~ ^/(api|health)/ {");
    for (const want of [
      'if ($api_host = "") {',
      "return 404;",
      "proxy_pass https://$api_host;",
      "proxy_set_header Host $api_host;",
      "proxy_ssl_server_name on;",
      "proxy_ssl_name $api_host;",
      "proxy_ssl_verify on;",
      "proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;",
      "proxy_connect_timeout 10s;",
      "proxy_read_timeout 60s;",
      "proxy_send_timeout 60s;",
      "client_max_body_size 32m;",
    ]) {
      expect(api, want).toContain(want);
    }
    // No URI part on proxy_pass: the request's own path reaches the API unchanged.
    expect(api).not.toMatch(/proxy_pass https:\/\/\$api_host\//);
  });

  it("keeps the SPA fallback for every other path", () => {
    expect(location("location / {")).toContain("try_files $uri $uri/ /index.html;");
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
    // the service back to the first deploy's image.
    expect(line(command)).toContain("--refresh");
  });

  it("never approve an apply for you", () => {
    expect(line("--yes")).toBe("");
  });

  it("run the first deploy from the script, passing your flags through", () => {
    expect(line("first-deploy.sh")).toContain("./scripts/first-deploy.sh {{ '{{' }}STACK}} {{ '{{' }}FLAGS}}");
  });
});
