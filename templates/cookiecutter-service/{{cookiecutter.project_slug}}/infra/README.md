# Infrastructure

Pulumi in Python. One component per concern, composed in `pulumi/__main__.py`, which reads top to
bottom as a dependency graph.

Nothing here has been applied yet. A freshly generated repository has a stack that type-checks and
unit-tests without any cloud access, and has never created a resource. Everything below is what
turns that into a running service, in the order it has to happen.

## The order, and why it cannot change

```
1. gcloud + pulumi installed and authenticated     nothing is created
2. a GCP project with billing enabled              the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain gcloud; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
5. just infra-preview                              read it, by a human
6. just infra-up                                   creates the service on a placeholder image
7. gcloud secrets versions add …                   values a human supplies, never Pulumi
8. just infra-sync-github                          copies the stack's outputs into repository variables
9. merge to main                                   cd.yml is no longer inert; the real image deploys
```

Step 3 is a script rather than a stack because a stack cannot create the bucket that holds its own
state — the first `pulumi up` would need somewhere to write before that somewhere exists.

Step 6 asks for confirmation and **refuses to run with nothing there to confirm** — "--yes must be
passed in to proceed when running in non-interactive mode". The recipe does not pass `--yes` for
you; this command creates billable resources and a human reading the preview is the point. From a
pipeline, or from an agent with no terminal, type it: `just infra-up dev --yes`. The stack name is
positional, so the flag has to follow one.

Step 7 is separate because secret *values* must never appear in infrastructure code, in a stack
configuration file, or in state. The stack creates the empty container and grants access to it;
what goes inside is added out of band. Ask Athena for the Pulumi standards for the graded rule.

Step 9 is last because the service exists before the image does. `pulumi up` creates a Cloud Run
service on Google's public `hello` container, Cloud Run assigns it a URL, and only then can a
pipeline push a real image at it. That ordering matters more than it looks: anything that compiles
its own origin into a build artefact — a client bundle with an API base URL in it, a canonical URL
in a sitemap — cannot be built correctly until the URL exists, so the sequence is placeholder
image, then URL into a repository variable, then the real build.

{% if cookiecutter.include_frontend == "yes" -%}
This repository has exactly that artefact, so the sequence above is not a caution — it is what
makes the frontend work. Vite inlines `VITE_API_URL` into the JavaScript at build time. Step 6
creates both Cloud Run services and Cloud Run assigns each a URL; step 8 copies the backend's into
`SERVICE_URL`; step 9 is the first build that can therefore compile the right origin into the
bundle. `frontend-cd.yml` refuses to build at all when neither `SERVICE_URL` nor `FRONTEND_API_URL`
is set, because the alternative is a bundle that calls `http://localhost:8000`, deploys cleanly,
serves a page and answers nothing.

The consequence worth remembering afterwards: **changing the API's address means rebuilding the
frontend image**, not editing a variable on a running revision. Re-run `just infra-sync-github`,
then re-run Frontend CD from the Actions tab — nothing under `frontend/` has to change for the
deployed bundle to be wrong.
{%- endif %}

### Taking it down, which is not one command

```
1. just infra-destroy dev                 removes every resource the stack created
2. just infra-teardown                    removes the two resources the stack never created
                                          (refuses when another project shares the backend)
3. gcloud kms keys delete … (30 days on)  KMS will not go faster; step 2 prints the commands
```

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
[Pulumi standards](https://engineeringathena.com/rules/iac/pulumi-standards) MUST-8. A listing it
cannot read counts as shared.

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
*Destroy and restore key versions* and *Delete Cloud KMS resources*, both read 2026-09-21 and both
showing "Last updated 2026-09-18 UTC".)

Two charges outlive the script and both stop on their own. A deleted bucket is soft-deleted rather
than gone — Cloud Storage enables soft delete on every bucket that supports it, default retention
7 days, and soft-deleted data keeps accruing storage charges until it expires. A key version
scheduled for destruction is still billed until it reaches `DESTROYED`. Both are pennies on a state
bucket and a single key; neither is a reason to skip the teardown.

If the GCP project exists only for this service, deleting the project ends every charge in one
command and is the cleanest teardown there is: `gcloud projects delete {{ cookiecutter.gcp_project_id }}`.

## Layout

```
infra/
├── bootstrap/state-bucket.sh   plain gcloud; creates the bucket Pulumi stores state in
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
├── scripts/sync-github.sh      copies stack outputs into GitHub repository variables
└── pulumi/
    ├── Pulumi.yaml             project, and the GCS backend URL
    ├── Pulumi.dev.yaml         per-stack configuration; a second environment is a sibling file
    ├── __main__.py             composition root
    ├── config.py               typed StackConfig; the only place configuration keys are named
    ├── components/             one file per concern
    └── tests/                  pulumi.runtime.set_mocks — no credentials needed
```

Two properties keep this from accreting. **Components take a frozen `StackConfig`** and their
explicit dependencies, so none of them reads global configuration and each can be constructed in a
test. **`Service` describes a Cloud Run service, not this app**: the port, the probe path, the
database, the secrets and the jobs are all arguments with defaults. A second service — a worker, a
frontend, an admin API — is one more instantiation in `__main__.py`, not a copied file with a
different string in it.

## What the stack provisions

| Component | Resources | Cost posture |
| --- | --- | --- |
| `apis` | the service APIs everything else needs, enabled first with an explicit `depends_on` | free |
| `registry` | one Artifact Registry repository, keeping the last 10 images | pennies |
| `identities` | a runtime service account{% if cookiecutter.include_frontend == "yes" %}, a second one for the frontend{% endif %}, a CI service account, and a Workload Identity pool scoped to one repository | free |
| `secrets` | Secret Manager containers, and the IAM to read them, granted per secret{% if cookiecutter.use_postgres == "yes" %}. Two of them — the database DSN and the token signing key — are generated here and have a version the moment the stack applies{% endif %} | free |
| `service` | {% if cookiecutter.include_frontend == "yes" %}two Cloud Run services — the API and the nginx container serving the built bundle — both scaling{% else %}one Cloud Run service, scaling{% endif %} to zero{% if cookiecutter.use_postgres == "yes" %}, plus a migration job{% endif %} | nothing while idle |
{%- if cookiecutter.use_postgres == "yes" %}
| `database` | Cloud SQL Postgres 16, shared-core, 10 GB HDD, zonal, 7 daily backups | **the only standing charge** |
{%- endif %}

{% if cookiecutter.use_postgres == "yes" -%}
Everything except the database is free at rest: Cloud Run scales to zero, and an idle environment
costs nothing. Cloud SQL has no scale-to-zero, so the database bills whether or not anyone uses it.

### The database settings are already the cheapest ones that work

That is the question worth answering before you go looking for a knob, so: there isn't one. Every
setting below is at its minimum, and each is at its minimum for a reason you can check.

| Setting | Value | Why nothing is lower |
| --- | --- | --- |
| `databaseTier` | `db-f1-micro` | Cloud SQL offers exactly two shared-core machine types, `db-f1-micro` and `db-g1-small`. This is the smaller one. Shared core is Cloud SQL Enterprise edition only. |
| `databaseDiskGb` | `10` | 10 GB is Cloud SQL's minimum instance storage and also its default. `disk_autoresize` is on, so it grows when it has to — and never shrinks back, which is why starting at the floor matters. |
| `disk_type` | `PD_HDD` | The cheaper of the two storage types per GB, and offered on Enterprise edition only. |
| `availability_type` | `ZONAL` | `REGIONAL` is high availability: a standby in a second zone, at roughly double the instance charge. |
| `edition` | `ENTERPRISE` | Enterprise Plus costs more per vCPU and does not offer shared-core machines at all. |
| `point_in_time_recovery_enabled` | off outside production | It is driven by `is_production`, and PITR keeps write-ahead logs, which are billed storage. |

**What the floor costs you, stated plainly:** shared-core instances and single-zone instances are
both excluded from the Cloud SQL SLA. `db-f1-micro` with no standby is a development database with
a real backup schedule, not a database anybody has promised will stay up. Moving off the floor for
production means `availability_type="REGIONAL"` and a dedicated-core tier, in that order.

Two things are below this and both buy the saving with something:

- **Fewer backups, or none.** `retained_backups=7` is Cloud SQL's own default for Enterprise
  edition; the valid range is 1 to 365 and automated backups can be switched off entirely. Backup
  storage is billed, so this is the only remaining lever — and it is your recovery window.
- **Stopping the instance.** Stopping suspends the instance charge, but storage and IP charges
  continue. It is cheaper, not free, and the service is down while it lasts.

The real zero is `just infra-destroy` (and then `./infra/bootstrap/teardown.sh` — read the
teardown order above first). There is no configuration of Cloud SQL that costs nothing.

**No figure is quoted here on purpose.** The standing charge is three line items — the shared-core
machine, 10 GB of HDD, and backup storage — and each is priced per region and changes without this
file changing. Price those three in the Google Cloud pricing calculator for
`{{ cookiecutter.gcp_region }}` before you apply, then read the real number off your first invoice.
A figure copied into a README is wrong somewhere and out of date everywhere.

(Machine types, storage minimum, HDD availability, backup retention range and stopped-instance
billing are from Google's Cloud SQL for PostgreSQL documentation and the `gcloud sql instances
create` reference, read 2026-09-21; the SLA exclusions are from the Cloud SQL SLA effective
2025-10-03.)
{%- else -%}
Everything in this stack is free at rest. Cloud Run scales to zero and an idle environment costs
nothing; you pay for requests, image storage, and egress.
{%- endif %}

## The two environments, which are not the same environment

There are two settings in this repository called some version of "environment", they are set
independently, and reading them as one thing is the most common way a first deploy surprises
its author.

| | Where it is set | What it does | Value in the `dev` stack |
| --- | --- | --- | --- |
| **The stack's** | `{{ cookiecutter.project_slug }}:environment` in `Pulumi.dev.yaml` | Names resources, and nothing else. `api-dev`, `{{ cookiecutter.project_slug }}-run-dev`, `{{ cookiecutter.project_slug }}-database-url-dev`. It is also what `is_production` reads to decide deletion protection{% if cookiecutter.use_postgres == "yes" %} and point-in-time recovery{% endif %}. The application never sees it. | `dev` |
| **The application's** | `{{ cookiecutter.project_slug }}:appEnvironment` in `Pulumi.dev.yaml` | Becomes `ENVIRONMENT` on the Cloud Run revision. One of `local`, `test`, `staging`, `production` — the literal in `backend/app/core/config.py`. It decides how the service *behaves*: `production` serves no `/docs` and no `/openapi.json`. | `staging` |

They are separate because they vary separately. A stack called `dev` that the whole company can
reach is production as far as the application is concerned; a stack called `prod` standing up a
customer demo is not. Tying one to the other would force a choice between a resource-naming scheme
and a behaviour switch.

`staging` is the default because it is the honest reading of a first deploy: production-shaped
logging, a placeholder database password still refused, and `/docs` reachable — which is the first
thing anybody does with a URL they have just been given. **Set `appEnvironment` to `production` in
the stack that real users reach.** Anything outside the four values is refused by `config.py` when
the stack loads, rather than by a container exiting several minutes into an apply.

## Who owns what

The stack owns the *shape* of the Cloud Run resource — service account, scaling, secret wiring,
probes{% if cookiecutter.use_postgres == "yes" %}, the Cloud SQL attachment{% endif %}. The CD
pipeline owns exactly one field: the image tag. The image is declared with `ignore_changes`, and
every preview and apply passes `--refresh`.

Both halves are needed. `ignore_changes` suppresses the diff between the program and Pulumi's
state; it does not tell state that CD changed the image. Without `--refresh`, program and state
both still say `hello`, there is no diff to ignore, and the next apply of any unrelated field sends
the whole template — stale image included. The `just infra-*` recipes are where that is enforced.

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
| Cloud Run service{% if cookiecutter.include_frontend == "yes" %}s{% endif %}, the registry, the identities, the Workload Identity pool, the secret containers{% if cookiecutter.use_postgres == "yes" %}, the database, the migration job{% endif %} | `pulumi/__main__.py` |
| Every repository variable the pipelines read | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| Removing the state bucket and the KMS key, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| Backend image build{% if cookiecutter.use_postgres == "yes" %}, migration{% endif %} and deploy | `.github/workflows/cd.yml` |
{%- if cookiecutter.include_frontend == "yes" %}
| Frontend image build and deploy | `.github/workflows/frontend-cd.yml` |
| The API origin compiled into the frontend bundle | `SERVICE_URL`, exported by the stack and synced by the script |
| The browser origin the backend's CORS allowlist accepts | the frontend service's URL, wired in `pulumi/__main__.py` |
{%- endif %}

The stack's exports, the variables `sync-github.sh` sets and the `vars.` names the workflows read
have to agree exactly, so a unit test in `pulumi/tests` asserts that they do. A name changed in one
of the three is then a failing build rather than a deploy job that is skipped forever and never
says why.

Manual, and no template can change that:

| | Why it cannot be shipped |
| --- | --- |
| A GCP project with a billing account attached | It costs money and names a payer |
| `./bootstrap/state-bucket.sh`, then `pulumi stack init` | A stack cannot create the bucket that holds its own state |
| Reading `just infra-preview` before the first apply | The point of the step is that a human looked |
| A value inside every empty Secret Manager container | A secret value must never exist in this repository |
| `SENTRY_ORG`, `SENTRY_PROJECT`, `SENTRY_AUTH_TOKEN` | They identify an account outside this project |
| A custom domain, its DNS records and its certificate | You own the name; nothing here can |
| Confirming `just infra-teardown`, and the KMS commands it leaves for 30 days' time | Deleting a state bucket is irreversible, and KMS will not go faster |
{%- if cookiecutter.include_frontend == "yes" %}
| `FRONTEND_API_URL`, once the API answers on that domain | The same decision, one variable later — it overrides `SERVICE_URL` |
{%- endif %}
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

## What a human has to do, and what no script can

| | Who | Where |
| --- | --- | --- |
| Install and authenticate `gcloud` and `pulumi` | you | your machine |
| Create the GCP project and attach a billing account | you | console or `gcloud` |
| Run the bootstrap script | you, once | `./infra/bootstrap/state-bucket.sh` |
| `pulumi stack init` with the KMS secrets provider | you, once | the bootstrap script prints the command |
| Read the preview before the first apply | you | `just infra-preview` |
| Populate every empty Secret Manager container | you | `gcloud secrets versions add` |{% if cookiecutter.use_postgres == "yes" %}
| Nothing, for `jwt-secret` | the stack | 48 random characters, minted and mounted; never typed |{% endif %}
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
`GCP_PROJECT`, the same variable `state-bucket.sh` reads.

A second **service** in the same project shares the bucket too, and that one is not a stack this
program can enumerate: it is another Pulumi project with its own directory in the backend. Tearing
down either service leaves the other's state where it is — `teardown.sh` refuses on the directory
listing — so the bucket and the key outlive both, and the last one out deletes them. Set both environment keys in the
new file: `environment`
for the names, `appEnvironment` for how the service behaves. A `prod` stack that keeps the default
`staging` is a production service serving its own OpenAPI document.

A second service is one more `Service(...)` in `__main__.py` with its own runtime service account.
Give it its own account rather than reusing this one: a service that holds another service's roles
turns any compromise of the least protected surface into database and Secret Manager access.
{% if cookiecutter.include_frontend == "yes" -%}
The frontend is the worked example — a different port, a different probe, no database, no secrets
and an account of its own, all of it arguments to the same component. Deploying a third is that
plus a workflow modelled on `frontend-cd.yml`, an export, and a line in `sync-github.sh`; the test
that asserts those three agree is what tells you when you have done two of the three.
{%- endif %}

## Related

- Ask Athena for the **Pulumi standards** and the **Cloud Run service standards** — they are the
  graded rules this stack is built to satisfy, and they are what a reviewer will check it against.
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi GCP provider — https://www.pulumi.com/registry/packages/gcp/
- Cloud Run container runtime contract — https://cloud.google.com/run/docs/container-contract
