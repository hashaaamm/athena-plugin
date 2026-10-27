# Infrastructure

Pulumi in Go, the service's own language. One component per concern, in
`pulumi/internal/components/`, composed in `pulumi/main.go`, which reads top to bottom as a
dependency graph.

Nothing here has been applied yet. A freshly generated repository has a stack that compiles and
unit-tests without any cloud access, and has never created a resource. Everything below is what
turns that into a running service, in the order it has to happen.

## What runs where

Unlike `backend/`, whose recipes all run in containers, **every recipe in `infra/` runs on your
machine.** Pulumi compiles a Go program with the Go toolchain of whoever runs `pulumi`, and
`pulumi`, `gcloud` and `gh` act with your own credentials; putting them in a container would mean
mounting those credentials into it. So this directory, and only this directory, has host
prerequisites:

| Tool | Needed for | Check |
| --- | --- | --- |
| Go {{ cookiecutter.go_version }} or newer | `just infra-test`, and every `pulumi` command (it compiles `main.go`) | `go version` |
| `golangci-lint` v2 | `just infra-lint` | `golangci-lint version` |
| `pulumi` CLI | preview, up, destroy, stack outputs | `pulumi version` |
| `gcloud` | the bootstrap and teardown scripts, and your own authentication | `gcloud version` |
| `gh` | `just infra-sync-github` | `gh auth status` |
| `python3` | `just infra-teardown` only (it reads Pulumi's JSON) | `python3 --version` |

Each recipe checks for the tools it uses and names the missing one. `just infra-test` needs only
Go: it runs against mocks, with no cloud account at all.

`go.sum` is not templated, for the same reason `backend/go.sum` is not: a checksum file frozen when
the template was written is stale on the day it ships. The first command is `cd infra && just lock`,
and its result is committed. CI runs `go mod tidy -diff` and fails on a `go.sum` that is missing or
out of date.

## The order, and why it cannot change

```
0. cd infra && just lock                           resolves go.sum; commit it
1. gcloud + pulumi installed and authenticated     nothing is created
2. a GCP project with billing enabled              the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain gcloud; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
5. just infra-preview                              read it, by a human
6. just infra-up                                   creates the service on a placeholder image
{%- if cookiecutter.use_postgres == "yes" %}
7. just infra-secrets                              generates the secrets the stack leaves empty
8. just infra-up                                   again: mounts the versions step 7 recorded
{%- if cookiecutter.use_sentry == "yes" %}
   (optional) gcloud secrets versions add …        the Sentry DSN, which a human supplies
{%- endif %}
9. just infra-sync-github                          copies the stack's outputs into repository variables
10. merge to main                                  cd.yml is no longer inert; the real image deploys
{%- else %}
{%- if cookiecutter.use_sentry == "yes" %}
7. gcloud secrets versions add …                   optional: the Sentry DSN, which a human supplies
8. just infra-sync-github                          copies the stack's outputs into repository variables
9. merge to main                                   cd.yml is no longer inert; the real image deploys
{%- else %}
7. just infra-sync-github                          copies the stack's outputs into repository variables
8. merge to main                                   cd.yml is no longer inert; the real image deploys
{%- endif %}
{%- endif %}
```

Step 3 is a script rather than a stack because a stack cannot create the bucket that holds its own
state — the first `pulumi up` would need somewhere to write before that somewhere exists.

Step 6 asks for confirmation and **refuses to run with nothing there to confirm** — "--yes must be
passed in to proceed when running in non-interactive mode". The recipe does not pass `--yes` for
you; this command creates billable resources and a human reading the preview is the point. From a
pipeline, or from an agent with no terminal, type it: `just infra-up dev --yes`. The stack name is
positional, so the flag has to follow one.

Secret *values* never appear in infrastructure code, in a stack configuration file, or in state —
not even a value the stack could have generated itself, because a generated value is in state like
any other. Ask Athena for the Pulumi standards (MUST-3); ADR-0023 records how this stack meets it.
The stack creates each Secret Manager container empty, grants one account access to it, and mounts
it only once its version *number* — which is not a secret — is in `Pulumi.<stack>.yaml`.
{%- if cookiecutter.use_postgres == "yes" %}

Step 7 is the out-of-band half, and it is a script rather than a stack resource for exactly that
reason. `just infra-secrets` (`scripts/secrets.sh`) generates two values, pipes each one straight to
where it is kept, and records the version numbers as `jwtSecretVersion` and `dbAdminPasswordVersion`:

- **`jwt-secret`**, the HS256 key the service signs tokens with: 64 random characters, mounted on
  the service as `JWT_SECRET` and on nothing else.
- **`db-admin-password`**, the password of Cloud SQL's built-in `postgres` login. The script sets it
  on the instance and stores it for the migration job, which mounts it as `DATABASE_PASSWORD`. The
  service never sees it — see [Two database identities](#two-database-identities).

No value is printed, written to a file or passed as a command-line argument. Run it again and it
keeps what is already recorded; `just infra-secrets dev --rotate` replaces both. **Rotating signs
every user out** — every token was signed with the old key — and the migration job cannot log in
between the rotation and the `up` that pins the new password, so rotate between deploys.

Step 8 is the same `up` again: it is the one that mounts the versions step 7 recorded. Until both
are recorded the stack exports `secrets_ready=false`, and step 9 refuses to un-gate CD — the image
CD would deploy refuses to start without its signing key.
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

The Sentry DSN is the one value a human adds, and it is optional: the container stays empty and
unmounted until you add a version — on stdin, `gcloud secrets versions add <id> --data-file=-` —
and record its number as `sentryDsnVersion`, then `up` again.
{%- endif %}

Merging is last because the service exists before the image does. `pulumi up` creates a Cloud Run
service on Google's public `hello` container, Cloud Run assigns it a URL, and only then can a
pipeline push a real image at it. That ordering matters more than it looks: anything that compiles
its own origin into a build artefact — a client bundle with an API base URL in it — cannot be built
correctly until the URL exists, so the sequence is placeholder image, then URL into a repository
variable, then the real build. A browser client relies on exactly that; see
[Browser clients](#browser-clients).

### Taking it down, which is not one command

```
1. just infra-destroy dev                 removes every resource the stack created
2. just infra-teardown                    removes the two resources the stack never created
                                          (refuses when another project shares the backend)
3. gcloud kms keys delete … (30 days on)  KMS will not go faster; step 2 prints the commands
```

{% if cookiecutter.use_postgres == "yes" -%}
`destroy` takes the secret containers with it — every version `just infra-secrets` added, so a
stack brought back up needs step 7 again — and drops the service's IAM database user only after
the database, because the grants the migration job gave it are what Postgres refuses to drop a
role over. The `postgres` login goes with the instance.

{% endif -%}
**`pulumi destroy` does not undo step 3 of the setup order at the top of this page.** It removes
what the stack created and nothing else, and the state bucket and the KMS key were created by
`state-bucket.sh`, with plain gcloud. Pulumi has never known about them, so no Pulumi command will
ever take them away. Destroy a stack and walk away and you are left holding a versioned GCS bucket
and a KMS key ring, quietly, indefinitely — which is the whole reason `teardown.sh` exists.

**The bucket may not be yours, and that is the default rather than the edge case.**
`state-bucket.sh` derives the name from the GCP project alone — `gs://${PROJECT}-pulumi-state`,
with no per-service component — and the key ring `pulumi` and key `stack` the same way. Generate a
second service into the same project and region and it bootstraps onto the same bucket and the same
key, by construction: the script finds them already there and reuses them. One backend then holds
several Pulumi projects, a directory each under `.pulumi/stacks/`, and deleting the bucket deletes
all of their state. So `teardown.sh` lists that prefix first and refuses when it finds a directory
this project does not own, printing the `pulumi stack rm --remove-backups` path instead — that is
the Pulumi standards' MUST-8; ask Athena for it. A listing it cannot read counts as shared.

The order is the second trap. The bucket holds the state `pulumi destroy` reads, so deleting the
bucket first strands the stack: every resource it created still exists, still bills, and the only
record of what those resources are has just been deleted. `teardown.sh` will not let that happen by
accident either — it exports every stack in the backend, `--all` rather than only this Pulumi
project's, and refuses while any of them still holds a resource. Then it asks you to type the
bucket name, and refuses outright when nothing can answer, for the same reason `just infra-up`
refuses a non-interactive apply. There is deliberately no `--yes` here: an unattended teardown of a
state bucket is not something this repository will do on your behalf.

Step 3 is separate because **a KMS key cannot be hard-deleted on demand.** `destroy` schedules a
key *version* for destruction and it sits in that state for the key's destroy-scheduled duration —
30 days by default, fixed when the key was created and not changeable afterwards — before it
becomes `DESTROYED`. Only a destroyed version can be deleted, only a key with no undeleted versions
can be deleted, and only a key ring with no keys can be deleted. `teardown.sh` does the part that
can be done now, prints the three commands for later, and does not pretend the key is gone. Until
the date passes, `gcloud kms keys versions restore` can still cancel it. (Sourced from Google's
*Destroy and restore key versions* and *Delete Cloud KMS resources*, both read 2026-09-21.)

Two charges outlive the script and both stop on their own. A deleted bucket is soft-deleted rather
than gone — Cloud Storage enables soft delete on every bucket that supports it, default retention
7 days, and soft-deleted data keeps accruing storage charges until it expires. A key version
scheduled for destruction is still billed until it reaches `DESTROYED`. Both are pennies on a state
bucket and a single key; neither is a reason to skip the teardown.

`destroy` also leaves the GitHub repository variables behind, naming infrastructure that no longer
exists. Delete them in the repository settings, or run `gh variable delete <NAME>` for each one
`sync-github.sh` set.

If the GCP project exists only for this service, deleting the project ends every charge in one
command and is the cleanest teardown there is: `gcloud projects delete {{ cookiecutter.gcp_project_id }}`.

## Layout

```
infra/
├── justfile                    test lint preview up sync-github destroy teardown lock
├── bootstrap/state-bucket.sh   plain gcloud; creates the bucket Pulumi stores state in
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
{%- if cookiecutter.use_postgres == "yes" %}
├── scripts/secrets.sh          generates the secrets the stack leaves empty; records their versions
{%- endif %}
├── scripts/sync-github.sh      copies stack outputs into GitHub repository variables; checks secrets_ready
└── pulumi/
    ├── Pulumi.yaml             project, runtime, and the GCS backend URL
    ├── Pulumi.dev.yaml         per-stack configuration; a second environment is a sibling file
    ├── go.mod                  the stack's own module; nothing in backend/ imports it
    ├── main.go                 composition root
    ├── main_test.go            the whole stack against mocks, with and without a browser client
    ├── contract_test.go        exports ↔ sync-github.sh ↔ the workflows' `vars.`
    └── internal/
        ├── stackconfig/        typed StackConfig; the only place configuration keys are named
        ├── components/         one file per concern, each with its tests
        └── infratest/          pulumi.WithMocks plus a recorder — no credentials needed
```

Two properties keep this from accreting. **Components take a `StackConfig` by value** and their
explicit dependencies, so none of them reads global configuration and each can be constructed in a
test. **`Service` describes a Cloud Run service, not this app**: the port, the probe path, the
database, the secrets and the jobs are all fields of `ServiceArgs` with usable zero values. A second
service — a worker, an admin API — is one more `components.NewService` call in `main.go`, not a
copied file with a different string in it.

## What the stack provisions

| Component | Resources | Cost posture |
| --- | --- | --- |
| `apis` | the service APIs everything else needs, enabled first, with every other resource depending on them | free |
| `registry` | one Artifact Registry repository, keeping the last 10 images | pennies |
| `identities` | a runtime service account, {% if cookiecutter.use_postgres == "yes" %}a migrator service account, {% endif %}a CI service account, and a Workload Identity pool scoped to one repository | free |
| `secrets` | empty Secret Manager containers, each readable by exactly one account, mounted at the version recorded in stack configuration — never a value | free |
| `service` | one Cloud Run service scaling to zero{% if cookiecutter.use_postgres == "yes" %}, plus a migration job running as the migrator{% endif %} | nothing while idle |
{%- if cookiecutter.use_postgres == "yes" %}
| `database` | Cloud SQL Postgres 16, shared-core, 10 GB HDD, zonal, 7 daily backups; IAM database authentication on, connectors required, and the service's IAM database user | **the only standing charge** |
{%- endif %}

{% if cookiecutter.use_postgres == "yes" -%}
Everything except the database is free at rest: Cloud Run scales to zero, and an idle environment
costs nothing. Cloud SQL has no scale-to-zero, so **the database bills from step 6 onwards, whether
or not anyone uses it.** That is where the money starts.

### The database settings are already the cheapest ones that work

That is the question worth answering before you go looking for a knob, so: there isn't one. Every
setting below is at its minimum, and each is at its minimum for a reason you can check.

| Setting | Value | Why nothing is lower |
| --- | --- | --- |
| `databaseTier` | `db-f1-micro` | Cloud SQL offers exactly two shared-core machine types, `db-f1-micro` and `db-g1-small`. This is the smaller one. Shared core is Cloud SQL Enterprise edition only. |
| `databaseDiskGb` | `10` | 10 GB is Cloud SQL's minimum instance storage and also its default. `DiskAutoresize` is on, so it grows when it has to — and never shrinks back, which is why starting at the floor matters. The stack refuses a smaller value when it loads. |
| `DiskType` | `PD_HDD` | The cheaper of the two storage types per GB, and offered on Enterprise edition only. |
| `AvailabilityType` | `ZONAL` | `REGIONAL` is high availability: a standby in a second zone, at roughly double the instance charge. |
| `Edition` | `ENTERPRISE` | Enterprise Plus costs more per vCPU and does not offer shared-core machines at all. |
| `PointInTimeRecoveryEnabled` | off outside production | It is driven by `IsProduction()`, and PITR keeps write-ahead logs, which are billed storage. |

**What the floor costs you, stated plainly:** shared-core instances and single-zone instances are
both excluded from the Cloud SQL SLA. `db-f1-micro` with no standby is a development database with
a real backup schedule, not a database anybody has promised will stay up. Moving off the floor for
production means `AvailabilityType: "REGIONAL"` and a dedicated-core tier, in that order.

Two things are below this and both buy the saving with something:

- **Fewer backups, or none.** `RetainedBackups: 7` is Cloud SQL's own default for Enterprise
  edition; the valid range is 1 to 365 and automated backups can be switched off entirely. Backup
  storage is billed, so this is the only remaining lever — and it is your recovery window.
- **Stopping the instance.** Stopping suspends the instance charge, but storage and IP charges
  continue. It is cheaper, not free, and the service is down while it lasts.

The real zero is `just infra-destroy` (and then `just infra-teardown` — read the teardown order
above first). There is no configuration of Cloud SQL that costs nothing.

**No figure is quoted here on purpose.** The standing charge is three line items — the shared-core
machine, 10 GB of HDD, and backup storage — and each is priced per region and changes without this
file changing. Price those three in the Google Cloud pricing calculator for
`{{ cookiecutter.gcp_region }}` before you apply, then read the real number off your first invoice.
A figure copied into a README is wrong somewhere and out of date everywhere.

(Machine types, storage minimum, HDD availability, backup retention range and stopped-instance
billing are from Google's Cloud SQL for PostgreSQL documentation and the `gcloud sql instances
create` reference, read 2026-09-21; the SLA exclusions are from the Cloud SQL SLA effective
2025-10-03.)

### Two database identities

| | Logs in as | With | May |
| --- | --- | --- | --- |
| **The service** | its runtime service account, as the Cloud SQL IAM user `<account>@<project>.iam` | IAM database authentication through the Cloud SQL Go connector in its own process — `DATABASE_AUTH=cloud-sql-iam`, `CLOUD_SQL_INSTANCE`, `DATABASE_NAME`, `DATABASE_USER`. No password exists. | read and write rows, use sequences — nothing that changes the schema |
| **The migration job** | Cloud SQL's built-in `postgres` login, running as the migrator service account | the password `just infra-secrets` set, mounted from `db-admin-password` at `dbAdminPasswordVersion`, over Cloud Run's Cloud SQL connection — the Unix socket at `/cloudsql/<connection-name>` | own and change the schema |

The stack creates the service's IAM user (`sql.User`, type `CLOUD_IAM_SERVICE_ACCOUNT`) with no
privileges. `migrate up` grants them, **after** the migrations and on every run, so a table a
migration just created is covered, and sets default privileges so the next one is too. The runtime
account holds `cloudsql.client` and `cloudsql.instanceUser` and cannot read the admin password;
the migrator holds `cloudsql.client` and that one secret, and cannot read the signing key.

The instance keeps a public IP with **zero authorised networks** and **connector enforcement
`REQUIRED`** — the GCP infrastructure standards' grading for a public-IP instance. Every client is
on a connector already: the service's Go connector, and the job's Cloud Run Cloud SQL connection,
which is the Cloud SQL Auth Proxy. There is no hostname to reach and no route for anything else.
The service has no Cloud SQL volume at all; only the job mounts one.
{%- else -%}
Everything in this stack is free at rest. Cloud Run scales to zero and an idle environment costs
nothing; you pay for requests, image storage, and egress. The bootstrap bucket and KMS key in step
3 are the first charges, and they are pennies.
{%- endif %}

## The two environments, which are not the same environment

There are two settings in this repository called some version of "environment", they are set
independently, and reading them as one thing is the most common way a first deploy surprises
its author.

| | Where it is set | What it does | Value in the `dev` stack |
| --- | --- | --- | --- |
| **The stack's** | `{{ cookiecutter.project_slug }}:environment` in `Pulumi.dev.yaml` | Names resources, and nothing else. `{{ cookiecutter.project_slug }}-dev`, `{{ cookiecutter.project_slug }}-run-dev`{% if cookiecutter.use_postgres == "yes" %}, `{{ cookiecutter.project_slug }}-jwt-secret-dev`{% endif %}. It is also what `IsProduction()` reads to decide{% if cookiecutter.use_postgres == "yes" %} deletion protection and point-in-time recovery{% else %} anything production-only{% endif %}. The application never sees it. | `dev` |
| **The application's** | `{{ cookiecutter.project_slug }}:appEnvironment` in `Pulumi.dev.yaml` | Becomes `ENVIRONMENT` on the Cloud Run revision. One of `local`, `test`, `staging`, `production` — the values `backend/internal/config` accepts. It decides how the service *behaves*: `production` serves no `/docs` and no `/openapi.json`. | `staging` |

They are separate because they vary separately. A stack called `dev` that the whole company can
reach is production as far as the application is concerned; a stack called `prod` standing up a
customer demo is not. Tying one to the other would force a choice between a resource-naming scheme
and a behaviour switch.

`staging` is the default because it is the honest reading of a first deploy: production-shaped
logging, a placeholder database password still refused, and `/docs` reachable — which is the first
thing anybody does with a URL they have just been given. **Set `appEnvironment` to `production` in
the stack that real users reach.** Anything outside the four values is refused by
`internal/stackconfig` when the stack loads, rather than by a container exiting several minutes
into an apply.

## Browser clients

This repository is backend only. A browser client is generated from the handbook's
`cookiecutter-react-app` template into a repository of its own, with its own Pulumi stack and its
own pipelines (ADR-0022). This backend answers the HTTP contract that client is written against —
the same routes, JSON, error envelope{% if cookiecutter.use_postgres == "yes" %}, refresh-cookie behaviour{% endif %} and OpenAPI 3 document at
`/openapi.json` as the FastAPI template — so pointing one at it is configuration on both sides and
code on neither:

1. **Deploy this stack first**, through `just infra-sync-github`. Its `service_url` output is the
   API's address.
2. **Generate and deploy the client** from `cookiecutter-react-app`, following that repository's
   `infra/README.md`. Its `API_URL` repository variable is this stack's `service_url`, and its stack
   exports `site_url`, the address the browser loads the app from.
3. **Allow that origin here**, in `infra/pulumi/Pulumi.<stack>.yaml`:

   ```yaml
   {{ cookiecutter.project_slug }}:corsOrigins: "https://<the client's site_url host>"
{%- if cookiecutter.use_postgres == "yes" %}
   {{ cookiecutter.project_slug }}:refreshTransport: cookie
{%- endif %}
   ```

   then `just infra-preview` and `just infra-up`. The service gets a new revision with
   `CORS_ORIGINS` set to the list{% if cookiecutter.use_postgres == "yes" %} and `REFRESH_TRANSPORT=cookie`: the refresh token travels
   as an HttpOnly cookie rather than in storage the bundle's JavaScript can read, and refresh and
   logout require the CSRF header and a listed `Origin`{% endif %}.

`corsOrigins` is comma-separated, and each entry is an origin exactly as a browser sends it — a
scheme, a host and an optional port, no path and no trailing slash. Empty, the default, means no
browser on another origin may call the API, and `CORS_ORIGINS` is not set at all.{% if cookiecutter.use_postgres == "yes" %} `refreshTransport` is
`body` (the default, for server-to-server and mobile callers) or `cookie`, and `cookie` with no
origins is refused: the CSRF check accepts listed origins only, so every refresh would fail.{% endif %} The
stack refuses what the service would refuse at start-up, at preview rather than as a revision that
never becomes ready.{% if cookiecutter.use_postgres == "yes" %} Neither variable reaches the migration job; it serves no
HTTP.{% endif %}
{%- if cookiecutter.use_postgres == "yes" %}

**The one thing that does not work on the default URLs, and why.** The refresh token is a
`SameSite=Strict` cookie, and a session that survives a reload needs the SPA and the API on one
*site*. The default hostnames are not: `run.app` is on the Public Suffix List, so two Cloud Run URLs
are two sites, and a client on `cloudfront.net` and an API on `run.app` are two sites as well. The
browser will not send a Strict cookie from one to the other. Signing in works, and then a reload —
or the access token's fifteen minutes — signs the user out. The fix is a name you own: put the SPA
and the API on one site, say `app.example.com` and `api.example.com`, list the SPA's origin in
`corsOrigins`, and point the client's `API_URL` at the API's name. Relaxing the cookie to
`SameSite=None` is not the fix: it reopens the cross-site request surface the Strict setting
closes, and browsers that block third-party cookies drop it anyway.
{%- endif %}

## Who owns what

The stack owns the *shape* of the Cloud Run resource — service account, scaling, secret wiring and
the version each secret is pinned to, probes, CORS{% if cookiecutter.use_postgres == "yes" %}, the database settings, the migration job's Cloud SQL
attachment{% endif %}. The CD pipeline owns exactly one field: the image tag. Secret *values* belong
to neither: {% if cookiecutter.use_postgres == "yes" %}`just infra-secrets` and {% endif %}a human add them out of band, and the stack only records
which version to mount. The image is declared with `ignore_changes`, and
every preview and apply passes `--refresh`.

Both halves are needed. `ignore_changes` suppresses the diff between the program and Pulumi's
state; it does not tell state that CD changed the image. Without `--refresh`, program and state
both still say `hello`, there is no diff to ignore, and the next apply of any unrelated field sends
the whole template — stale image included. The `just infra-*` recipes are where that is enforced,
and `contract_test.go` fails if one of them loses the flag.

Environment variables are written in name order. A Go map has no order, and a revision whose
environment came out shuffled would be a diff — and a new revision — for nothing.

## Prepackaged, or manual

Every step of getting this repository into production is one or the other, and the line is not a
matter of taste. **It is prepackaged when the value or the decision already exists inside this
repository, and manual when it needs an account, a bill, a name you own, or a human looking at
something.** Anything on the first side that is not actually shipped is a gap in the template, not
a step for you; anything on the second side cannot be shipped by any template, so it is written
down here instead of being discovered on the day.

Prepackaged — generated, wired together, and covered by a test:

| | Where it lives |
| --- | --- |
| The Cloud Run service, the registry, the identities, the Workload Identity pool, the secret containers{% if cookiecutter.use_postgres == "yes" %}, the database, the migration job{% endif %} | `pulumi/main.go` |
| The service's CORS allowlist{% if cookiecutter.use_postgres == "yes" %} and refresh-token transport{% endif %} for a browser client, checked before they deploy | `corsOrigins`{% if cookiecutter.use_postgres == "yes" %} and `refreshTransport`{% endif %} in `Pulumi.<stack>.yaml`, read by `pulumi/internal/stackconfig` |
{%- if cookiecutter.use_postgres == "yes" %}
| The signing key and the database admin password, generated, stored and their versions recorded — never in a file, an argument or state | `scripts/secrets.sh`, via `just infra-secrets` |
| The service's database identity and its grants | the IAM user in `pulumi/internal/components/database.go`; the grants in `backend/internal/app/migrate.go`, on every `migrate up` |
{%- endif %}
| Every repository variable the pipelines read, and the refusal to set them before the secrets exist | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| Removing the state bucket and the KMS key, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| Backend image build{% if cookiecutter.use_postgres == "yes" %}, migration{% endif %} and deploy | `.github/workflows/cd.yml` |
| The stack's tests and a preview on every pull request | `.github/workflows/infra.yml` |

The stack's exports, the variables `sync-github.sh` sets and the `vars.` names the workflows read
have to agree exactly, so `pulumi/contract_test.go` asserts that they do. A name changed in one of
the three is then a failing build rather than a deploy job that is skipped forever and never says
why.

Manual, and no template can change that:

| | Why it cannot be shipped |
| --- | --- |
| A GCP project with a billing account attached | It costs money and names a payer |
| `./bootstrap/state-bucket.sh`, then `pulumi stack init` | A stack cannot create the bucket that holds its own state |
| Reading `just infra-preview` before the first apply | The point of the step is that a human looked |
{%- if cookiecutter.use_postgres == "yes" %}
| Running `just infra-secrets`, then `just infra-up` again | It writes to the cloud with your credentials, and a value is generated where it is used, never in this repository |
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
| The Sentry DSN, in its container | It comes from your Sentry account |
{%- endif %}
| `SENTRY_ORG`, `SENTRY_PROJECT`, `SENTRY_AUTH_TOKEN` | They identify an account outside this project |
| A custom domain, its DNS records and its certificate | You own the name; nothing here can |
| A browser client's origin in `corsOrigins` | It is another repository's output, and that repository is deployed after this one |
| Confirming `just infra-teardown`, and the KMS commands it leaves for 30 days' time | Deleting a state bucket is irreversible, and KMS will not go faster |
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

## What a human has to do, and what no script can

| | Who | Where |
| --- | --- | --- |
| Install Go, `golangci-lint`, `pulumi`, `gcloud` and `gh`, and authenticate the last three | you | your machine |
| Resolve and commit `infra/pulumi/go.sum` | you, once | `cd infra && just lock` |
| Create the GCP project and attach a billing account | you | console or `gcloud` |
| Run the bootstrap script | you, once | `./infra/bootstrap/state-bucket.sh` |
| `pulumi stack init` with the KMS secrets provider | you, once | the bootstrap script prints the command |
| Read the preview before the first apply | you | `just infra-preview` |
{%- if cookiecutter.use_postgres == "yes" %}
| Generate the signing key and the database admin password, and record their versions | you, once per stack; again with `--rotate` | `just infra-secrets`, then `just infra-up` |
{%- endif %}{% if cookiecutter.use_sentry == "yes" %}
| Add the Sentry DSN and record its version number | you | `gcloud secrets versions add … --data-file=-`, `sentryDsnVersion` in `Pulumi.<stack>.yaml`, then `just infra-up` |{% endif %}
| Set the repository variables | `sync-github.sh` | `just infra-sync-github` |
| Set `SENTRY_ORG`, `SENTRY_PROJECT` and the `SENTRY_AUTH_TOKEN` secret | you | GitHub settings |
| Protect `main`, and require CI before merge | you | GitHub settings |
| Tear the stack down, when it is time | you | `just infra-destroy`, then `just infra-teardown` |
| Delete the KMS key and key ring, 30 days later | you | the commands `teardown.sh` prints |

Do not create a service-account JSON key for any of this. You authenticate as yourself; CI
authenticates by exchanging GitHub's OIDC token, which is what the Workload Identity pool exists
for. A downloaded key is a long-lived credential in a file and the handbook bans them.

## Adding a second environment, or a second service

A second environment is a new `Pulumi.<stack>.yaml` and a second `state-bucket.sh` run against its
project — not a second program. Every resource name already carries the environment, so two stacks
can share one project without colliding. Two stacks sharing one bucket is why `teardown.sh` checks
every stack in the backend — `pulumi stack ls --all`, because without the flag it sees only this
Pulumi project's — rather than the one you name; run it against another project by setting
`GCP_PROJECT`, the same variable `state-bucket.sh` reads. Set both environment keys in the new
file: `environment` for the names, `appEnvironment` for how the service behaves. A `prod` stack
that keeps the default `staging` is a production service serving its own OpenAPI document.

A second **service** in the same project shares the bucket too, and that one is not a stack this
program can enumerate: it is another Pulumi project with its own directory in the backend. Tearing
down either service leaves the other's state where it is — `teardown.sh` refuses on the directory
listing — so the bucket and the key outlive both, and the last one out deletes them.

A second Cloud Run service in this stack is one more `components.NewService` call in `main.go`
with its own runtime service account. Give it its own account rather than reusing this one: a
service that holds another service's roles turns any compromise of the least protected surface into
database and Secret Manager access. A different port, a different probe, no database and no
secrets are all fields of the same `ServiceArgs`. Deploying it is that plus a workflow modelled on
`cd.yml`, an export, and a row in `sync-github.sh`; `contract_test.go` is what tells you when you
have done two of the three. A browser client is not one of these: it is its own repository and its
own stack, generated from `cookiecutter-react-app`.

## Related

- Ask Athena for the **Pulumi standards** and the **Cloud Run service standards** — they are the
  graded rules this stack is built to satisfy, and they are what a reviewer will check it against.
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi GCP provider (Go SDK `github.com/pulumi/pulumi-gcp/sdk/v10`) — https://www.pulumi.com/registry/packages/gcp/
- Pulumi unit testing in Go (`pulumi.WithMocks`) — https://www.pulumi.com/docs/iac/concepts/testing/unit/
- Cloud Run container runtime contract — https://cloud.google.com/run/docs/container-contract
