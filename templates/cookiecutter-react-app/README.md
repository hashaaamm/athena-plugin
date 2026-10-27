# cookiecutter-react-app

> **This is a published copy.** It is generated from Athena's source and rewritten on every publish, so an edit made here is lost at the next one. Report a problem as an issue on the repository you found it in.

A **React + TypeScript single-page application, generated as one component of a repository**, with
the Pulumi stack that serves it — from Cloud Run on GCP, or from a private S3 bucket behind
CloudFront on AWS, chosen at generation time by `deploy_target`. A repository holds one folder per
component (ADR-0024): this template writes `<repo-root>/<component>/`, `web` by default, with the
app and its `infra/` together inside it. The backend it calls is another component, generated the
same way from `cookiecutter-fastapi-service` (FastAPI) or `cookiecutter-gin-service` (Gin), which
answer the same HTTP contract. A repository that holds only this app is a repository with one
component.

It generates an application that already satisfies the handbook's frontend rules — a typed API
client generated from the backend's OpenAPI document, one HTTP entry point, every request state
rendered, tokens for colour — with CI, a CD pipeline that is inert until configured, and a stack
that unit-tests with no cloud account.

**With `include_auth=yes` it signs people in**: sign-in, registration and an account page against
the backend's six auth endpoints, the refresh token in an httpOnly cookie, the access token in
memory, a 401 handled once in client middleware. **With `include_auth=no` it is the dashboard and
nothing else**, for a backend with no database and so no users.

## Use it

Run it from anywhere, with `-o` naming the repository root. It writes `<repo-root>/<component>/`.

```bash
uvx cookiecutter gh:hashaaamm/athena-plugin --directory templates/cookiecutter-react-app -o .   # interactive
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-react-app -o . \
  project_name="Billing" component=web api_url=http://localhost:8000

# The same app, served from S3 and CloudFront instead
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-react-app -o . \
  project_name="Billing" component=web deploy_target=aws-s3-cloudfront aws_account_id=123456789012 aws_region=eu-west-1
```

Generate the backend into the same root with its own `component` — `api` is its default — and give
both the same `project_name`: they then share a slug, and every name each one creates carries its
own component. Generating a component whose name is already taken is refused before anything is
written.

Then, in the component's folder:

```bash
just lock                  # resolves pnpm-lock.yaml in a container; commit it
(cd infra && just lock)    # the stack's own lockfile, on the host; commit it too
just dev                   # http://localhost:3000, calling api_url
just check                 # everything CI runs: lint, types, tests, the production bundle
just infra-test            # the stack's tests; no cloud credentials needed
```

Every recipe also runs from the repository root as `just <component> <recipe>` — `just web check`.
The component's justfile is a `just` module of the root one, which needs just 1.31 or newer.

Neither lockfile is templated. A committed one would be stale on the day it was written and would
pin whatever versions existed when this template was last touched; every install runs with
`--frozen-lockfile`, so a stale lockfile fails loudly rather than resolving something new.

## Variables

| Variable | Default | What it does |
| --- | --- | --- |
| `project_name` | `Example App` | Human name. The page title, the shell's heading, the READMEs. |
| `project_slug` | derived | The repository's name for itself. With the component it makes the base of every name below. |
| `component` | `web` | The folder this app is generated into, and the name it goes by everywhere outside it: the workflows are `.github/workflows/<component>-*.yml`, every repository variable is `<COMPONENT>_*` (upper-cased, `-` as `_`), and every cloud resource, the Pulumi project, the Compose project and the package are named `<project_slug>-<component>`. Refused unless it is a lowercase letter and up to 19 more lowercase letters, digits or hyphens, and refused when the repository already has a component of that name. |
| `description` | … | One line. `index.html`'s meta description and the READMEs. |
| `author` | `Your Name` | Attribution. |
| `deploy_target` | `gcp-cloud-run` | Where the app is served, and so which `infra/` and which deploy workflows the component gets. `gcp-cloud-run` builds the nginx image in `docker/` and runs it on Cloud Run, which scales to zero; you get an `https://….run.app` URL. `aws-s3-cloudfront` uploads the bundle to a private S3 bucket that only CloudFront can read, through Origin Access Control; you get an `https://d….cloudfront.net` URL, and the component has no `docker/` at all. Both have the same `just infra-*` recipes and the same order of operations in `infra/README.md`. The unchosen target is deleted at generation. |
| `gcp_project_id` | `your-gcp-project` | `gcp-cloud-run` only. The project the stack targets, and the state bucket `gs://<project>-pulumi-state` — the backend templates' bucket, shared when the two share a project. |
| `gcp_region` | `europe-west1` | `gcp-cloud-run` only. Cloud Run and Artifact Registry live here. |
| `aws_account_id` | `123456789012` | `aws-s3-cloudfront` only, and refused before generation unless it is 12 digits. The provider's `allowedAccountIds`, the state bucket `pulumi-state-<account>-<region>` — again the backend templates' — and the site bucket's name. |
| `aws_region` | `eu-west-1` | `aws-s3-cloudfront` only. The site bucket lives here; CloudFront is global. |
| `github_repository` | `your-org/<slug>` | `owner/repo`. The Workload Identity provider's attribute condition on GCP; the base of the CI roles' `sub` condition on AWS, which `githubSubjectPrefix` overrides when GitHub issues the immutable form. Wrong here means a deploy that cannot authenticate. |
| `include_auth` | `yes` | `yes` ships the sign-in, registration and account screens and the session machinery, and needs a backend generated with a database, running with `REFRESH_TRANSPORT=cookie`. `no` removes those files, the route guard and the auth middleware; the dashboard is the only page. |
| `api_url` | `http://localhost:8000` | The backend the app calls in local development: `VITE_API_URL` in `.env.example`, the client's fallback, and where `just gen-api` reads `/openapi.json`. Refused unless it is an http(s) origin with no trailing slash. Deployed, the backend's address is the stack's `apiUrl` instead, which the site serves on its own origin by default, so the bundle is built with `/` — the stack's `api_url`, which `just infra-sync-github` copies into `<COMPONENT>_API_URL`. |

## What generation writes

Into the component's folder, `<repo-root>/<component>/`: the application — `package.json`, `src/`,
`index.html`, and `docker/` on GCP — and beside it:

| Path | What it is |
| --- | --- |
| `justfile` | The component's entry point. The application's recipes run in the compose `app` container; `infra-*` delegate to `infra/justfile`, which runs on the host. |
| `docker-compose.yml` | The Vite dev server in `node:24-slim`, the source bind-mounted and `node_modules` in a named volume, under the Compose project `<project_slug>-<component>`. The backend is not in it: it is another component, run beside this one. |
| `infra/` | Pulumi in TypeScript (ADR-0020), its bootstrap and teardown scripts, `first-deploy.sh`, `sync-github.sh` — and on AWS `publish.sh` — and a README that is the order of operations. |
| `AGENTS.md`, `README.md` | The rules an agent needs while editing the app, and what a human needs before the first deploy. |
| `.gitleaks.toml`, `.pre-commit-config.yaml`, `.env.example`, `.editorconfig`, `.gitignore` | As in the service templates. |

And at the repository root, because that is where GitHub and `just` look for them:

| Path | What it is |
| --- | --- |
| `.github/workflows/<component>-ci.yml` | Lint, types, tests, the production bundle, `pnpm audit --audit-level high`, and the secret scan: the gitleaks CLI from its own image over every commit that touched the component, with the component's `.gitleaks.toml`. The same for both targets. |
| `.github/workflows/<component>-cd.yml` | Per target. Refuses to build when `<COMPONENT>_API_URL` is empty, then builds with it as `VITE_API_URL`. GCP: builds and pushes the image, `gcloud run services update` (image only), smoke-tests `/`, a deep route and, when the site serves the API, `/health/live` as the API's JSON. AWS: `pnpm build`, then `infra/scripts/publish.sh` — two `aws s3 sync`s, hashed assets `immutable` and the index `no-cache`, an invalidation of the index polled to completion against a deadline, the previous assets pruned, the same smoke test. |
| `.github/workflows/<component>-infra.yml` | The stack's type check, lint and tests on every pull request, a `pulumi preview --refresh` once the preview identity exists, and a `pulumi up --refresh` on every merge to `main` once the apply identity exists, in the `infrastructure` environment. |
| `justfile` | Created if absent, and given `mod <component>`, so `just <component> <recipe>` works from the root. |
| `AGENTS.md` | Created if absent, and given a line pointing at the component's own `AGENTS.md`. |

Every workflow is filtered to `<component>/**` and its own file, runs in the component's folder,
checks the repository out before anything else, and reads only `<COMPONENT>_`-prefixed variables.
The GitHub environment they deploy from is `production`, shared by every component; each
component's deploy identity trusts that environment and nothing else. The environment every
component's infrastructure apply runs in is `infrastructure`, likewise shared.

**Setup runs from a machine, every later apply from CI** (ADR-0024, Pulumi standards MUST-9). The
bootstrap, the first `infra-up` and `infra-first-deploy` run once per environment from an
operator's machine, because the identity CI applies with is created by the first apply. After
`infra-sync-github`, `<component>-infra.yml` applies every infrastructure change merged to `main`
as the stack's apply identity; the `infrastructure` environment's protection rules — required
reviewers, `main` only, set by hand — are the gate on every apply. On AWS the apply role holds
`AdministratorAccess`; on GCP the apply account holds one predefined admin role per service the
stack manages, because the GCP infrastructure standards rule out basic roles in production. Either
can change anything in its account or project, which is why one per environment is what bounds it.

## What the generated `infra/` gives you

**`gcp-cloud-run`**: the APIs, an Artifact Registry repository with a cleanup policy, a runtime
account that can write logs and nothing else, a deploy account that can push to this repository,
update this service and act as the runtime account and nothing else, a preview account that can
read, an apply account only the `infrastructure` environment may impersonate, and a Workload
Identity pool pinned to the repository — and, once `just infra-first-deploy`
has built and pushed the real image and set `serviceImage`, a Cloud Run service on it: nginx on
8080, probed at `/`, 0 to 2 instances, `ignoreChanges` on the image so CD owns it from then on, and
`API_UPSTREAM_HOST` — the host in `apiUrl` — in its environment, which the image's nginx proxies
`/api/` and `/health/` to over verified TLS, sending the API its own name as `Host` and SNI, with a
60-second read timeout and a 32 MB body limit. There is no placeholder image. State in the backend
templates' GCS bucket and KMS key.

**`aws-s3-cloudfront`**: a private bucket — all four public-access blocks, `BucketOwnerEnforced`,
SSE-S3, not versioned, `forceDestroy` outside `prod` — an Origin Access Control that always signs
with SigV4, a bucket policy admitting `cloudfront.amazonaws.com` only for this distribution, and the
distribution: HTTPS redirect, `index.html` as the root object and, through a viewer-request
CloudFront Function on the site's own behaviour, the answer to any path with no extension (the SPA
fallback), the managed `CachingOptimized` policy, HSTS, `nosniff`, `DENY` framing and a strict
referrer policy, `PriceClass_100`, the default certificate, HTTP/3 and IPv6 — and the API as a
second origin behind `/api/*` and `/health/*`: `CachingDisabled`, `AllViewerExceptHostHeader`, every
method, HTTPS only with TLS 1.2, a 60-second read timeout. Three keyless
GitHub roles: deploy, trusting `<prefix>:environment:production`, may write the bucket's objects and
invalidate this distribution; preview, trusting `<prefix>:pull_request`, has `ReadOnlyAccess` and
this project's state — the backend templates' preview grants, line for line; apply, trusting
`<prefix>:environment:infrastructure`, holds `AdministratorAccess`. The bootstrap creates
the same account-level bucket, key and OIDC provider the backend templates do, under the same
names, so a site and a service in one account share them. `just infra-first-deploy` builds the
bundle and publishes it with the same script CD runs.

**Both targets make the app and its API one site by default.** On the platforms' own hostnames —
`*.run.app`, `*.cloudfront.net`, `*.on.aws`, all on the Public Suffix List — the site and the API
are two sites, so the backend's `SameSite=Strict` refresh cookie never reaches the API and every
reload signs the user out. So each stack serves the API on the site's own origin, under `/api/` and
`/health/`, and the bundle calls `/`, which the API client resolves against the page's origin. One
setting, `apiUrl` — the backend's own address — and `apiSameOrigin`, `true` by default, which turns
the route off for a site and an API on one domain you own. The stack exports what the bundle is
built with as `api_url` — `/`, or `apiUrl` when called directly — and `sync-github.sh` copies it into
`<COMPONENT>_API_URL`: the address CD compiles in is configuration, never a variable somebody typed.
With sign-in, the backend's stack still needs `refreshTransport=cookie`, and `corsOrigins` listing
the site's `site_url`: same origin needs no CORS, but both backends' cookie routes check `Origin`
against that list, and a browser sends `Origin` on a same-origin `POST` too. Locally nothing
changes: `localhost:3000` and `localhost:8000` are one site, and `just dev` calls `api_url` directly.
The first deploy is:

```
bootstrap → stack init → githubSubjectPrefix (AWS) and apiUrl → just infra-up
          → just infra-first-deploy → just infra-sync-github → push
```

Both stacks are unit-tested with vitest and `pulumi.runtime.setMocks`: the exports, an explicit
provider on every resource and every lookup, the trust subjects with and without a configured
prefix, who may read and write what, the service before and after the first deploy, the API route
with `apiSameOrigin` on and off (the CloudFront behaviours and routing function on AWS; on GCP the
service's `API_UPSTREAM_HOST` and the shape of the nginx proxy), and a contract
test — which finds the repository root by walking up to `.github` — that the stack's exports, the
prefixed variables `sync-github.sh` sets and the `vars.` the component's workflows read all agree,
and that no token survived generation.

## The cookiecutter traps this template handles explicitly

GitHub Actions' `${{ ... }}` and Jinja's `{{ ... }}` are the same two braces, so
`_copy_without_render` covers `.github/workflows/*` and `deploy/*/workflows/*`, and no workflow may
contain a cookiecutter variable — a contract test fails if one does. The component's name reaches
the workflows as two plain tokens instead, `__COMPONENT__` and `__COMPONENT_VAR__`, which the
post-generation hook replaces as it writes each workflow to the repository root; a contract test
fails if either survives. The derived names are cookiecutter variables with **two** leading
underscores, `__component_var` and `__name`: cookiecutter 2 renders those and not single-underscore
ones, which it passes through as raw text. `just`'s interpolation is also `{{ }}`: the justfiles are
rendered, so their bodies sit inside `{% raw %}` blocks. And JSX: an inline object prop opens with
the same two braces, so every one in `src/` is hoisted to a `const`. The stack's tests are rendered
too, which is why the ones that read a workflow expression or a recipe spell their braces escaped.

## Related

- [templates/README.md](../README.md) · [cookiecutter-fastapi-service/](../cookiecutter-fastapi-service) · [cookiecutter-gin-service/](../cookiecutter-gin-service)
- The rules a generated component is built to satisfy are retrieved, not linked: ask Athena for the
  **React architecture**, **data fetching**, **authentication in the browser**, **GitHub Actions
  standards** and **Pulumi standards**. Nothing in a generated repository points at a file path in
  this one, because a generated repository does not have this one.
