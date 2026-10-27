# Infrastructure

Pulumi in Go, the service's own language. One component per concern, in
`pulumi/internal/components/`, composed in `pulumi/main.go`, which reads top to bottom as a
dependency graph.

Nothing here has been applied yet. A freshly generated repository has a stack that compiles and
unit-tests without any cloud access, and has never created a resource. Everything below is what
turns that into a running service on AWS — ECS Express Mode{% if cookiecutter.use_postgres == "yes" %} and RDS for PostgreSQL{% endif %} — in the order it has
to happen.

## What runs where

Unlike `backend/`, whose recipes all run in containers, **every recipe in `infra/` runs on your
machine.** Pulumi compiles a Go program with the Go toolchain of whoever runs `pulumi`, and
`pulumi`, `aws` and `gh` act with your own credentials; putting them in a container would mean
mounting those credentials into it. So this directory, and only this directory, has host
prerequisites:

| Tool | Needed for | Check |
| --- | --- | --- |
| Go {{ cookiecutter.go_version }} or newer | `just infra-test`, and every `pulumi` command (it compiles `main.go`) | `go version` |
| `golangci-lint` v2 | `just infra-lint` | `golangci-lint version` |
| `pulumi` CLI | preview, up, destroy, stack outputs | `pulumi version` |
| `aws` CLI v2 | the bootstrap and teardown scripts, and your own authentication | `aws --version` |
| `gh` | `just infra-sync-github` | `gh auth status` |
| `python3` | `just infra-teardown` only (it reads JSON from `aws` and `pulumi`) | `python3 --version` |

Each recipe checks for the tools it uses and names the missing one. `just infra-test` needs only
Go: it runs against mocks, with no cloud account at all.

`go.sum` is not templated, for the same reason `backend/go.sum` is not: a checksum file frozen when
the template was written is stale on the day it ships. The first command is `cd infra && just lock`,
and its result is committed. CI runs `go mod tidy -diff` and fails on a `go.sum` that is missing or
out of date.

## The order, and why it cannot change

```
0. cd infra && just lock                           resolves go.sum; commit it
1. aws + pulumi installed and authenticated        nothing is created
2. an AWS account for this environment             the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain aws CLI; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
   pulumi config set githubSubjectPrefix …         the OIDC subject GitHub issues for this repository
5. just infra-preview                              read it, by a human
6. just infra-up                                   creates the service on a placeholder image
{%- if cookiecutter.use_postgres == "yes" %}
7. just infra-secrets                              generates the signing key the stack leaves empty
8. just infra-up                                   again: mounts the version step 7 recorded
{%- if cookiecutter.use_sentry == "yes" %}
   (optional) aws secretsmanager put-secret-value  the Sentry DSN, which a human supplies
{%- endif %}
9. just infra-sync-github                          copies the stack's outputs into repository variables
10. merge to main                                  cd.yml is no longer inert; the real image deploys
{%- else %}
{%- if cookiecutter.use_sentry == "yes" %}
7. aws secretsmanager put-secret-value …           optional: the Sentry DSN, which a human supplies
8. just infra-sync-github                          copies the stack's outputs into repository variables
9. merge to main                                   cd.yml is no longer inert; the real image deploys
{%- else %}
7. just infra-sync-github                          copies the stack's outputs into repository variables
8. merge to main                                   cd.yml is no longer inert; the real image deploys
{%- endif %}
{%- endif %}
```

Step 3 is a script rather than a stack because a stack cannot create the bucket that holds its own
state — the first `pulumi up` would need somewhere to write before that somewhere exists. It creates
three things, all of them shared by every service in the account and region: the state bucket
`pulumi-state-{{ cookiecutter.aws_account_id }}-{{ cookiecutter.aws_region }}`, the KMS key
`alias/pulumi-state` that encrypts it and every stack's secrets, and the GitHub Actions OIDC
provider the CI roles trust. The third is there for the same reason as the first two: IAM allows one
provider per issuer per account, so it is not one service's to own, and a stack that created it
would delete it from under every other service on its own `destroy`. The stack looks it up instead.
The script refuses to run when your credentials are for any account other than
`{{ cookiecutter.aws_account_id }}`.

State locking needs no DynamoDB table. Pulumi's self-managed backend writes a lock object under
`.pulumi/locks/` in the same bucket for the length of every update, and refuses to start one while
another update's lock is there.

The second line of step 4 is there because GitHub decides the shape of the `sub` claim in this
repository's OIDC tokens, and the CI roles trust exactly one. Many repositories now get the
immutable form, `repo:<owner>@<owner-id>/<repo>@<repo-id>`, so that a renamed or re-registered
account cannot inherit the trust; a role written for the name-only `repo:<owner>/<repo>` then admits
no token, and every CD run fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity".
Ask the repository, after it exists on GitHub:

```bash
pulumi -C infra/pulumi config set githubSubjectPrefix \
  "$(gh api repos/{{ cookiecutter.github_repository }}/actions/oidc/customization/sub --jq '.sub_claim_prefix // empty')"
```

Empty is the name-only form. `just infra-sync-github` makes the same request and refuses to un-gate
CD while the stack trusts anything else, printing the command that fixes it.

Step 6 asks for confirmation and **refuses to run with nothing there to confirm** — "--yes must be
passed in to proceed when running in non-interactive mode". The recipe does not pass `--yes` for
you; this command creates billable resources and a human reading the preview is the point. From a
pipeline, or from an agent with no terminal, type it: `just infra-up dev --yes`. The stack name is
positional, so the flag has to follow one.

Secret *values* never appear in infrastructure code, in a stack configuration file, or in state —
not even a value the stack could have generated itself, because a generated value is in state like
any other. Ask Athena for the Pulumi standards (MUST-3); ADR-0023 records how this stack meets it.
The stack creates each Secrets Manager secret empty and mounts it only once its `VersionId` — which
is not a secret — is in `Pulumi.<stack>.yaml`.
{%- if cookiecutter.use_postgres == "yes" %}

Step 7 is the out-of-band half, and it is a script rather than a stack resource for exactly that
reason. `just infra-secrets` (`scripts/secrets.sh`) generates the HS256 signing key — 64 random
characters — pipes it into the `jwt-secret` secret, and records the `VersionId` as
`jwtSecretVersion`. The key is never printed, written to a file or passed as a command-line
argument. Run it again and it keeps what is recorded; `just infra-secrets dev --rotate` replaces it,
and **rotating signs every user out**: every token was signed with the old key.

The database needs nothing from step 7. The service logs in with an IAM token, and the migration
task as the master user, whose password RDS itself generates, keeps and rotates — see
[Two database identities](#two-database-identities).

Step 8 is the same `up` again: it is the one that mounts the version step 7 recorded. Until then
the stack exports `secrets_ready=false`, and step 9 refuses to un-gate CD — the image CD would
deploy refuses to start without its signing key.
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

The Sentry DSN is the one value a human adds, and it is optional: the secret stays empty and
unmounted until you put a value — from stdin, `--secret-string file:///dev/stdin` — and record the
`VersionId` that `put-secret-value` prints as `sentryDsnVersion`, then run `just infra-up` again.
{%- endif %}

Merging is last because the service exists before the image does. ECR is empty when the stack is
first applied, so `pulumi up` creates the Express service on Google's public `hello` container — a
few megabytes, anonymous pull, answers 200 on every path — Express Mode assigns it an HTTPS
endpoint, and only then can a pipeline push a real image at it. That ordering matters more than it
looks: anything that compiles its own origin into a build artefact — a client bundle with an API
base URL in it — cannot be built correctly until the URL exists, so the sequence is placeholder
image, then URL into a repository variable, then the real build. A browser client relies on
exactly that; see [Browser clients](#browser-clients).

### Taking it down, which is not one command

```
1. just infra-destroy dev                 removes every resource the stack created
2. just infra-teardown                    removes the three things the stack never created
                                          (refuses when another project shares the backend)
3. nothing, 30 days on                    the KMS key finishes deleting on its own
```

{% if cookiecutter.use_postgres == "yes" -%}
`destroy` takes the `jwt-secret` secret with it, so a stack brought back up needs step 7 again. The
database's master secret goes with the instance: RDS deletes the secret it manages when it deletes
the instance. The final-snapshot and automated-backup behaviour is unchanged — none outside `prod`,
a final snapshot in it.

{% endif -%}
**`pulumi destroy` does not undo step 3 of the setup order at the top of this page.** It removes
what the stack created and nothing else, and the state bucket, the KMS key and the OIDC provider
were created by `state-bucket.sh`, with the plain AWS CLI. Pulumi has never known about them, so no
Pulumi command will ever take them away. Destroy a stack and walk away and you are left holding a
versioned S3 bucket and a KMS key, quietly, indefinitely — which is the whole reason `teardown.sh`
exists.

**The bucket may not be yours, and that is the default rather than the edge case.**
`state-bucket.sh` derives the name from the account and region alone —
`pulumi-state-<account>-<region>`, with no per-service component — and the key alias the same way.
Generate a second service into the same account and region and it bootstraps onto the same bucket
and the same key, by construction: the script finds them already there and reuses them. One backend
then holds several Pulumi projects, a directory each under `.pulumi/stacks/`, and deleting the
bucket deletes all of their state. So `teardown.sh` lists that prefix first and refuses when it
finds a directory this project does not own, printing the `pulumi stack rm --remove-backups` path
instead — that is the Pulumi standards' MUST-8; ask Athena for it. A listing it cannot read counts as
shared. The OIDC provider is shared across the whole account, so it is deleted only when no IAM role
still trusts it; otherwise the script names the roles that do and keeps it.

The order is the second trap. The bucket holds the state `pulumi destroy` reads, so deleting the
bucket first strands the stack: every resource it created still exists, still bills, and the only
record of what those resources are has just been deleted. `teardown.sh` will not let that happen by
accident either — it exports every stack in the backend, `--all` rather than only this Pulumi
project's, and refuses while any of them still holds a resource. Then it asks you to type the
bucket name, and refuses outright when nothing can answer, for the same reason `just infra-up`
refuses a non-interactive apply. There is deliberately no `--yes` here: an unattended teardown of a
state bucket is not something this repository will do on your behalf.

Step 3 is a wait because **a KMS key cannot be deleted on demand.** `schedule-key-deletion` puts it
in `PendingDeletion` for a waiting period of 7 to 30 days — the script uses 30 — and nothing can use
it meanwhile. Until the date it prints, `aws kms cancel-key-deletion` reverses it. The script also
deletes the alias, so a later bootstrap creates a fresh key rather than finding this one pending.

The bucket, by contrast, is gone outright: S3 keeps no soft-deleted copy, and the script removes
every object version and delete marker before it deletes the bucket.

`destroy` also leaves the GitHub repository variables behind, naming infrastructure that no longer
exists. Delete them in the repository settings, or run `gh variable delete <NAME>` for each one
`sync-github.sh` set.

If the AWS account exists only for this service, closing the account ends every charge at once and
is the cleanest teardown there is.

## Layout

```
infra/
├── justfile                    test lint preview up{% if cookiecutter.use_postgres == "yes" %} secrets{% endif %} sync-github destroy teardown lock
├── bootstrap/state-bucket.sh   plain aws CLI; the state bucket, its KMS key, the GitHub OIDC provider
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
{%- if cookiecutter.use_postgres == "yes" %}
├── scripts/secrets.sh          generates the signing key the stack leaves empty; records its version
{%- endif %}
├── scripts/sync-github.sh      copies stack outputs into GitHub repository variables; checks secrets_ready
└── pulumi/
    ├── Pulumi.yaml             project, runtime, and the S3 backend URL
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
test. **`Service` describes an Express service, not this app**: the port, the probe path, the roles,
the secrets and the size are all fields of `ServiceArgs` with usable zero values. A second service —
a worker, an admin API — is one more `components.NewService` call in `main.go`, not a copied file
with a different string in it.

## What the stack provisions

| Component | Resources | Cost posture |
| --- | --- | --- |
| `network` | a VPC, an internet gateway, a public and an isolated subnet per AZ (two by default), and two security groups. **No NAT gateway** | free; public IPv4 addresses are billed per hour |
| `registry` | one ECR repository, tags immutable, scanned on push, keeping the newest 30 images | pennies |
| `logs` | the CloudWatch log groups every container writes to, 30 days' retention (90 in `prod`) | pennies |
| `cluster` | the ECS cluster, Container Insights off outside `prod` | free |
| `identities` | an ECS infrastructure role, an execution role{% if cookiecutter.use_postgres == "yes" %} for the service and one for the migration task{% endif %}, the application's runtime role{% if cookiecutter.use_postgres == "yes" %} with `rds-db:connect` as its own database user{% endif %}, and two keyless GitHub roles — deploy and preview | free |
| `secrets` | empty Secrets Manager secrets, mounted at the `VersionId` recorded in stack configuration — never a value | a small monthly charge per secret |
| `service` | one Express Mode service: the Fargate task, an HTTPS Application Load Balancer, its target group, a service security group and CPU-based scaling | **standing: one task and the load balancer** |
{%- if cookiecutter.use_postgres == "yes" %}
| `database` | RDS for PostgreSQL 16, `db.t4g.micro`, 20 GB gp3, single-AZ, 7 days of backups, TLS only, IAM database authentication on, the master password managed by RDS | **standing**; RDS's master secret is a Secrets Manager charge too |
| `migrations` | the ECS task definition CD runs `/app/api migrate up` with | free; billed per run |
{%- endif %}
| `deploy_access` | what the deploy role may touch, attached once the services exist | free |

**Unlike Cloud Run, nothing here scales to zero.** Express Mode runs at least one task, and the
Application Load Balancer it creates bills by the hour from the moment it exists, whether or not
anyone calls it. That is where the money starts: step 6.

What is deliberately not here, and why:

- **No NAT gateway.** It is an hourly charge per AZ plus a charge per gigabyte, and on a small
  service it would be the largest line on the bill. Express Mode puts its load balancer and its
  tasks in the same subnets and picks the load balancer's scheme from them — public subnets, an
  internet-facing ALB — so the tasks run in public subnets with public IPs, which is also how they
  reach ECR, Secrets Manager and CloudWatch without a NAT. What keeps them unreachable is the
  security group: the app group has no ingress rule, and Express Mode's own service group admits
  only its load balancer.
- **No VPC endpoints.** Each is an hourly charge per AZ, and the public route already works.
- **No customer-managed keys** for ECR or Secrets Manager. AWS's own keys protect the data at rest
  at no charge; the IAM grant on each resource is the boundary, and it is tested.

{% if cookiecutter.use_postgres == "yes" -%}
### The database settings are the cheapest ones that work

| Setting | Value | Why nothing is lower |
| --- | --- | --- |
| `databaseInstanceClass` | `db.t4g.micro` | The smallest Graviton burstable class RDS offers for PostgreSQL. Burstable means CPU credits: sustained load past the baseline is throttled, or billed, depending on the credit mode. |
| `databaseStorageGb` | `20` | gp3's floor for RDS for PostgreSQL. Storage autoscaling is on up to 100 GB, so it grows when it has to — and never shrinks back, which is why it starts at the floor. The stack refuses a value outside 20–99 when it loads. |
| `StorageType` | `gp3` | Baseline IOPS and throughput are included in the storage price, rather than tied to volume size as on gp2. |
| `databaseMultiAz` | `false` | Multi-AZ is a synchronous standby in a second AZ, at roughly double the instance and storage charge. |
| `BackupRetentionPeriod` | `7` | Point-in-time recovery inside the window, and backup storage up to the database's size is not charged extra. One day is the minimum; zero disables automated backups. |

**What the floor costs you, stated plainly:** a single-AZ instance is down for the length of any
AZ problem and for its own maintenance, and a burstable class can run out of credits under steady
load. `db.t4g.micro` in one AZ is a development database with a real backup schedule. Moving off
the floor for production means `databaseMultiAz: "true"` and a non-burstable class, in that order.

The real zero is `just infra-destroy` (and then `just infra-teardown` — read the teardown order
above first). There is no configuration of RDS that costs nothing; a stopped instance restarts on
its own after seven days.

**No figure is quoted here on purpose.** The standing charge is the instance hour, the storage, the
Fargate task, the load balancer and the public IPv4 addresses — each priced per region and changing
without this file changing. Price them in the AWS Pricing Calculator for
`{{ cookiecutter.aws_region }}` before you apply, then read the real number off your first bill.
A figure copied into a README is wrong somewhere and out of date everywhere.

### Two database identities

| | Logs in as | With | May |
| --- | --- | --- | --- |
| **The service** | `databaseAppRole` (`app_iam` by default) | an IAM token its task role signs for every new connection — `DATABASE_AUTH=rds-iam`, `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`, `DATABASE_USER`, `AWS_REGION`. No password exists. The runtime role holds `rds-db:connect` on exactly `arn:aws:rds-db:<region>:<account>:dbuser:<resource-id>/<databaseAppRole>` | read and write rows, use sequences — nothing that changes the schema |
| **The migration task** | the master user, `app` | `DATABASE_USER` and `DATABASE_PASSWORD` from the master secret RDS manages (`manageMasterUserPassword`), resolved by ECS when the task starts | own and change the schema |

`migrate up` creates `databaseAppRole` if it is missing, makes it a member of `rds_iam`, and grants
it row privileges — **after** the migrations and on every run, so a table a migration just created
is covered, with default privileges so the next one is too.

The master secret is the one secret reference in this stack that does **not** pin a version
(`<arn>:username::` and `<arn>:password::`), and that is deliberate (ADR-0023): its owner is RDS,
which rotates it, and a pinned version would be a password the database no longer accepts after the
first rotation. ECS resolves it when the task starts, so a rotation affects only the next migration.
Only the migration task's own execution role may read it; the service's execution role cannot, and
the migration task has no task role at all. No `kms:Decrypt` grant is needed: RDS encrypts the
secret with the account's `aws/secretsmanager` key. Name a customer-managed key on the instance and
the migration execution role needs `kms:Decrypt` on it too.

Both connections are TLS. The parameter group sets `rds.force_ssl`, so a plaintext connection is
refused by the server; the service connects with `sslmode=require`, and the migration task's
`PGSSLMODE=require` makes the client refuse plaintext too. `require` rather than `verify-full`
because the RDS certificate authority is not in the distroless image's trust store, and the path
never leaves the VPC. The instance has no public endpoint, sits in isolated subnets with no route
out, and its security group admits port 5432 from the app tasks' group and nothing else.
{%- else -%}
**No figure is quoted here on purpose.** The standing charge is the Fargate task, the load
balancer and the public IPv4 addresses — each priced per region and changing without this file
changing. Price them in the AWS Pricing Calculator for `{{ cookiecutter.aws_region }}` before you
apply, then read the real number off your first bill.
{%- endif %}

## The two environments, which are not the same environment

There are two settings in this repository called some version of "environment", they are set
independently, and reading them as one thing is the most common way a first deploy surprises
its author.

| | Where it is set | What it does | Value in the `dev` stack |
| --- | --- | --- | --- |
| **The stack's** | `{{ cookiecutter.project_slug }}:environment` in `Pulumi.dev.yaml` | Names resources, and nothing else. `{{ cookiecutter.project_slug }}-dev`, `{{ cookiecutter.project_slug }}-deploy-dev`{% if cookiecutter.use_postgres == "yes" %}, `{{ cookiecutter.project_slug }}-jwt-secret-dev`{% endif %}. It is also what `IsProduction()` reads to decide{% if cookiecutter.use_postgres == "yes" %} deletion protection, the final snapshot,{% endif %} secret recovery windows, log retention and Container Insights. The application never sees it. | `dev` |
| **The application's** | `{{ cookiecutter.project_slug }}:appEnvironment` in `Pulumi.dev.yaml` | Becomes `ENVIRONMENT` on the ECS task. One of `local`, `test`, `staging`, `production` — the values `backend/internal/config` accepts. It decides how the service *behaves*: `production` serves no `/docs` and no `/openapi.json`. | `staging` |

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
own pipelines (ADR-0022) — on AWS, a private S3 bucket behind CloudFront. This backend answers the
HTTP contract that client is written against — the same routes, JSON, error envelope{% if cookiecutter.use_postgres == "yes" %},
refresh-cookie behaviour{% endif %} and OpenAPI 3 document at `/openapi.json` as the FastAPI template — so
pointing one at it is configuration on both sides and code on neither:

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

   then `just infra-preview` and `just infra-up`. The service rolls out with `CORS_ORIGINS` set to
   the list{% if cookiecutter.use_postgres == "yes" %} and `REFRESH_TRANSPORT=cookie`: the refresh token travels as an HttpOnly cookie
   rather than in storage the bundle's JavaScript can read, and refresh and logout require the CSRF
   header and a listed `Origin`{% endif %}.

`corsOrigins` is comma-separated, and each entry is an origin exactly as a browser sends it — a
scheme, a host and an optional port, no path and no trailing slash. Empty, the default, means no
browser on another origin may call the API, and `CORS_ORIGINS` is not set at all.{% if cookiecutter.use_postgres == "yes" %} `refreshTransport` is
`body` (the default, for server-to-server and mobile callers) or `cookie`, and `cookie` with no
origins is refused: the CSRF check accepts listed origins only, so every refresh would fail.{% endif %} The
stack refuses what the service would refuse at start-up, at preview rather than as a deployment
that never becomes healthy.{% if cookiecutter.use_postgres == "yes" %} Neither variable reaches the migration task; it serves no
HTTP.{% endif %}
{%- if cookiecutter.use_postgres == "yes" %}

**The one thing that does not work on the default hostnames, and why.** The refresh token is a
`SameSite=Strict` cookie, and a session that survives a reload needs the SPA and the API on one
*site*. The default hostnames are not: the client is on `cloudfront.net` and this API on Express
Mode's `on.aws` hostname, which are two sites. The browser will not send a Strict cookie from one
to the other. Signing in works, and then a reload — or the access token's fifteen minutes — signs
the user out. The fix is a name you own: put the SPA and the API on one site, say `app.example.com`
and `api.example.com`, list the SPA's origin in `corsOrigins`, and point the client's `API_URL` at
the API's name. Relaxing the cookie to `SameSite=None` is not the fix: it reopens the cross-site
request surface the Strict setting closes, and browsers that block third-party cookies drop it
anyway.
{%- endif %}

## Who owns what

The stack owns the *shape* of the Express service — size, scaling, roles, network, secret wiring
and the version each secret is pinned to, logs, CORS. The CD pipeline owns exactly one field: the
image. Secret *values* belong to neither: {% if cookiecutter.use_postgres == "yes" %}`just infra-secrets`, a human, and for the database's master
password RDS itself{% else %}a human{% endif %} put them in out of band, and the stack only records which version to mount. The image is declared with
`ignoreChanges` on `primaryContainer.image`, and every preview and apply passes `--refresh`.

Both halves are needed. `ignoreChanges` suppresses the diff between the program and Pulumi's
state; it does not tell state that CD changed the image. Without `--refresh`, program and state
both still say `hello`, there is no diff to ignore, and the next apply of any unrelated field sends
the whole primary container — stale image included. The `just infra-*` recipes are where that is
enforced, and `contract_test.go` fails if one of them loses the flag.
{%- if cookiecutter.use_postgres == "yes" %}

The migration task definition is owned differently, on purpose. Pulumi owns its shape and CD never
edits it: on every deploy CD reads the latest ACTIVE revision of the family, swaps the image,
registers that as the next revision and runs it. So there is no `ignoreChanges` there — a change
to the stack is a new revision CD picks up on its next run.
{%- endif %}

Every secret reference the stack owns pins a version: ECS resolves `<secret-arn>:::<version-id>`,
never the `AWSCURRENT` stage implicitly{% if cookiecutter.use_postgres == "yes" %} — RDS's master secret is the one exception, and the database section
says why{% endif %}. A task reads its secrets when it starts, so a stage-relative
reference would let two tasks of one deployment hold different values after a rotation. Pinned, a
new value is a new deployment, and a rollback restores the credential with the image.

Environment variables are written in name order. A Go map has no order, and a container whose
environment came out shuffled would be a diff — and a new deployment — for nothing.

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
| The network, the Express service, the registry, the roles, the secrets{% if cookiecutter.use_postgres == "yes" %}, the database, the migration task{% endif %} | `pulumi/main.go` |
| The service's CORS allowlist{% if cookiecutter.use_postgres == "yes" %} and refresh-token transport{% endif %} for a browser client, checked before they deploy | `corsOrigins`{% if cookiecutter.use_postgres == "yes" %} and `refreshTransport`{% endif %} in `Pulumi.<stack>.yaml`, read by `pulumi/internal/stackconfig` |
{%- if cookiecutter.use_postgres == "yes" %}
| The signing key, generated, stored and its version recorded — never in a file, an argument or state | `scripts/secrets.sh`, via `just infra-secrets` |
| The service's database identity and its grants | `rds-db:connect` in `pulumi/internal/components/identities.go`; the role and grants in `backend/internal/app/migrate.go`, on every `migrate up` |
{%- endif %}
| Every repository variable the pipelines read, and the refusal to set them before the secrets exist | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| The state bucket, its KMS key and the GitHub OIDC provider | `bootstrap/state-bucket.sh` |
| Removing them again, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| Backend image build{% if cookiecutter.use_postgres == "yes" %}, migration{% endif %} and deploy | `.github/workflows/cd.yml` |
| The stack's tests and a preview on every pull request | `.github/workflows/infra.yml` |

The stack's exports, the variables `sync-github.sh` sets and the `vars.` names the workflows read
have to agree exactly, so `pulumi/contract_test.go` asserts that they do, for `cd.yml` and
`infra.yml`. A name changed in one of the three is then a failing build rather than a deploy job
that is skipped forever and never says why.

Manual, and no template can change that:

| | Why it cannot be shipped |
| --- | --- |
| An AWS account per environment, with billing | It costs money and names a payer |
| `./bootstrap/state-bucket.sh`, then `pulumi stack init` | A stack cannot create the bucket that holds its own state |
| Reading `just infra-preview` before the first apply | The point of the step is that a human looked |
{%- if cookiecutter.use_postgres == "yes" %}
| Running `just infra-secrets`, then `just infra-up` again | It writes to the cloud with your credentials, and a value is generated where it is used, never in this repository |
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
| The Sentry DSN, in its secret | It comes from your Sentry account |
{%- endif %}
| Protection rules on the `production` GitHub environment | The deploy role trusts that environment; its rules are the gate on a deploy |
| `SENTRY_ORG`, `SENTRY_PROJECT`, `SENTRY_AUTH_TOKEN` | They identify an account outside this project |
| A custom domain, its DNS records and its certificate | You own the name; nothing here can |
| A browser client's origin in `corsOrigins` | It is another repository's output, and that repository is deployed after this one |
| Confirming `just infra-teardown` | Deleting a state bucket is irreversible |
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

## What a human has to do, and what no script can

| | Who | Where |
| --- | --- | --- |
| Install Go, `golangci-lint`, `pulumi`, `aws` and `gh`, and authenticate the last three | you | your machine |
| Resolve and commit `infra/pulumi/go.sum` | you, once | `cd infra && just lock` |
| Create the AWS account and attach billing | you | AWS Organizations or the console |
| Run the bootstrap script | you, once per account | `./infra/bootstrap/state-bucket.sh` |
| `pulumi stack init` with the KMS secrets provider | you, once | the bootstrap script prints the command |
| Read the preview before the first apply | you | `just infra-preview` |
{%- if cookiecutter.use_postgres == "yes" %}
| Generate the signing key and record its VersionId | you, once per stack; again with `--rotate` | `just infra-secrets`, then `just infra-up` |
| Nothing, for the database's passwords | RDS, and IAM | the master password is RDS's; the service has none |
{%- endif %}{% if cookiecutter.use_sentry == "yes" %}
| Put the Sentry DSN and record its VersionId | you | `aws secretsmanager put-secret-value … --secret-string file:///dev/stdin`, `sentryDsnVersion` in `Pulumi.<stack>.yaml`, then `just infra-up` |{% endif %}
| Set the repository variables | `sync-github.sh` | `just infra-sync-github` |
| Protect the `production` environment | you | GitHub settings |
| Set `SENTRY_ORG`, `SENTRY_PROJECT` and the `SENTRY_AUTH_TOKEN` secret | you | GitHub settings |
| Protect `main`, and require CI before merge | you | GitHub settings |
| Tear the stack down, when it is time | you | `just infra-destroy`, then `just infra-teardown` |

Do not create an IAM user or an access key for any of this. You authenticate as yourself — SSO, or
whatever your organisation uses; CI authenticates by exchanging GitHub's OIDC token, which is what
the OIDC provider and the two CI roles exist for. A downloaded access key is a long-lived credential
in a file and the handbook bans them.

The two CI roles are not interchangeable. **Deploy** trusts a token from the `production`
environment of `{{ cookiecutter.github_repository }}` and can push to this stack's repositories,
roll this stack's services{% if cookiecutter.use_postgres == "yes" %}, run this stack's migration task{% endif %} and pass this stack's roles to ECS — nothing else.
**Preview** trusts a token from a pull request in the same repository and can read the account
(`ReadOnlyAccess`) and this project's state, write only this project's lock objects, and read no
secret value at all — the stack writes none, so a refresh reads none.

## Adding a second environment, or a second service

**One AWS account per environment is the recommendation**, and production in an account of its
own. An account is the strongest boundary AWS has: a `dev` credential that cannot see the
production account cannot damage it. A second environment is then a new `Pulumi.<stack>.yaml` with
its own `accountId`, and a `state-bucket.sh` run against that account (set `AWS_ACCOUNT_ID` and
`AWS_REGION`, the same variables `teardown.sh` reads) — not a second program. The new account gets
its own bucket, its own key and its own OIDC provider. `Pulumi.yaml`'s backend URL names *this*
account's bucket, and `PULUMI_BACKEND_URL` overrides it (checked with pulumi v3.263), so work on the
other account's stack with `PULUMI_BACKEND_URL=s3://pulumi-state-<account>-<region>?region=<region>`
set — and give its preview job the same variable.
Set both environment keys in the new file: `environment` for the names, `appEnvironment` for how
the service behaves. A `prod` stack that keeps the default `staging` is a production service
serving its own OpenAPI document.

Two stacks *can* share one account — every resource name carries the environment — and then they
share one bucket, which is why `teardown.sh` checks every stack in the backend, `pulumi stack ls
--all`, rather than the one you name.

A second **service** in the same account shares the bucket too, and that one is not a stack this
program can enumerate: it is another Pulumi project with its own directory in the backend. Tearing
down either service leaves the other's state where it is — `teardown.sh` refuses on the directory
listing — so the bucket, the key and the OIDC provider outlive both, and the last one out deletes
them. Each stack creates its own VPC, so two services in one account share no network either —
and the default quota is five VPCs per region, which a busy account reaches sooner than expected.

A second Express service in this stack is one more `components.NewService` call in `main.go` with
its own execution role. Give it its own role rather than reusing this one: a service that holds
another service's grants turns any compromise of the least protected surface into database and
Secrets Manager access. A different probe, no task role and no secrets are all fields of the same
`ServiceArgs`. Deploying it is that plus a workflow modelled on `cd.yml`, an export, a row in
`sync-github.sh`, and its ARN in the deploy role's grant; `contract_test.go` is what tells you when
you have done some of it. A browser client is not one of these: it is its own repository and its
own stack, generated from `cookiecutter-react-app`.

The application's own AWS identity is the **runtime role**, exported as `runtime_role_arn`. It
holds nothing today. When the application needs a bucket, a queue or a model API, the grant goes
on that role — an `iam.RolePolicy` in `identities.go`, scoped to the one resource — never on the
execution role, which is ECS's and acts before the container starts.

## Related

- Ask Athena for the **Pulumi standards** — the graded rules this stack is built to satisfy, and
  what a reviewer will check it against — and for the decision on where Pulumi state lives for an
  AWS target (ADR-0021).
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi AWS provider (Go SDK `github.com/pulumi/pulumi-aws/sdk/v7`) — https://www.pulumi.com/registry/packages/aws/
- Pulumi unit testing in Go (`pulumi.WithMocks`) — https://www.pulumi.com/docs/iac/concepts/testing/unit/
- Amazon ECS Express Mode — https://docs.aws.amazon.com/AmazonECS/latest/developerguide/express-service-overview.html
- GitHub Actions OIDC in AWS — https://docs.github.com/en/actions/security-for-github-actions/security-hardening-your-deployments/configuring-openid-connect-in-amazon-web-services
