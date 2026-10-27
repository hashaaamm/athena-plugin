# cookiecutter-react-app

> **This is a published copy.** It is generated from Athena's source and rewritten on every publish, so an edit made here is lost at the next one. Report a problem as an issue on the repository you found it in.

A **React + TypeScript single-page application in a repository of its own**, with the Pulumi stack
that serves it — from Cloud Run on GCP, or from a private S3 bucket behind CloudFront on AWS, chosen
at generation time by `deploy_target`. Every frontend is its own repository, with its own stack and
its own CD (ADR-0022): the backend it calls is generated separately, from `cookiecutter-service`
(FastAPI) or `cookiecutter-gin-service` (Gin), which answer the same HTTP contract.

It generates a repository whose application already satisfies the handbook's frontend rules — a
typed API client generated from the backend's OpenAPI document, one HTTP entry point, every request
state rendered, tokens for colour — with CI, a CD pipeline that is inert until configured, and a
stack that unit-tests with no cloud account.

**With `include_auth=yes` it signs people in**: sign-in, registration and an account page against
the backend's six auth endpoints, the refresh token in an httpOnly cookie, the access token in
memory, a 401 handled once in client middleware. **With `include_auth=no` it is the dashboard and
nothing else**, for a backend with no database and so no users.

## Use it

```bash
uvx cookiecutter gh:hashaaamm/athena-plugin --directory templates/cookiecutter-react-app   # interactive
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-react-app \
  project_name="Billing Web" api_url=http://localhost:8000

# The same app, served from S3 and CloudFront instead
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-react-app \
  project_name="Billing Web" deploy_target=aws-s3-cloudfront aws_account_id=123456789012 aws_region=eu-west-1
```

Then, in the generated repository:

```bash
just lock                  # resolves pnpm-lock.yaml in a container; commit it
(cd infra && just lock)    # the stack's own lockfile, on the host; commit it too
just dev                   # http://localhost:3000, calling api_url
just check                 # everything CI runs: lint, types, tests, the production bundle
just infra-test            # the stack's tests; no cloud credentials needed
```

Neither lockfile is templated. A committed one would be stale on the day it was written and would
pin whatever versions existed when this template was last touched; every install runs with
`--frozen-lockfile`, so a stale lockfile fails loudly rather than resolving something new.

## Variables

| Variable | Default | What it does |
| --- | --- | --- |
| `project_name` | `Example App` | Human name. The page title, the shell's heading, the READMEs. |
| `project_slug` | derived | Directory name, package name, the Pulumi project, and the start of every cloud resource's name. **Give it a different slug from the backend's**: on one cloud account or project the two share a state bucket, and an identical Pulumi project name is one state file — `infra/README.md` says what happens and how to recover. |
| `description` | … | One line. `index.html`'s meta description and the READMEs. |
| `author` | `Your Name` | Attribution. |
| `deploy_target` | `gcp-cloud-run` | Where the app is served, and so which `infra/` and which deploy workflows the repository gets. `gcp-cloud-run` builds the nginx image in `docker/` and runs it on Cloud Run, which scales to zero; you get an `https://….run.app` URL. `aws-s3-cloudfront` uploads the bundle to a private S3 bucket that only CloudFront can read, through Origin Access Control; you get an `https://d….cloudfront.net` URL, and the repository has no `docker/` at all. Both have the same `just infra-*` recipes and the same order of operations in `infra/README.md`. The unchosen target is deleted at generation. |
| `gcp_project_id` | `your-gcp-project` | `gcp-cloud-run` only. The project the stack targets, and the state bucket `gs://<project>-pulumi-state` — the backend templates' bucket, shared when the two share a project. |
| `gcp_region` | `europe-west1` | `gcp-cloud-run` only. Cloud Run and Artifact Registry live here. |
| `aws_account_id` | `123456789012` | `aws-s3-cloudfront` only, and refused before generation unless it is 12 digits. The provider's `allowedAccountIds`, the state bucket `pulumi-state-<account>-<region>` — again the backend templates' — and the site bucket's name. |
| `aws_region` | `eu-west-1` | `aws-s3-cloudfront` only. The site bucket lives here; CloudFront is global. |
| `github_repository` | `your-org/<slug>` | `owner/repo`. The Workload Identity provider's attribute condition on GCP; the base of the CI roles' `sub` condition on AWS, which `githubSubjectPrefix` overrides when GitHub issues the immutable form. Wrong here means a deploy that cannot authenticate. |
| `include_auth` | `yes` | `yes` ships the sign-in, registration and account screens and the session machinery, and needs a backend generated with a database, running with `REFRESH_TRANSPORT=cookie`. `no` removes those files, the route guard and the auth middleware; the dashboard is the only page. |
| `api_url` | `http://localhost:8000` | The backend the app calls in local development: `VITE_API_URL` in `.env.example`, the client's fallback, and where `just gen-api` reads `/openapi.json`. Refused unless it is an http(s) origin with no trailing slash. Deployed, the address comes from the `API_URL` repository variable instead, set by a human. |

## What is in the generated repository

The application sits at the root — `package.json`, `src/`, `index.html`, and `docker/` on GCP —
because it is the repository's one component. Beside it:

| Path | What it is |
| --- | --- |
| `justfile` | The entry point. The application's recipes run in the compose `app` container; `infra-*` delegate to `infra/justfile`, which runs on the host. |
| `docker-compose.yml` | The Vite dev server in `node:24-slim`, the source bind-mounted and `node_modules` in a named volume. The backend is not in it: it is another repository, run beside this one. |
| `.github/workflows/ci.yml` | Lint, types, tests, the production bundle, `pnpm audit --audit-level high` and gitleaks. The same for both targets. |
| `.github/workflows/cd.yml` | Per target. Refuses to build when `API_URL` is empty, then builds with it as `VITE_API_URL`. GCP: builds and pushes the image, `gcloud run services update` (image only), smoke-tests `/` and a deep route. AWS: `pnpm build`, two `aws s3 sync`s — hashed assets `immutable`, the index `no-cache` — an invalidation of the index polled to completion against a deadline, then the same smoke test. |
| `.github/workflows/infra.yml` | The stack's type check, lint and tests on every pull request, and a `pulumi preview --refresh` once the preview identity exists. |
| `infra/` | Pulumi in TypeScript (ADR-0020), its bootstrap and teardown scripts, `sync-github.sh`, and a README that is the order of operations. |
| `AGENTS.md`, `README.md` | The rules an agent needs while editing the app, and what a human needs before the first deploy. |
| `.gitleaks.toml`, `.pre-commit-config.yaml`, `.env.example`, `.editorconfig` | As in the service templates. |

## What the generated `infra/` gives you

**`gcp-cloud-run`**: the APIs, an Artifact Registry repository with a cleanup policy, a Cloud Run
service on the public `hello` image until CD replaces it — nginx on 8080, probed at `/`, 0 to 2
instances, `ignoreChanges` on the image — a runtime account that can write logs and nothing else, a
deploy account that can push to this repository, update this service and act as the runtime account
and nothing else, a preview account that can read, and a Workload Identity pool pinned to the
repository. State in the backend templates' GCS bucket and KMS key.

**`aws-s3-cloudfront`**: a private bucket — all four public-access blocks, `BucketOwnerEnforced`,
SSE-S3, not versioned, `forceDestroy` outside `prod` — an Origin Access Control that always signs
with SigV4, a bucket policy admitting `cloudfront.amazonaws.com` only for this distribution, and the
distribution: HTTPS redirect, `index.html` as the root object and as the 200 answer to any 403 or
404 (the SPA fallback), the managed `CachingOptimized` policy, HSTS, `nosniff`, `DENY` framing and a
strict referrer policy, `PriceClass_100`, the default certificate, HTTP/3 and IPv6. Two keyless
GitHub roles: deploy, trusting `<prefix>:environment:production`, may write the bucket's objects and
invalidate this distribution; preview, trusting `<prefix>:pull_request`, has `ReadOnlyAccess` and
this project's state — the backend templates' preview grants, line for line. The bootstrap creates
the same account-level bucket, key and OIDC provider the backend templates do, under the same
names, so a site and a service in one account share them.

Both stacks are unit-tested with vitest and `pulumi.runtime.setMocks`: the exports, an explicit
provider on every resource and every lookup, the trust subjects with and without a configured
prefix, who may read and write what, and a contract test that the stack's exports, the variables
`sync-github.sh` sets and the `vars.` the workflows read all agree.

## The cookiecutter traps this template handles explicitly

GitHub Actions' `${{ ... }}` and Jinja's `{{ ... }}` are the same two braces, so
`_copy_without_render` covers `.github/workflows/*` and `deploy/*/workflows/*`, and no workflow may
contain a cookiecutter variable — a contract test fails if one does. `just`'s interpolation is also
`{{ }}`: the justfiles are rendered, so their bodies sit inside `{% raw %}` blocks. And JSX: an inline
object prop opens with the same two braces, so every one in `src/` is hoisted to a `const`. The
stack's tests are rendered too, which is why the one that reads a workflow expression spells its
braces escaped.

## Related

- [templates/README.md](../README.md) · [cookiecutter-service/](../cookiecutter-service) · [cookiecutter-gin-service/](../cookiecutter-gin-service)
- The rules a generated repository is built to satisfy are retrieved, not linked: ask Athena for the
  **React architecture**, **data fetching**, **authentication in the browser**, **GitHub Actions
  standards** and **Pulumi standards**. Nothing in a generated repository points at a file path in
  this one, because a generated repository does not have this one.
