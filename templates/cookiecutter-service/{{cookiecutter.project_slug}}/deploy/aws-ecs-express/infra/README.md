# Infrastructure

Pulumi in Python, deploying to **AWS: ECS Express Mode**{% if cookiecutter.use_postgres == "yes" %} and **RDS for PostgreSQL**{% endif %}.
One component per concern, composed in `pulumi/stack.py`, which reads top to bottom as a
dependency graph.

Nothing here has been applied yet. A freshly generated repository has a stack that type-checks and
unit-tests without any cloud access, and has never created a resource. Everything below is what
turns that into a running service, in the order it has to happen.

## The order, and why it cannot change

```
1. aws + pulumi installed, a session on account …   nothing is created
2. an AWS account with billing                     the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain AWS CLI; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
   pulumi config set githubSubjectPrefix …         the OIDC subject GitHub issues for this repository
5. just infra-preview                              read it, by a human
6. just infra-up                                   creates the service on a placeholder image
7. aws secretsmanager put-secret-value …           values a human supplies, never Pulumi
8. just infra-sync-github                          copies the stack's outputs into repository variables
9. merge to main                                   cd.yml is no longer inert; the real image deploys
```

Step 1 means a session on **account {{ cookiecutter.aws_account_id }}**, region
`{{ cookiecutter.aws_region }}` — `aws sso login`, or whatever your organisation uses. Both the
bootstrap script and the stack refuse any other account: the script checks
`aws sts get-caller-identity`, and the provider is configured with `allowed_account_ids`, so a shell
pointed at last week's client fails before it plans anything.

Step 3 is a script rather than a stack because a stack cannot create the bucket that holds its own
state — the first `pulumi up` would need somewhere to write before that somewhere exists. It creates
three account-level things, all shared by every service generated into this account and region:

| | Name | Why it is the script's and not the stack's |
| --- | --- | --- |
| State bucket | `pulumi-state-{{ cookiecutter.aws_account_id }}-{{ cookiecutter.aws_region }}` | Holds every stack's state. Versioned, Block Public Access on all four settings, owner-enforced (no ACLs), SSE-KMS with an S3 Bucket Key, TLS-only by bucket policy, old versions expired after 90 days |
| KMS key | `alias/pulumi-state` | Encrypts the bucket and every stack's secrets — the stack's secrets provider is `awskms://alias/pulumi-state?region={{ cookiecutter.aws_region }}`. Symmetric, yearly rotation on |
| GitHub OIDC provider | `token.actions.githubusercontent.com` | One per account, trusted by every service's CI roles. A stack that owned it would delete it from under its neighbours on `destroy` — the ownership problem ADR-0013 describes for the bucket — so the stack only looks it up |

The second line of step 4 is there because GitHub decides the shape of the `sub` claim in this
repository's OIDC tokens, and the CI roles trust exactly one. Many repositories now get the
immutable form, `repo:<owner>@<owner-id>/<repo>@<repo-id>`, so that a renamed or re-registered
account cannot inherit the trust; a role written for the name-only `repo:<owner>/<repo>` then admits
no token, and every CD run fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity".
Ask the repository, after it exists on GitHub:

```bash
uv run --directory infra/pulumi pulumi config set githubSubjectPrefix \
  "$(gh api repos/{{ cookiecutter.github_repository }}/actions/oidc/customization/sub --jq '.sub_claim_prefix // empty')"
```

Empty is the name-only form. `just infra-sync-github` makes the same request and refuses to un-gate
CD while the stack trusts anything else, printing the command that fixes it.

There is **no DynamoDB lock table**. Pulumi's self-managed backend locks with objects of its own
under `.pulumi/locks/` in the bucket, so two concurrent `up`s against one stack refuse each other.

Step 6 asks for confirmation and **refuses to run with nothing there to confirm** — "--yes must be
passed in to proceed when running in non-interactive mode". The recipe does not pass `--yes` for
you; this command creates billable resources and a human reading the preview is the point. From a
pipeline, or from an agent with no terminal, type it: `just infra-up dev --yes`. The stack name is
positional, so the flag has to follow one.

Step 7 is separate because a human-chosen secret *value* must never appear in infrastructure code,
in a stack configuration file, or in state. The stack creates the empty secret; what goes inside is
added out of band, and its `VersionId` goes into stack configuration so the service pins exactly
that version. Ask Athena for the Pulumi standards for the graded rule.{% if cookiecutter.use_postgres == "yes" %} The database DSN and the token
signing key are the exception: the stack generates both, and nobody ever types either.{% endif %}

Step 9 is last because the service exists before the image does. `pulumi up` creates the Express
service on a public placeholder container — Google's `cloudrun/container/hello`, used here for its
properties alone: anonymous pull, listens on 8080, answers 200 on every path. ECR is empty until the
first CD run, and Express Mode will not create a service without an image. Express Mode assigns the
service a URL, and only then can a pipeline push a real image at it. That ordering matters more
than it looks: anything that compiles its own origin into a build artefact — a client bundle with an
API base URL in it — cannot be built correctly until the URL exists, so the sequence is placeholder
image, then URL into a repository variable, then the real build.

### Taking it down, which is not one command

```
1. just infra-destroy dev                 removes every resource the stack created
2. just infra-teardown                    removes the three things the stack never created
                                          (refuses when another project shares the backend)
3. nothing, 30 days on                    KMS deletes the key itself once its waiting period ends
```

**`pulumi destroy` does not undo step 3 of the setup order at the top of this page.** It removes
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
| `identities` | the infrastructure, execution and runtime roles, and two CI roles that trust GitHub's OIDC token: `deploy` and `preview` | free |
| `secrets` | Secrets Manager secrets{% if cookiecutter.use_postgres == "yes" %}, two of them — the database DSN and the token signing key — generated here with a version the moment the stack applies{% endif %} | a small charge per secret per month |
| `service` | one Express service, with the load balancer Express Mode provisions for it, scaling on CPU from **one task** | **standing charge**: the load balancer and one Fargate task per service, always |
{%- if cookiecutter.use_postgres == "yes" %}
| `migrations` | a Fargate task definition CD runs before every deploy | only while it runs |
| `database` | RDS PostgreSQL 16, `db.t4g.micro`, 20 GB gp3, single-AZ, 7 daily backups | **standing charge** |
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
`rds.force_ssl`, and the DSN asks for `ssl=require`. Not `verify-full`, because the RDS certificate
authority is not in the image's trust store; the path never leaves the VPC.
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
| **The stack's** | `{{ cookiecutter.project_slug }}:environment` in `Pulumi.dev.yaml` | Names resources, and nothing else. `{{ cookiecutter.project_slug }}-dev`, `{{ cookiecutter.project_slug }}-deploy-dev`, `{{ cookiecutter.project_slug }}-jwt-secret-dev`. It is also what `is_production` reads to decide deletion protection, final snapshots, secret recovery windows and log retention. The application never sees it. | `dev` |
| **The application's** | `{{ cookiecutter.project_slug }}:appEnvironment` in `Pulumi.dev.yaml` | Becomes `ENVIRONMENT` on the task. One of `local`, `test`, `staging`, `production` — the literal in `backend/app/core/config.py`. It decides how the service *behaves*: `production` serves no `/docs` and no `/openapi.json`. | `staging` |

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
both still say `hello`, there is no diff to ignore, and the next apply of any unrelated field sends
the whole container — stale image included. The `just infra-*` recipes are where that is enforced.
{%- if cookiecutter.use_postgres == "yes" %}

The migration task works differently, because a task definition is immutable: CD never edits the
stack's revision. It registers a new revision derived from the latest one with only the image
changed, runs it, and fails the deploy unless it exits 0. The stack's revision is the shape —
command, roles, the database secret, logs — and changing it here is picked up by the next deploy.
{%- endif %}

Every secret reference pins a version: `<secret ARN>:::<VersionId>`, never the bare ARN, which
means "whatever is current when the task starts". A task reads its secrets when it starts, so an
unpinned reference lets the tasks of one deployment hold different values after a rotation. Pinned,
a new value is a new deployment, and a rollback restores the credential with the image.

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
several minutes into an apply. `just infra-up` then deploys the service with the new environment.
Neither setting reaches the migration task, which serves no HTTP.
{%- if cookiecutter.use_postgres == "yes" %}

**A session that survives a reload needs the SPA and the API on one site** — `app.example.com`
and `api.example.com`, under a domain you own. The refresh cookie is `SameSite=Strict`, and the
default hostnames are cross-site: this service answers on an `*.on.aws` name, a React app on
CloudFront on a `*.cloudfront.net` one, and those are two sites. On default hostnames signing in
works, and a reload signs the user out.
{%- endif %}

## Prepackaged, or manual

Every step of getting this repository into production is one or the other, and the line is not a
matter of taste. **It is prepackaged when the value or the decision already exists inside this
repository, and manual when it needs an account, a bill, a name you own, or a human looking at
something.**

Prepackaged — generated, wired together, and covered by a test:

| | Where it lives |
| --- | --- |
| The VPC, the Express service, the registry, the roles, the secrets{% if cookiecutter.use_postgres == "yes" %}, the database, the migration task{% endif %} | `pulumi/stack.py` |
| Every repository variable the pipelines read | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| Removing the state bucket, the KMS key and the OIDC provider, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| Backend image build{% if cookiecutter.use_postgres == "yes" %}, migration{% endif %} and deploy | `.github/workflows/cd.yml` |
| A `pulumi preview` on every pull request, as a read-only role | `.github/workflows/infra.yml` |

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
| A value inside every empty Secrets Manager secret, and its `VersionId` in stack configuration | A human-chosen secret value must never exist in this repository |
| `SENTRY_ORG`, `SENTRY_PROJECT`, `SENTRY_AUTH_TOKEN` | They identify an account outside this project |
| A custom domain and its certificate | You own the name; nothing here can |
| A GitHub environment called `production`, with whatever protection rules you want | The deploy role trusts that environment by name; GitHub creates it on the first run if it does not exist |
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
| Populate every empty Secrets Manager secret, and pin its `VersionId` | you | `aws secretsmanager put-secret-value`, then `Pulumi.<stack>.yaml` |{% if cookiecutter.use_postgres == "yes" %}
| Nothing, for `jwt-secret` or `database-url` | the stack | generated, referenced by version, never typed |{% endif %}
| Set the repository variables | `sync-github.sh` | `just infra-sync-github` |
| Set `SENTRY_ORG`, `SENTRY_PROJECT` and the `SENTRY_AUTH_TOKEN` secret | you | GitHub settings |
| Protect `main`, and require CI before merge | you | GitHub settings |
| Tear the stack down, when it is time | you | `just infra-destroy`, then `just infra-teardown` |

Do not create an IAM user or an access key for any of this. You authenticate as yourself; CI
exchanges GitHub's OIDC token for a short-lived session on a role that trusts exactly this
repository — `deploy` in the `production` environment, `preview` on pull requests. A long-lived
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

`environment: prod` turns on what a production stack needs and a `dev` one must not have: deletion
protection and a final snapshot on the database, a 30-day recovery window on every secret, 90 days of
logs, enhanced Container Insights, and ECR repositories a destroy will not empty.

## Adding a second service

A second service is one more `Service(...)` in `stack.py`, with its own execution role, log group
and repository. Give it its own roles rather than reusing these: a service that holds another
service's roles turns any compromise of the least protected surface into that service's secrets.
Deploying it is a workflow modelled on `cd.yml`, an export, and a line in `sync-github.sh`; the test
that asserts those three agree is what tells you when you have done two of the three.

## Related

- Ask Athena for the **Pulumi standards** — the graded rules this stack is built to satisfy, and
  what a reviewer will check it against — and ADR-0021, which puts Pulumi's state in the cloud it
  deploys to.
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi AWS provider — https://www.pulumi.com/registry/packages/aws/
- Amazon ECS Express Mode — https://docs.aws.amazon.com/AmazonECS/latest/developerguide/express-service-overview.html
