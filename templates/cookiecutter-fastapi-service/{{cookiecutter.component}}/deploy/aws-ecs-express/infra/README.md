# Infrastructure

Pulumi in Python, deploying to **AWS: ECS Express Mode**{% if cookiecutter.use_postgres == "yes" %} and **RDS for PostgreSQL**{% endif %}.
One component per concern, composed in `pulumi/stack.py`, which reads top to bottom as a
dependency graph.

Nothing here has been applied yet. A freshly generated repository has a stack that type-checks and
unit-tests without any cloud access, and has never created a resource. Everything below is what
turns that into a running service, in the order it has to happen.

## The order, and why it cannot change

Every command below runs from this component's folder, `{{ cookiecutter.component }}/`; from the repository root, put the
component's name in front: `just {{ cookiecutter.component }} infra-up`.

```
    aws, pulumi and docker installed, a session …    nothing is created
    an AWS account with billing                     the first thing that can cost money
 1. ./infra/bootstrap/state-bucket.sh               plain AWS CLI; a stack cannot create its own state store
 2. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 1
 3. pulumi config set --plaintext githubSubjectPrefix …   the OIDC subject GitHub issues for this repository
 4. just infra-up                                   everything but the service — read `just infra-preview` first
{%- if cookiecutter.use_postgres == "yes" %}
 5. just infra-secrets                              generates the signing key, stores it, pins its version
{%- else %}
 5. (nothing)                                       no database, so no secret the stack generates
{%- endif %}
 6. just infra-first-deploy                         the real image, built here; {% if cookiecutter.use_postgres == "yes" %}the migrations; {% endif %}the service live
 7. just infra-sync-github                          copies the stack's outputs into repository variables
    protect the `infrastructure` environment        required reviewers, main only — GitHub settings
 8. push to main                                    CD owns the image; {{ cookiecutter.component }}-infra.yml applies every infra change
```

Steps 1 to 7 run from your machine, once per environment. Every infrastructure change after them
merges and is applied by `{{ cookiecutter.component }}-infra.yml` — see
[After setup, CI applies every change](#after-setup-ci-applies-every-change).

The session in the first line is one on **account {{ cookiecutter.aws_account_id }}**, region
`{{ cookiecutter.aws_region }}` — `aws sso login`, or whatever your organisation uses. Both the
bootstrap script and the stack refuse any other account: the script checks
`aws sts get-caller-identity`, and the provider is configured with `allowed_account_ids`, so a shell
pointed at last week's client fails before it plans anything.

Step 1 is a script rather than a stack because a stack cannot create the bucket that holds its own
state — the first `pulumi up` would need somewhere to write before that somewhere exists. It creates
three account-level things, all shared by every service generated into this account and region:

| | Name | Why it is the script's and not the stack's |
| --- | --- | --- |
| State bucket | `pulumi-state-{{ cookiecutter.aws_account_id }}-{{ cookiecutter.aws_region }}` | Holds every stack's state. Versioned, Block Public Access on all four settings, owner-enforced (no ACLs), SSE-KMS with an S3 Bucket Key, TLS-only by bucket policy, old versions expired after 90 days |
| KMS key | `alias/pulumi-state` | Encrypts the bucket and every stack's secrets — the stack's secrets provider is `awskms://alias/pulumi-state?region={{ cookiecutter.aws_region }}`. Symmetric, yearly rotation on |
| GitHub OIDC provider | `token.actions.githubusercontent.com` | One per account, trusted by every service's CI roles. A stack that owned it would delete it from under its neighbours on `destroy` — the ownership problem ADR-0013 describes for the bucket — so the stack only looks it up |

Step 3 is there because GitHub decides the shape of the `sub` claim in this
repository's OIDC tokens, and the CI roles trust exactly one. Many repositories now get the
immutable form, `repo:<owner>@<owner-id>/<repo>@<repo-id>`, so that a renamed or re-registered
account cannot inherit the trust; a role written for the name-only `repo:<owner>/<repo>` then admits
no token, and every CD run fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity".
Ask the repository, after it exists on GitHub:

```bash
uv run --directory infra/pulumi pulumi config set --plaintext githubSubjectPrefix \
  "$(gh api repos/{{ cookiecutter.github_repository }}/actions/oidc/customization/sub --jq '.sub_claim_prefix // empty')"
```

`--plaintext`, because a subject prefix is an identifier rather than a secret, and the CLI asks
which one a value is when it cannot tell. Empty is the name-only form. `just infra-sync-github` makes the same request and refuses to un-gate
CD while the stack trusts anything else, printing the command that fixes it.

There is **no DynamoDB lock table**. Pulumi's self-managed backend locks with objects of its own
under `.pulumi/locks/` in the bucket, so two concurrent `up`s against one stack refuse each other.

Step 4 asks for confirmation and **refuses to run with nothing there to confirm** — "--yes must be
passed in to proceed when running in non-interactive mode". The recipe does not pass `--yes` for
you; this command creates billable resources and a human reading the preview is the point. From a
pipeline, or from an agent with no terminal, type it: `just infra-up dev --yes`. The stack name is
positional, so the flag has to follow one.

Step 5 is separate because a secret *value* must never appear in infrastructure code, in a stack
configuration file, or in state — Pulumi standards MUST-3; ADR-0023 records how this stack meets it.
The stack creates empty secrets; what goes inside is added out of band, and its `VersionId` goes
into stack configuration so the service pins exactly that version.
{%- if cookiecutter.use_postgres == "yes" %}
`just infra-secrets` does it for the token signing key: generated with `openssl rand`, handed to
`aws secretsmanager put-secret-value` on stdin and never printed, its `VersionId` pinned with
`pulumi config set --plaintext jwtSecretVersion`. Run it again and it keeps what is pinned; `--rotate` makes a
new version, and **a new signing key signs every user out**. Until it is pinned the service
references nothing, the stack exports `secrets_ready: "false"`, and steps 6 and 7 refuse to go on:
a service with no signing key does not start. Step 6's apply is the one that references it.

The database needs nothing here. The service logs in with IAM database authentication and has no
password; the migration task logs in as the master user, whose password RDS itself generates, keeps
in a secret it owns, and rotates.
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

A Sentry DSN is the one a person adds — `aws secretsmanager put-secret-value --secret-string
file:///dev/stdin`, the value on stdin — then sets `sentryDsnVersion` to the `VersionId` and applies.
{%- endif %}

Step 6 is the first deploy, and **there is no placeholder image**. Step 4 creates everything the
service needs and not the service{% if cookiecutter.use_postgres == "yes" %}, nor the migration {{ 'task' if cookiecutter.deploy_target == 'aws-ecs-express' else 'job' }}{% endif %}: nothing real has been built
yet, and a service on a stand-in container is a service whose first URL serves the wrong thing.
`just infra-first-deploy` builds the production image from this checkout — `backend/docker/Dockerfile.prod`,
target `production`, for `linux/amd64`, with the `GIT_SHA` build argument, tagged with the commit,
exactly as CD builds it — and refuses while the component's folder has uncommitted changes, because
the image is named for a commit. It pushes the image, sets `serviceImage` in stack configuration and
applies, so the service is created on the real build.{% if cookiecutter.use_postgres == "yes" %} Then it runs the migrations exactly as CD
does; until they finish the service's first tasks are live but not ready, which is why every health
check here reads liveness.{% endif %} It waits for the rollout, smoke-tests `/health/live` and `/health/ready`,
and prints the URL. It refuses a stack that has not been applied{% if cookiecutter.use_postgres == "yes" %} or has no pinned secrets{% endif %}, and one
that already has a service: from then on CD owns the image, and `serviceImage` records the first
deploy and nothing more. It applies, so it prompts as step 4 does — `just infra-first-deploy dev --yes`
unattended.

Step 7 refuses until there is a service: a `service_url` that is still empty means step 6 has not
run, and the script says so. Step 8 hands every later image to CD, and every later infrastructure
change to `{{ cookiecutter.component }}-infra.yml`.

### After setup, CI applies every change

Steps 1 to 7 cannot run from CI: the identity CI applies with — the apply role,
`{{ cookiecutter.__name }}-apply-<env>` — is one of the things step 4 creates. So they run from an
operator's machine, once per environment, and that is the one exception the Pulumi standards' MUST-9
makes. From then on **every infrastructure change merges and is applied by
`{{ cookiecutter.component }}-infra.yml`**: a pull request that touches `infra/` gets the component
tests and the preview's diff as a comment, and the merge to `main` runs `pulumi up --refresh` on the
`dev` stack as the apply role. Applying from a laptop after setup is the exception MUST-9 forbids —
it skips the review the preview exists for and leaves no audit trail. `just infra-up` stays for
setting up an environment.

The apply role trusts a token from the `infrastructure` GitHub environment of
`{{ cookiecutter.github_repository }}` and nothing else, and it holds `AdministratorAccess`. That
is deliberate. `pulumi up` creates and deletes everything in this stack — IAM roles and their
policies, the network, {% if cookiecutter.use_postgres == "yes" %}RDS, {% endif %}ECS, ECR, Secrets Manager — and writes its own state; a hand-written list
covering all of it still has to write IAM, the apply role's own policy among it, and the right to
write IAM is the right to grant anything. A narrower list would not be narrower in effect. It would
only break, as an AccessDenied halfway through an apply on `main`, the first time the stack gained a
resource type. What bounds the role is the environment and the account:

- **Protect the `infrastructure` environment before step 8**, in the repository's settings under
  Environments: required reviewers, and deployment branches limited to `main`. GitHub creates an
  environment with no rules the first time a job names it, and an unprotected one is an apply
  nobody approved. No stack can set these rules — they belong to the repository, and the reviewers
  are people.
- **One AWS account per environment** (ask Athena for the ECS Express standards, SHOULD-4) keeps the
  role's reach to the environment it applies.

Applies queue, one at a time, and are never cancelled: a `pulumi up` stopped halfway leaves
resources half-changed and the stack locked. When one fails, `main` is ahead of the account until
the fix merges — fix forward with another pull request, and the merge applies it.

### Taking it down, which is not one command

```
1. just infra-destroy dev                 removes every resource the stack created
2. just infra-teardown                    removes the three things the stack never created
                                          (refuses when another project shares the backend)
3. nothing, 30 days on                    KMS deletes the key itself once its waiting period ends
```

**`pulumi destroy` does not undo step 1 of the setup order at the top of this page.** It removes
what the stack created and nothing else, and the state bucket, the KMS key and the OIDC provider
were created by `state-bucket.sh`, with the plain AWS CLI. Pulumi has never known about them, so no
Pulumi command will ever take them away — which is the whole reason `teardown.sh` exists.

**The bucket may not be yours, and that is the default rather than the edge case.**
`state-bucket.sh` derives the name from the account and region alone, with no per-service
component, and the key alias the same way. Generate a second service into the same account and it
bootstraps onto the same bucket and the same key, by construction. One backend then holds several
Pulumi projects, a directory each under `.pulumi/stacks/`, and deleting the bucket deletes all of
their state. So `teardown.sh` lists that prefix first and refuses when it finds a directory this
project does not own, printing the `pulumi stack rm --remove-backups` path instead — that is the
Pulumi standards' MUST-8; ask Athena for it. A listing it cannot read counts as shared.

**With several components in one repository, sharing the backend is the normal case.** Every
component of this repository deploying to the same {{ 'account and region' if cookiecutter.deploy_target == 'aws-ecs-express' else 'project' }} bootstraps onto the same bucket, a
Pulumi project each — this one is `{{ cookiecutter.__name }}`. Taking the whole repository down is therefore: `just
<component> infra-destroy` for every component; `pulumi stack rm <stack> --remove-backups` from
every component's `infra/pulumi` but the last; and `just <component> infra-teardown` last, from the
component whose state is the only state left.

The order is the second trap. The bucket holds the state `pulumi destroy` reads, so deleting the
bucket first strands the stack: every resource it created still exists, still bills, and the only
record of what those resources are has just been deleted. `teardown.sh` exports every stack in the
backend, `--all` rather than only this Pulumi project's, and refuses while any of them still holds
a resource. Then it asks you to type the bucket name, and refuses outright when nothing can answer.
There is deliberately no `--yes` here. **S3 has no soft delete**: a deleted bucket, and every
version in it, is gone.

The KMS key is **scheduled for deletion, not deleted** — 30 days, the longest KMS allows, because the
only thing a shorter wait buys is less time to notice that something still needed it. Its alias is
removed at once, so a later bootstrap creates a fresh key; until the date passes,
`aws kms cancel-key-deletion` brings the old one back, and the script prints the three commands that
restore it. The OIDC provider is deleted only when no IAM role in the account still trusts it —
another team's pipeline, Pulumi-managed or not, would otherwise be locked out with no warning.

What `pulumi destroy` takes with it, and what that means for the secrets: every Secrets Manager
secret the stack created, at once outside production and after a 30-day recovery window in it{% if cookiecutter.use_postgres == "yes" %};
and the RDS instance, whose managed master secret RDS deletes with it. Deletion protection, the
final snapshot in production and `skip_final_snapshot` elsewhere, and automated backups are exactly
as they were — managing the master password changes none of them{% endif %}. Version ids pinned in
`Pulumi.<stack>.yaml` then name versions that no longer exist, so before standing the stack up again
remove every `…Version` key there (`pulumi config rm <key> --stack dev`); the next apply references
nothing, and the order from step 4 fills them again.

Two things `pulumi destroy` leaves that cost nothing and are worth knowing about: the task
definition revisions CD registered for the migration task, which are not this stack's and stay
`ACTIVE` (they are free), and the images in a production ECR repository, which `forceDelete` keeps
outside `dev` — empty it first, or the destroy stops on it.

If the AWS account exists only for this service, closing the account ends every charge at once.

## Layout

```
infra/
├── bootstrap/state-bucket.sh   plain AWS CLI; the state bucket, its KMS key, the GitHub OIDC provider
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
{%- if cookiecutter.use_postgres == "yes" %}
├── scripts/secrets.sh          generates the secrets the stack reads, stores them, pins their versions
{%- endif %}
├── scripts/first-deploy.sh     builds the real image, creates the service on it, migrates, smoke-tests
├── scripts/sync-github.sh      copies stack outputs into GitHub repository variables
└── pulumi/
    ├── Pulumi.yaml             project, and the S3 backend URL
    ├── Pulumi.dev.yaml         per-stack configuration; a second environment is a sibling file
    ├── __main__.py             loads the configuration, deploys, exports
    ├── stack.py                the composition root, as a function the tests can run
    ├── config.py               typed StackConfig; the only place configuration keys are named
    ├── components/             one file per concern
    └── tests/                  pulumi.runtime.set_mocks — no credentials needed
```

Two properties keep this from accreting. **Components take a frozen `StackConfig`** and their
explicit dependencies, so none of them reads global configuration or looks anything up, and each
can be constructed in a test. **`Service` describes an Express service, not this app**: the port,
the health path, the roles, the secrets and the log group are all arguments. A second service — a
worker, an admin API — is one more instantiation in `stack.py`, not a copied file with a
different string in it.

## What the stack provisions, and what it costs

| Component | Resources | Cost posture |
| --- | --- | --- |
| `network` | a VPC, an internet gateway, a public subnet per zone{% if cookiecutter.use_postgres == "yes" %}, an isolated subnet per zone with no route out,{% endif %} and the security groups — two zones by default, `availabilityZoneCount`. **No NAT gateway** | free; see below |
| `registry` | an ECR repository per image, tags immutable, scanned on push, keeping the last 30 | pennies |
| `logs` | a CloudWatch log group per thing that runs, 30 days' retention (90 in production) | pennies at this volume |
| `cluster` | the ECS cluster, Container Insights off outside production | free |
| `identities` | the infrastructure, execution and runtime roles{% if cookiecutter.use_postgres == "yes" %}, the migration task's own execution role{% endif %}, and three CI roles that trust GitHub's OIDC token: `deploy`, `preview` and `apply` | free |
| `secrets` | Secrets Manager secrets, all created empty; no value is ever in the stack{% if cookiecutter.use_postgres == "yes" %}. The master password's secret is RDS's own, not one of these{% endif %} | a small charge per secret per month |
| `service` | one Express service, with the load balancer Express Mode provisions for it, scaling on CPU from **one task** | **standing charge**: the load balancer and one Fargate task per service, always |
{%- if cookiecutter.use_postgres == "yes" %}
| `migrations` | a Fargate task definition CD runs before every deploy | only while it runs |
| `database` | RDS PostgreSQL 16, `db.t4g.micro`, 20 GB gp3, single-AZ, 7 daily backups; IAM database authentication on, master password managed by RDS | **standing charge** |
{%- endif %}

**Express Mode cannot scale to zero.** That is the difference from a scale-to-zero platform worth
knowing before the first apply: `minTasks` is at least 1, and that task runs — and bills — whether
anybody calls it or not. So does the Application Load Balancer Express Mode provisions, which bills
by the hour plus by usage; it is shared by up to 25 Express services in a VPC, so a second service adds
a task but not a second load balancer. **Public IPv4 addresses bill by the hour**
too — the load balancer's, and one per running task.

**There is no NAT gateway, on purpose.** Express Mode puts the load balancer and the tasks in the
same subnets, so they are public subnets and each task has a public IP, which is how it reaches ECR,
Secrets Manager and CloudWatch Logs. A NAT gateway instead is an hourly charge plus a per-gigabyte
one, which on a service this size would cost more than the task it serves. Inbound, a public IP
admits nothing a security group does not: Express Mode's own group admits its load balancer, and
the stack's `app` group has no ingress rules at all.
{%- if cookiecutter.use_postgres == "yes" %}

### The database settings are already the cheapest ones that work

| Setting | Value | Why nothing is lower |
| --- | --- | --- |
| `databaseInstanceClass` | `db.t4g.micro` | The smallest burstable Graviton class RDS offers for PostgreSQL 16. Burstable means CPU credits: sustained load runs out of them. |
| `databaseStorageGb` | `20` | gp3's minimum allocation. Storage autoscaling is on up to 100 GB, so it grows when it has to — and never shrinks back, which is why starting at the floor matters. |
| `storage_type` | `gp3` | The general-purpose SSD type with baseline IOPS included in the storage price. |
| `databaseMultiAz` | `false` | Multi-AZ is a synchronous standby in a second zone, at roughly double the instance charge. |
| `backup_retention_period` | `7` | Automated backups up to the size of the database are included; retention is your recovery window. |
| `deletion_protection`, final snapshot | production only | Driven by the stack's `environment`: a `dev` destroy finishes, a `prod` one cannot happen by accident. |

**What the floor costs you, stated plainly:** a single-AZ instance has no standby, so a zone
outage or an instance failure is downtime until RDS replaces it, and a burstable class that runs out
of credits is slow. That is a development database with a real backup schedule. Moving off the floor
for production means `databaseMultiAz: "true"` and a non-burstable class, in that order.

The database is not publicly accessible, lives in subnets with no route out of the VPC, and admits
Postgres only from the service's security group. Every connection is TLS: the parameter group sets
`rds.force_ssl`, `rds-iam` always asks for it, and the migration sets `PGSSLMODE=require`.
Not `verify-full`, because the RDS certificate authority is not in the image's trust store; the path
never leaves the VPC.
{%- endif %}

**No figure is quoted here on purpose.** The standing charge is a short list of line items — the
load balancer, the Fargate tasks, their public IPv4 addresses{% if cookiecutter.use_postgres == "yes" %}, the RDS instance, its storage and backups{% endif %} — and each is priced per region and changes without this file
changing. Price them in the AWS Pricing Calculator for `{{ cookiecutter.aws_region }}` before you
apply, then read the real number off the first bill. The real zero is `just infra-destroy` and then
`just infra-teardown`; read the teardown order above first.

## The two environments, which are not the same environment

There are two settings in this repository called some version of "environment", they are set
independently, and reading them as one thing is the most common way a first deploy surprises
its author.

| | Where it is set | What it does | Value in the `dev` stack |
| --- | --- | --- | --- |
| **The stack's** | `{{ cookiecutter.__name }}:environment` in `Pulumi.dev.yaml` | Names resources, and nothing else. `{{ cookiecutter.__name }}-dev`, `{{ cookiecutter.__name }}-deploy-dev`, `{{ cookiecutter.__name }}-jwt-secret-dev`. It is also what `is_production` reads to decide deletion protection, final snapshots, secret recovery windows and log retention. The application never sees it. | `dev` |
| **The application's** | `{{ cookiecutter.__name }}:appEnvironment` in `Pulumi.dev.yaml` | Becomes `ENVIRONMENT` on the task. One of `local`, `test`, `staging`, `production` — the literal in `backend/app/core/config.py`. It decides how the service *behaves*: `production` serves no `/docs` and no `/openapi.json`. | `staging` |

They are separate because they vary separately. A stack called `dev` that the whole company can
reach is production as far as the application is concerned; a stack called `prod` standing up a
customer demo is not. **Set `appEnvironment` to `production` in the stack that real users reach.**
Anything outside the four values is refused by `config.py` when the stack loads, rather than by a
container exiting several minutes into an apply.

## Who owns what

The stack owns the *shape* of the Express service — roles, scaling, network, secret references,
logs, the health path. The CD pipeline owns exactly one field: the image. The image is declared with
`ignore_changes`, and every preview and apply passes `--refresh`.

Both halves are needed. `ignore_changes` suppresses the diff between the program and Pulumi's
state; it does not tell state that CD changed the image. Without `--refresh`, program and state
both still say the first deploy's image, there is no diff to ignore, and the next apply of any unrelated field sends
the whole container — stale image included. The `just infra-*` recipes are where that is enforced.
{%- if cookiecutter.use_postgres == "yes" %}

The migration task works differently, because a task definition is immutable: CD never edits the
stack's revision. It registers a new revision derived from the latest one with only the image
changed, runs it, and fails the deploy unless it exits 0. The stack's revision is the shape —
command, roles, the database secret, logs — and changing it here is picked up by the next deploy.

**Two identities reach the database, and neither password is the stack's.**

| | Logs in as | With | Owns |
| --- | --- | --- | --- |
| The service | `databaseAppRole` (`app_iam`), created by the migration and granted `rds_iam` | an IAM token the backend signs with the task role for every new connection (`DATABASE_AUTH=rds-iam`); the task role holds `rds-db:connect` on that one user of this one instance | nothing — every migration grants it `SELECT, INSERT, UPDATE, DELETE` on the tables, sequence usage, and the same as default privileges |
| The migration task | the RDS master user, `app` | the master secret RDS generates, stores and rotates, read by JSON key (`<arn>:username::`, `<arn>:password::`) by the migration's own execution role, the only role that can read it | the schema |
{%- endif %}

Every secret reference pins a version: `<secret ARN>:::<VersionId>`, never the bare ARN, which
means "whatever is current when the task starts". A task reads its secrets when it starts, so an
unpinned reference lets the tasks of one deployment hold different values after a rotation. Pinned,
a new value is a new deployment, and a rollback restores the credential with the image.
{%- if cookiecutter.use_postgres == "yes" %}

**One reference is deliberately unpinned: the migration task's master credentials.** RDS owns that
secret and rotates it; a reference pinned to a `VersionId` would break the migration at the first
rotation. A task runs once and exits, so a rotation can only ever affect the next run.
{%- endif %}

## A browser client

This stack serves an API and nothing else. A browser client is its own repository, with its own
stack and its own pipeline, generated from `templates/cookiecutter-react-app` in the Engineering
Athena handbook (ADR-0022). What this stack owns is the setting{% if cookiecutter.use_postgres == "yes" %}s{% endif %} that let{% if cookiecutter.use_postgres != "yes" %}s{% endif %} one call the API,
in `pulumi/Pulumi.<stack>.yaml`:

| Key | Becomes | Set it to |
| --- | --- | --- |
| `corsOrigins` | `CORS_ORIGINS` on the service, only when non-empty | the React app's `site_url` output; comma-separated for more than one. Empty, the default, means no browser on another origin may call the API |
{%- if cookiecutter.use_postgres == "yes" %}
| `refreshTransport` | `REFRESH_TRANSPORT` on the service | `cookie` for a browser client: the refresh token travels as an httpOnly cookie rather than in the JSON body. `body`, the default, is for a server-to-server caller. `cookie` needs `corsOrigins`, because the cookie routes' CSRF check accepts only listed origins |
{%- endif %}

`config.py` refuses an entry that is not an origin — a path, a trailing slash, a `*`{% if cookiecutter.use_postgres == "yes" %} — and
`cookie` with no origin{% endif %}, when the stack loads; the service would refuse the same values at start-up,
several minutes into an apply. Merging the change applies it, which deploys the service with the
new environment.
Neither setting reaches the migration task, which serves no HTTP.
{%- if cookiecutter.use_postgres == "yes" %}

**A session that survives a reload needs the SPA and the API on one site.** The refresh token is
a `SameSite=Strict` cookie, and the default hostnames are cross-site: this service answers on an `*.on.aws` name and a React app on CloudFront on a `*.cloudfront.net` one. The React template
closes that by default: it serves this API under `/api/*` and `/health/*` on the app's own origin,
so the browser sees one site. On this side, set `refreshTransport=cookie` and list the app's
`site_url` in `corsOrigins`; the cookie routes' CSRF check reads that list on same-origin requests
too. A domain you own, with the app and the API on two of its subdomains, is the production setup.
Relaxing the cookie to `SameSite=None` is not the fix: it reopens the cross-site request surface the
Strict setting closes, and browsers that block third-party cookies drop it anyway.
{%- endif %}

## Prepackaged, or manual

Every step of getting this repository into production is one or the other, and the line is not a
matter of taste. **It is prepackaged when the value or the decision already exists inside this
repository, and manual when it needs an account, a bill, a name you own, or a human looking at
something.**

Prepackaged — generated, wired together, and covered by a test:

| | Where it lives |
| --- | --- |
| The VPC, the Express service, the registry, the roles, the secrets{% if cookiecutter.use_postgres == "yes" %}, the database, the migration task{% endif %} | `pulumi/stack.py` |{% if cookiecutter.use_postgres == "yes" %}
| Generating the signing key, storing it and pinning its version | `scripts/secrets.sh`, via `just infra-secrets` |
| The service's database role and its grants | the migration, on every run — `backend/alembic/env.py` |{% endif %}
| The first deploy: the real image, built from this checkout, and the service created on it | `scripts/first-deploy.sh`, via `just infra-first-deploy` |
| Every repository variable the pipelines read, prefixed `{{ cookiecutter.__component_var }}_` | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| Removing the state bucket, the KMS key and the OIDC provider, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| Backend image build{% if cookiecutter.use_postgres == "yes" %}, migration{% endif %} and deploy, after the first | `.github/workflows/{{ cookiecutter.component }}-cd.yml` |
| A `pulumi preview` on every pull request, as a read-only role, and a `pulumi up` on every merge to `main`, as the apply role | `.github/workflows/{{ cookiecutter.component }}-infra.yml` |

The stack's exports, the variables `sync-github.sh` sets and the `vars.` names the workflows read
have to agree exactly, so a unit test in `pulumi/tests` runs the program against mocks and asserts
that they do. A name changed in one of the three is then a failing build rather than a deploy job
that is skipped forever and never says why.

Manual, and no template can change that:

| | Why it cannot be shipped |
| --- | --- |
| An AWS account, with billing | It costs money and names a payer |
| `./bootstrap/state-bucket.sh`, then `pulumi stack init` | A stack cannot create the bucket that holds its own state |
| Reading `just infra-preview` before the first apply | The point of the step is that a human looked |
| A value only a person has — a Sentry DSN — in its empty secret, and its `VersionId` in stack configuration | It comes from an account outside this project, and a secret value must never exist in this repository |
| `{{ cookiecutter.__component_var }}_SENTRY_ORG`, `{{ cookiecutter.__component_var }}_SENTRY_PROJECT`, `{{ cookiecutter.__component_var }}_SENTRY_AUTH_TOKEN` | They identify an account outside this project |
| A custom domain and its certificate | You own the name; nothing here can |
| A GitHub environment called `production`, with whatever protection rules you want | The deploy role trusts that environment by name; GitHub creates it on the first run if it does not exist |
| A GitHub environment called `infrastructure`, with required reviewers and deployments from `main` only | The apply role trusts that environment by name, and can change anything in the account; its rules are the gate on every apply after setup |
| Confirming `just infra-teardown` | Deleting a state bucket is irreversible |
| `corsOrigins`{% if cookiecutter.use_postgres == "yes" %} and `refreshTransport`{% endif %}, once a browser client exists | Its URL comes from another repository's stack — see "A browser client" |
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

## What a human has to do, and what no script can

| | Who | Where |
| --- | --- | --- |
| Install `aws` and `pulumi`, and get a session on the account | you | your machine |
| Run the bootstrap script | you, once per account and region | `./infra/bootstrap/state-bucket.sh` |
| `pulumi stack init` with the KMS secrets provider | you, once | the bootstrap script prints the command |
| Read the preview before the first apply | you | `just infra-preview` |
{% if cookiecutter.use_postgres == "yes" %}| Generate, store and pin `jwt-secret` | you run it; the script does the rest | `just infra-secrets` |
| Nothing, for the database's passwords | RDS, and IAM | the master password is RDS's; the service has none |
{% endif %}| Add a value only you have, such as the Sentry DSN, and pin its `VersionId` | you | `aws secretsmanager put-secret-value --secret-string file:///dev/stdin`, then `Pulumi.<stack>.yaml` |
| Build the first image and create the service on it | you run it; the script does the rest | `just infra-first-deploy` |
| Set the repository variables | `sync-github.sh` | `just infra-sync-github` |
| Protect the `infrastructure` environment | you, before the first push | GitHub settings |
| Set `{{ cookiecutter.__component_var }}_SENTRY_ORG`, `{{ cookiecutter.__component_var }}_SENTRY_PROJECT` and the `{{ cookiecutter.__component_var }}_SENTRY_AUTH_TOKEN` secret | you | GitHub settings |
| Protect `main`, and require CI before merge | you | GitHub settings |
| Tear the stack down, when it is time | you | `just infra-destroy`, then `just infra-teardown` |

Do not create an IAM user or an access key for any of this. You authenticate as yourself; CI
exchanges GitHub's OIDC token for a short-lived session on a role that trusts exactly this
repository — `deploy` in the `production` environment, `preview` on pull requests, `apply` in the
`infrastructure` environment. A long-lived
access key in a CI secret is the most-stolen credential class there is, and the handbook bans it.

## Going to production

One stack per environment, and **one AWS account per environment**: production in an account of
its own, with its own bucket, key and OIDC provider from the same bootstrap. An account is the
strongest isolation AWS has — IAM, quotas and billing all stop at its edge — and it is what keeps a
`dev` experiment from ever holding a permission on production.

1. Run `AWS_ACCOUNT_ID=<prod account> ./infra/bootstrap/state-bucket.sh` with a session on that
   account.
2. Copy `Pulumi.dev.yaml` to `Pulumi.prod.yaml`, and set `accountId`, `environment: prod` and
   `appEnvironment: production`.
3. Point the stack at that account's bucket. `Pulumi.yaml` names one backend, and the
   `PULUMI_BACKEND_URL` environment variable takes precedence over it:
   `export PULUMI_BACKEND_URL="s3://pulumi-state-<prod account>-<region>?region=<region>"` in the
   shell that runs the production stack. `pulumi whoami -v` prints the backend it will use; read it
   before `stack init --secrets-provider="awskms://alias/pulumi-state?region=<region>"`.
4. Decide the knobs the `dev` floor leaves at their minimum: `minTasks` above 1 for a service that
   must survive a task being replaced, `databaseMultiAz`, a larger instance class.

Then the order of operations from step 4, `just infra-secrets prod` included: each stack has its
own signing key, generated into its own account. `{{ cookiecutter.component }}-infra.yml` previews and applies the `dev`
stack; the production stack is a second pair of jobs, with that backend URL and its own role
variables.

`environment: prod` turns on what a production stack needs and a `dev` one must not have: deletion
protection and a final snapshot on the database, a 30-day recovery window on every secret, 90 days of
logs, enhanced Container Insights, and ECR repositories a destroy will not empty.

## Adding a second service

A second service is one more `Service(...)` in `stack.py`, with its own execution role, log group
and repository. Give it its own roles rather than reusing these: a service that holds another
service's roles turns any compromise of the least protected surface into that service's secrets.
Deploying it is a workflow modelled on `{{ cookiecutter.component }}-cd.yml`, an export, and a line in `sync-github.sh`;
the test that asserts those three agree is what tells you when you have done two of the three.

A service with a codebase of its own is not that: it is another component. Generate it into the
same repository with another `component=` — its folder, workflows, resources and repository
variables all carry its own name, so nothing here needs an edit.

## Related

- Ask Athena for the **Pulumi standards** — the graded rules this stack is built to satisfy, and
  what a reviewer will check it against — and ADR-0021, which puts Pulumi's state in the cloud it
  deploys to.
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi AWS provider — https://www.pulumi.com/registry/packages/aws/
- Amazon ECS Express Mode — https://docs.aws.amazon.com/AmazonECS/latest/developerguide/express-service-overview.html
