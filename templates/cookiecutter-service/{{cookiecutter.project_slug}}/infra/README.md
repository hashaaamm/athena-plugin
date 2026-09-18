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

Step 7 is separate because secret *values* must never appear in infrastructure code, in a stack
configuration file, or in state. The stack creates the empty container and grants access to it;
what goes inside is added out of band. Ask Athena for the Pulumi standards for the graded rule.

Step 9 is last because the service exists before the image does. `pulumi up` creates a Cloud Run
service on Google's public `hello` container, Cloud Run assigns it a URL, and only then can a
pipeline push a real image at it. That ordering matters more than it looks: anything that compiles
its own origin into a build artefact — a client bundle with an API base URL in it, a canonical URL
in a sitemap — cannot be built correctly until the URL exists, so the sequence is placeholder
image, then URL into a repository variable, then the real build.

## Layout

```
infra/
├── bootstrap/state-bucket.sh   plain gcloud; creates the bucket Pulumi stores state in
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
| `identities` | a runtime service account, a CI service account, and a Workload Identity pool scoped to one repository | free |
| `secrets` | Secret Manager containers, and the IAM to read them, granted per secret | free |
| `service` | one Cloud Run service, scaling to zero{% if cookiecutter.use_postgres == "yes" %}, plus a migration job{% endif %} | nothing while idle |
{%- if cookiecutter.use_postgres == "yes" %}
| `database` | Cloud SQL Postgres 16, shared-core, 10 GB HDD, zonal, 7 daily backups | **the only standing charge** |
{%- endif %}

{% if cookiecutter.use_postgres == "yes" -%}
The database bills whether or not anyone uses it, so price `db-f1-micro` with 10 GB in
`{{ cookiecutter.gcp_region }}` before you apply. Everything else in this stack is free at rest:
Cloud Run scales to zero, and an idle environment costs nothing.
{%- else -%}
Everything in this stack is free at rest. Cloud Run scales to zero and an idle environment costs
nothing; you pay for requests, image storage, and egress.
{%- endif %}

## Who owns what

The stack owns the *shape* of the Cloud Run resource — service account, scaling, secret wiring,
probes{% if cookiecutter.use_postgres == "yes" %}, the Cloud SQL attachment{% endif %}. The CD
pipeline owns exactly one field: the image tag. The image is declared with `ignore_changes`, and
every preview and apply passes `--refresh`.

Both halves are needed. `ignore_changes` suppresses the diff between the program and Pulumi's
state; it does not tell state that CD changed the image. Without `--refresh`, program and state
both still say `hello`, there is no diff to ignore, and the next apply of any unrelated field sends
the whole template — stale image included. The `just infra-*` recipes are where that is enforced.

## What a human has to do, and what no script can

| | Who | Where |
| --- | --- | --- |
| Install and authenticate `gcloud` and `pulumi` | you | your machine |
| Create the GCP project and attach a billing account | you | console or `gcloud` |
| Run the bootstrap script | you, once | `./infra/bootstrap/state-bucket.sh` |
| `pulumi stack init` with the KMS secrets provider | you, once | the bootstrap script prints the command |
| Read the preview before the first apply | you | `just infra-preview` |
| Populate every empty Secret Manager container | you | `gcloud secrets versions add` |
| Set the repository variables | `sync-github.sh` | `just infra-sync-github` |
| Set `SENTRY_ORG`, `SENTRY_PROJECT` and the `SENTRY_AUTH_TOKEN` secret | you | GitHub settings |
| Protect `main`, and require CI before merge | you | GitHub settings |

Do not create a service-account JSON key for any of this. You authenticate as yourself; CI
authenticates by exchanging GitHub's OIDC token, which is what the Workload Identity pool exists
for. A downloaded key is a long-lived credential in a file and the handbook bans them.

## Adding a second environment, or a second service

A second environment is a new `Pulumi.<stack>.yaml` and a second `state-bucket.sh` run against its
project — not a second program. Every resource name already carries the environment, so two stacks
can share one project without colliding.

A second service is one more `Service(...)` in `__main__.py` with its own runtime service account.
Give it its own account rather than reusing this one: a service that holds another service's roles
turns any compromise of the least protected surface into database and Secret Manager access.

## Related

- Ask Athena for the **Pulumi standards** and the **Cloud Run service standards** — they are the
  graded rules this stack is built to satisfy, and they are what a reviewer will check it against.
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi GCP provider — https://www.pulumi.com/registry/packages/gcp/
- Cloud Run container runtime contract — https://cloud.google.com/run/docs/container-contract
