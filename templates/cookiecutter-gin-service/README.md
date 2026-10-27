# cookiecutter-gin-service

> **This is a published copy.** It is generated from Athena's source and rewritten on every publish, so an edit made here is lost at the next one. Report a problem as an issue on the repository you found it in.

A production-ready **Gin service in Go, generated as one component of a repository**, with the
Pulumi stack that deploys it — to Cloud Run on GCP, or to ECS Express Mode on AWS, chosen at
generation time by `deploy_target`. It generates a component whose backend already satisfies the
handbook's Gin standards — layering enforced by the linter, typed configuration, pgx with
sqlc-generated queries, Goose migrations embedded in the binary, a distroless image, Cloud Run
health endpoints, CI, and a CD pipeline that is inert until configured.

**It is backend only, and it answers the same HTTP contract as the FastAPI service template.** Same
routes, status codes, field names, error envelope, refresh cookie and OpenAPI schema names — so a
browser client generated from `cookiecutter-react-app` works against either backend. One HTTP suite,
run against both templates in CI, is what keeps that true.

**With a database, it ships the two health probes and six authentication endpoints** — register,
login, refresh, logout, me, change password. They are the worked example of the layering and a
feature every service eventually needs. Session handling is complete — rotating refresh tokens,
reuse detection, logout and revocation. Authorization is not: no roles, no permission guards, no
ownership checks. **With `use_postgres=no` the endpoints are the two health probes and nothing
else**, because half an auth system is worse than none.

## Use it

Every template is a **component** (ADR-0024): it is generated into a folder of one repository,
named by `component`, and a repository with one service is a repository with one component. Run
cookiecutter with `-o` pointing at the repository root:

```bash
uvx cookiecutter gh:hashaaamm/athena-plugin --directory templates/cookiecutter-gin-service -o .   # interactive
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-gin-service -o . \
  project_name="Billing" component=api go_module="github.com/acme/billing/api"
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-gin-service -o . \
  project_name="Billing" component=api go_module="github.com/acme/billing/api" \
  deploy_target=aws-ecs-express aws_account_id=123456789012 aws_region=eu-west-1
```

That writes `api/` — the service, its `infra/`, its justfile, Compose files, `.env.example`,
`AGENTS.md` and README — and, at the repository root, the three things GitHub and `just` look for
there: `.github/workflows/api-ci.yml`, `api-cd.yml` and `api-infra.yml`; a `justfile` with a
`mod api` line (created if missing); and an `AGENTS.md` listing the components (created if
missing). A second component — `cookiecutter-react-app` as `web`, or this template again as
`api2` — goes into the same root the same way and needs no edit to either. Generating a component
whose workflows are already there is refused before anything is written.

Then, from the repository root:

```bash
cp api/.env.example api/.env
(cd api/backend && just lock)   # resolve go.sum, in a container
just api dev                    # http://localhost:8000/docs
just api check                  # everything CI runs; `cd api && just check` is the same recipe

(cd api/infra && just lock)     # the Pulumi program's go.sum — needs Go on your machine
just api infra-test             # the component tests; no cloud credentials needed
```

`just api <recipe>` is a `just` module, which needs `just` 1.31 or newer.

The backend needs only Docker and `just`: every recipe runs in a container. The Pulumi program
needs Go and the `pulumi` CLI on the host, as a Pulumi Python stack needs Python.

`go.sum` is **not** templated, for either module. A committed one would pin whatever existed when
this template was last touched; resolving once at generation is the honest alternative. The images
build with `-mod=readonly`, so a stale `go.sum` fails the build loudly rather than resolving
something new. To generate into a repository that already exists, generate beside it and move the
tree in — ask Athena for merging a generated service into a repository that already has code.

## Variables

| Variable | Default | What it does |
| --- | --- | --- |
| `project_name` | `Example Service` | Human name. The OpenAPI title and the READMEs. |
| `project_slug` | derived | The repository's name. With `component`, the base of every cloud resource name; alone, the refresh cookie prefix and the database's local name. |
| `component` | `api` | The folder the component is generated into, and the prefix of everything it creates outside it or in a shared namespace: `<component>-*.yml` workflows, `<COMPONENT>_*` repository variables and secrets, and — as `<project_slug>-<component>` — the Pulumi project, the Compose project and every cloud resource name. Lowercase letters, digits and hyphens, starting with a letter, at most 20 characters. |
| `description` | … | One line. The OpenAPI description. |
| `author` | `Your Name` | Attribution. |
| `go_module` | `github.com/your-org/<slug>/<component>` | The module path both `go.mod` files declare and every import uses. Set it to the repository's real path. |
| `go_version` | `1.27` | The `go` directive, the build images and CI's toolchain. |
| `deploy_target` | `gcp-cloud-run` | Where the generated `infra/` and the deploy workflows deploy to. `gcp-cloud-run`: Cloud Run, Cloud SQL, Artifact Registry, Workload Identity Federation, state in GCS. `aws-ecs-express`: ECS Express Mode on Fargate, RDS for PostgreSQL, ECR, GitHub OIDC roles, state in S3 under AWS KMS. The generated component holds exactly one; the other is not there to switch to later. |
| `gcp_project_id` | `your-gcp-project` | `gcp-cloud-run` only. The GCP project the Pulumi stack targets. Not in any workflow. |
| `gcp_region` | `europe-west1` | `gcp-cloud-run` only. Cloud Run, Cloud SQL and Artifact Registry all live here. |
| `aws_account_id` | `123456789012` | `aws-ecs-express` only. Twelve digits, checked before anything is written. It names the state bucket, and the stack's provider refuses credentials for any other account. Not in any workflow. |
| `aws_region` | `eu-west-1` | `aws-ecs-express` only. The VPC, the Express service, RDS, ECR and the state bucket all live here. |
| `github_repository` | `your-org/<slug>` | `owner/repo`. On GCP it becomes the Workload Identity provider's attribute condition; on AWS, the `sub` the CI roles' trust policies require. Either way it is the one value stopping any other repository from assuming the CI identity. |
| `use_postgres` | `yes` | `no` drops migrations, queries, the generated data layer, the database from Compose and the stack, readiness' database check **and the whole authentication example**. |
| `use_sentry` | `yes` | `no` drops `sentry-go` and `internal/observability`. `yes` wires it with an **empty DSN by default** — local development reports nothing, by rule. |

There is no frontend variable. A browser client is its own component, generated from
`cookiecutter-react-app` into the same repository with its own Pulumi stack and pipelines
(ADR-0024). Pointing it at a generated service is stack configuration on both sides — its `apiUrl`
is this stack's `service_url`, and here: `corsOrigins` set to the client's `site_url`, and
`refreshTransport=cookie` when the service has accounts. A session that survives a reload needs
the client and the API on one site: the React template serves this API under `/api/*` on the app's
own origin by default, and a domain you own is the production setup. The
generated `infra/README.md` has the steps.

**A repository generated from plugin 0.5 with a frontend** (FastAPI `include_frontend=yes`, or a
Gin repository with `frontendEnabled` on) keeps two repository variables nothing reads any more,
`CLOUD_RUN_FRONTEND_SERVICE` and `FRONTEND_URL`, because `sync-github.sh` deletes only the variables
it still knows. Remove them by hand with `gh variable delete <NAME>` once the old frontend service
is gone.

## The brace traps this template handles explicitly

Jinja's `{{ }}` collides with three things in this repository:

1. **GitHub Actions' `${{ }}`.** `cookiecutter.json` declares `_copy_without_render:
   [".github/workflows/*", "deploy/*/workflows/*"]`, so **no workflow may contain a cookiecutter
   variable.** The post-generation hook substitutes two tokens instead — `__COMPONENT__`, the
   component's name, and `__COMPONENT_VAR__`, the same name upper-cased with `_` for `-` — as it
   writes each workflow to the repository root as `<component>-<file>`. Everything else
   project-specific comes from repository variables, prefixed `__COMPONENT_VAR___`, which is also
   why CD is inert until configured. The Pulumi stack exports every variable name the workflows
   read, the sync script copies them in, and tests in `infra/pulumi` fail if the three disagree or
   a token survives.
2. **`just` interpolation.** The justfiles are rendered, so their bodies sit in `{% raw %}` blocks
   with the cookiecutter conditionals outside them.
3. **Go composite literals.** A table-driven test written `[]T{{...}}` is a Jinja expression. No Go
   file here opens or closes two braces on one line unless it is a cookiecutter variable; break
   them across lines. The generated OpenAPI documents are JSON with Jinja around them, written by
   the maintenance script below rather than by hand.

## What the generated backend gives you

- **Layers, enforced by the linter.** `handler → service → repository → sqlc queries`. `depguard`
  in `.golangci.yml` fails `just lint` when a handler imports a repository, a service imports Gin
  or `net/http`, anything but the repository layer imports `internal/db` or pgx, or anything but
  `internal/config` reads the environment. The banned modules — `dgrijalva/jwt-go`, golang-jwt
  before v5, `lib/pq`, `golang/mock` — are denied in the same place.
- **One composition root.** `internal/app` builds the graph; nothing else constructs a service or a
  repository, so it is the one seam a test replaces.
- **Typed configuration.** `caarlos0/env` into one struct, read in `internal/config` and nowhere
  else, refusing a placeholder password outside `local` and `test`. The signing key is checked when
  `serve` starts rather than when configuration loads, because the migration job builds the same
  configuration and is deliberately granted no key.
- **pgx, sqlc and Goose.** SQL lives in `queries/`; sqlc reads the Goose migrations as its schema,
  so a query naming a column no migration created fails at generate time. Migrations are embedded
  in the binary and run by `api migrate up` — the same image is the server and the migration job.
  The request's transaction commits before the response is written, so a failed commit is a 500,
  never a 201 for a write that did not happen.
- **One error envelope.** Services return typed errors from `internal/apperr`; one middleware maps
  them to `{"error": {"code", "message", "trace_id"}}`, binding failures to 422, panics to a 500
  with a trace id, and every 401 carries `WWW-Authenticate: Bearer`.
- **Swagger and OpenAPI.** swaggo annotations on the handlers, converted to OpenAPI 3 at generate
  time and served at `/openapi.json` with Swagger UI at `/docs`. Both are off in production.
  `just gen-check` fails CI when the committed documents or the sqlc output are stale.
- **A distroless production image.** `CGO_ENABLED=0`, `-trimpath`, `static-debian12:nonroot`,
  about 11 MB, with a `HEALTHCHECK` that runs `api healthcheck` because there is no shell or curl
  to run anything else. Graceful shutdown on SIGTERM inside Cloud Run's ten seconds.
- **A real suite.** `go test -race` against Postgres — per-package isolation, a decoder test per
  JWT vulnerability class, a two-connection test that the rotation lock really blocks, a commit
  that fails answered with a 500, every migration up, down and up again, and both refresh
  transports through the router.

### The authentication example, and where it stops

| | |
| --- | --- |
| Passwords | Argon2id via `golang.org/x/crypto/argon2` at m=64 MiB, t=3, p=4, behind a semaphore — every hash holds 64 MiB, and goroutines make a few hundred concurrent logins an out-of-memory kill. |
| Unknown accounts | Verified against a throwaway digest anyway, so a miss costs what a hit costs. |
| Hash migration | Rehashed on login when the stored digest was made with other parameters than the current ones — login is the only moment the plaintext is in hand. |
| Tokens | HS256 via golang-jwt v5 with `sub`, `typ`, `iss`, `aud`, `iat`, `exp`, `jti`. Access tokens last fifteen minutes; refresh tokens thirty days and are refused as bearer tokens. |
| Decoding | `WithValidMethods(HS256)`, issuer, audience, required expiry and issued-at, and `typ` compared — each a documented vulnerability class when left out. |
| Sessions | A `refresh_sessions` row per refresh token, keyed by its `jti`; the token is never stored. Rotation locks the row `FOR UPDATE`. A spent token coming back revokes its whole family, and that revocation commits even though the request answers 401. |
| Logout and revocation | Logout revokes the family and never fails. A password change revokes every family and issues a new pair. Revocation latency is the access token's fifteen minutes. |
| Where the refresh token travels | `REFRESH_TRANSPORT=body` (the default): in the JSON body. `cookie`: an `HttpOnly`, `Secure`, `SameSite=Strict` cookie scoped to `/api/v1/auth`, never in a body, with refresh and logout behind a custom-header and `Origin` CSRF check — exactly the FastAPI template's browser transport. |
| 401 | Decided in one middleware, applied to the `private` route group, so a new route is authenticated unless somebody puts it on `public` on purpose. |

**What it does not do:** no roles or permission guards, no ownership checks, no password reset, no
email verification, no rate limit on login, and no job deleting expired `refresh_sessions` rows.

## What the generated `infra/` gives you

Each deploy target lives under `{{cookiecutter.component}}/deploy/<target>/` as a complete
implementation of one interface — an `infra/` directory whose justfile has the same recipes, and the
deploy workflows those recipes un-gate. The post-generation hook moves the chosen one's `infra/`
into place, installs its workflows at the repository root with the shared `ci.yml`, and deletes
the rest, so nothing outside `deploy/` refers to a target by path.

**There is no placeholder image.** `just infra-up` creates everything but the service;
`just infra-first-deploy` builds the production image for `linux/amd64` on your machine from the
committed tree, pushes it, records it as the stack's `serviceImage`, applies the stack — which
creates the service and its migration job or task on it — runs the migrations exactly as CD does,
and smoke-tests the result. CD owns every image after that one. The order, in every generated
`infra/README.md` and in the hook's closing message: bootstrap, `stack init`, `githubSubjectPrefix`
(AWS), `infra-up`, `infra-secrets`, `infra-first-deploy`, `infra-sync-github`, push.

**Setup runs from a machine, every later apply from CI** (ADR-0024, Pulumi standards MUST-9).
Those steps run once per environment from an operator's machine, because the identity CI applies
with is created by the first `infra-up`. After them, `<component>-infra.yml` previews every pull
request and applies every infrastructure change merged to `main`, as the stack's apply identity, in
the repository's `infrastructure` GitHub environment — whose protection rules, required reviewers
and `main` only, are the gate on every apply and are set by hand. On AWS the apply role holds
`AdministratorAccess`; on GCP the apply account holds one predefined admin role per service the
stack manages. Either can change anything in its account or project, which is why one per
environment is what bounds it.

**`gcp-cloud-run`.** A Pulumi program **in Go** that has never been applied and is ready to be: service APIs, an Artifact
Registry repository with a cleanup policy, runtime, CI and apply service accounts with no downloadable key,
a Workload Identity pool scoped to one repository, Secret Manager containers, and a Cloud Run service
that scales to zero, created on the first image — plus Cloud SQL and a migration job with a
database. The state bucket and KMS key come from a bootstrap script,
because a stack cannot create the store that holds its own state; secret values are never in code;
the pipeline owns the image tag and nothing else; components are unit-tested with
`pulumi.WithMocks` and no cloud credentials. The generated `infra/README.md` is the ordered list of
what a human has to do, and marks the step where money starts.

**`aws-ecs-express`.** The same shape on AWS, in the same Go: a VPC with public subnets for the
tasks and isolated ones for the database and no NAT gateway, an ECR repository that is immutable
and scanned, CloudWatch log groups that expire, an ECS cluster, an Express Mode service — task,
HTTPS load balancer and scaling from one resource — on the first image, Secrets Manager
secrets pinned by version, an ECS infrastructure role, execution roles, an empty runtime role for
the application, and three keyless GitHub roles: deploy, trusted from the `production` environment
only, a read-only preview, and apply, trusted from the `infrastructure` environment only — plus RDS for PostgreSQL and a migration task definition with a
database. The bootstrap script creates the state bucket, its KMS key and the account's GitHub OIDC
provider, all three shared by every service in the account. Nothing on this target scales to zero:
Express Mode runs at least one task behind a load balancer, and the generated `infra/README.md`
says so before it says anything else about cost.

## Maintaining this template

- **Generated files.** After changing a query, a migration, a handler's annotations or
  `cmd/openapi`, regenerate the shipped sqlc output and OpenAPI documents with
  `uv run --with cookiecutter python templates/maintenance/regen_gin_generated.py`. A fresh project
  must pass `just gen-check` before anybody has run a generator.
- **The contract.** `templates/contract-tests/` holds the HTTP suite both service templates answer
  to, and `run.sh` there runs it against all four backends. A change to routes, fields, status codes
  or the cookie is a change to both templates, in one commit.
- **CI.** `.github/workflows/templates.yml` renders every variant, runs each generated component's
  own gates, and runs the contract suite. A render with `-o <root>` lands in `<root>/api/`, with its
  workflows in `<root>/.github/workflows/`; the stack's contract tests find them by walking up to
  the directory that holds `.github`.

## Related

- [templates/README.md](../README.md)
- The rules a generated repository is built to satisfy are retrieved, not linked: ask Athena for
  the **Gin standards**, **starting a new service**, **layered architecture**,
  **containerization**, **GitHub Actions standards**, **Cloud Run service standards** and the
  **Pulumi standards**.
