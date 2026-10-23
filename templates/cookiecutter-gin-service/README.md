# cookiecutter-gin-service

> **This is a published copy.** It is generated from Athena's source and rewritten on every publish, so an edit made here is lost at the next one. Report a problem as an issue on the repository you found it in.

A production-ready **Gin service in Go, in a monorepo-shaped repository**, with the Pulumi stack
that deploys it to Cloud Run. It generates a repository whose backend already satisfies the
handbook's Gin standards — layering enforced by the linter, typed configuration, pgx with
sqlc-generated queries, Goose migrations embedded in the binary, a distroless image, Cloud Run
health endpoints, CI, and a CD pipeline that is inert until configured.

**It answers the same HTTP contract as the FastAPI service template.** Same routes, status codes,
field names, error envelope, refresh cookie and OpenAPI schema names — so that template's React
`frontend/` drops into a generated Gin repository without an edit. One HTTP suite, run against both
templates in CI, is what keeps that true.

**With a database, it ships the two health probes and six authentication endpoints** — register,
login, refresh, logout, me, change password. They are the worked example of the layering and a
feature every service eventually needs. Session handling is complete — rotating refresh tokens,
reuse detection, logout and revocation. Authorization is not: no roles, no permission guards, no
ownership checks. **With `use_postgres=no` the endpoints are the two health probes and nothing
else**, because half an auth system is worse than none.

## Use it

```bash
uvx cookiecutter gh:hashaaamm/athena-plugin --directory templates/cookiecutter-gin-service   # interactive
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-gin-service \
  project_name="Billing API" go_module="github.com/acme/billing-api"
```

Then, in the generated repository:

```bash
cp .env.example .env
(cd backend && just lock)   # resolve go.sum, in a container
just dev                    # http://localhost:8000/docs
just check                  # everything CI runs

(cd infra && just lock)     # the Pulumi program's go.sum — needs Go on your machine
just infra-test             # the component tests; no cloud credentials needed
```

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
| `project_slug` | derived | Directory name, Compose project, Cloud Run service, refresh cookie prefix. |
| `description` | … | One line. The OpenAPI description. |
| `author` | `Your Name` | Attribution. |
| `go_module` | `github.com/your-org/<slug>` | The module path both `go.mod` files declare and every import uses. Set it to the repository's real path. |
| `go_version` | `1.27` | The `go` directive, the build images and CI's toolchain. |
| `gcp_project_id` | `your-gcp-project` | The GCP project the Pulumi stack targets. Not in any workflow. |
| `gcp_region` | `europe-west1` | Cloud Run, Cloud SQL and Artifact Registry all live here. |
| `github_repository` | `your-org/<slug>` | `owner/repo`. Becomes the Workload Identity provider's attribute condition — the one line stopping any other repository from assuming the CI identity. |
| `use_postgres` | `yes` | `no` drops migrations, queries, the generated data layer, the database from Compose and the stack, readiness' database check **and the whole authentication example**. |
| `use_sentry` | `yes` | `no` drops `sentry-go` and `internal/observability`. `yes` wires it with an **empty DSN by default** — local development reports nothing, by rule. |

There is no frontend variable. The web client is the FastAPI template's `frontend/`, added by hand;
the generated `infra/README.md` lists the steps, and the one Pulumi switch (`frontendEnabled`) that
deploys it.

## The brace traps this template handles explicitly

Jinja's `{{ }}` collides with three things in this repository:

1. **GitHub Actions' `${{ }}`.** `cookiecutter.json` declares `_copy_without_render:
   [".github/workflows/*"]`, so **no workflow may contain a cookiecutter variable.** Everything
   project-specific comes from repository variables, which is also why CD is inert until
   configured. The Pulumi stack exports every variable name the workflows read, the sync script
   copies them in, and a test in `infra/pulumi` fails if the three disagree.
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

A Pulumi program **in Go** that has never been applied and is ready to be: service APIs, an Artifact
Registry repository with a cleanup policy, runtime and CI service accounts with no downloadable key,
a Workload Identity pool scoped to one repository, Secret Manager containers, and a Cloud Run service
that scales to zero — plus Cloud SQL and a migration job with a database, and the web client's Cloud
Run service when `frontendEnabled` is set. The state bucket and KMS key come from a bootstrap script,
because a stack cannot create the store that holds its own state; secret values are never in code;
the pipeline owns the image tag and nothing else; components are unit-tested with
`pulumi.WithMocks` and no cloud credentials. The generated `infra/README.md` is the ordered list of
what a human has to do, and marks the step where money starts.

## Maintaining this template

- **Generated files.** After changing a query, a migration, a handler's annotations or
  `cmd/openapi`, regenerate the shipped sqlc output and OpenAPI documents with
  `uv run --with cookiecutter python templates/maintenance/regen_gin_generated.py`. A fresh project
  must pass `just gen-check` before anybody has run a generator.
- **The contract.** `templates/contract-tests/` holds the HTTP suite both service templates answer
  to, and `run.sh` there runs it against all four backends. A change to routes, fields, status codes
  or the cookie is a change to both templates, in one commit.
- **CI.** `.github/workflows/templates.yml` renders every variant, runs each generated repository's
  own gates, and runs the contract suite.

## Related

- [templates/README.md](../README.md)
- The rules a generated repository is built to satisfy are retrieved, not linked: ask Athena for
  the **Gin standards**, **starting a new service**, **layered architecture**,
  **containerization**, **GitHub Actions standards**, **Cloud Run service standards** and the
  **Pulumi standards**.
