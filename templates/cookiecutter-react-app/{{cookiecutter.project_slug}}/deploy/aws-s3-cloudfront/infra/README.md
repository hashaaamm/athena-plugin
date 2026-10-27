# Infrastructure

Pulumi in TypeScript, the application's own language (ADR-0020). One component per concern, in
`pulumi/src/components/`, composed in `pulumi/src/stack.ts`, which reads top to bottom as a
dependency graph.

Nothing here has been applied yet. A freshly generated repository has a stack that type-checks and
unit-tests without any cloud access, and has never created a resource. Everything below is what
turns that into a site on AWS — a private S3 bucket behind CloudFront — in the order it has to
happen.

## What runs where

Unlike the application, whose recipes all run in a container, **every recipe in `infra/` runs on
your machine.** Pulumi runs the TypeScript program with the Node and the `node_modules` on the
machine running `pulumi`, and `pulumi`, `aws` and `gh` act with your own credentials; putting them in
a container would mean mounting those credentials into it. So this directory, and only this
directory, has host prerequisites:

| Tool | Needed for | Check |
| --- | --- | --- |
| Node 24 and pnpm | `just infra-test`, `just infra-lint`, and every `pulumi` command (it runs `index.ts`) | `node --version`, `pnpm --version` |
| `pulumi` CLI | preview, up, destroy, stack outputs | `pulumi version` |
| `aws` CLI v2 | the bootstrap and teardown scripts, and your own authentication | `aws --version` |
| `gh` | `just infra-sync-github` | `gh auth status` |
| `python3` | `just infra-teardown` only (it reads JSON from `aws` and `pulumi`) | `python3 --version` |

Each recipe checks for the tools it uses and names the missing one. `just infra-test` needs only
Node and pnpm: it runs against Pulumi's mocks, with no cloud account at all.

`pnpm-lock.yaml` is not templated, for the same reason the application's is not: a lockfile frozen
when the template was written is stale on the day it ships. The first command is `cd infra && just
lock`, and its result is committed. CI installs with `--frozen-lockfile` and fails on a lockfile
that is missing or out of date.

TypeScript is pinned to 5.9 here, not the newest major: Pulumi runs the program through ts-node and
the TypeScript compiler API, and `@pulumi/pulumi` supports TypeScript below 7.

## The order, and why it cannot change

```
0. cd infra && just lock                           resolves pnpm-lock.yaml; commit it
1. aws + pulumi installed and authenticated        nothing is created
2. an AWS account for this environment             the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain aws CLI; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
   pulumi config set githubSubjectPrefix …         the OIDC subject GitHub issues for this repository
5. just infra-preview                              read it, by a human
6. just infra-up                                   creates the bucket, the distribution and the roles
7. just infra-sync-github                          copies the stack's outputs into repository variables
8. gh variable set API_URL …                       the backend's address, by hand
9. the backend: corsOrigins = this stack's site_url, applied in the backend's repository
10. merge to main                                  cd.yml is no longer inert; the bundle deploys
```

Step 3 is a script rather than a stack because a stack cannot create the bucket that holds its own
state. It creates three things, all of them shared by every service and site in the account and
region: the state bucket `pulumi-state-{{ cookiecutter.aws_account_id }}-{{ cookiecutter.aws_region }}`, the KMS key
`alias/pulumi-state` that encrypts it and every stack's secrets, and the GitHub Actions OIDC provider
the CI roles trust. **They are the same names the backend templates' bootstrap uses**, so if the
backend this site calls is already deployed into this account and region, step 3 finds all three and
changes nothing — run it anyway; it is idempotent and it re-applies the bucket's settings. The
script refuses to run when your credentials are for any account other than
`{{ cookiecutter.aws_account_id }}`.

State locking needs no DynamoDB table. Pulumi's self-managed backend writes a lock object under
`.pulumi/locks/` in the same bucket for the length of every update.

**One name to check at step 4.** The backend's directory in the shared bucket is named for its
Pulumi project, and this stack's is `{{ cookiecutter.project_slug }}` — the slug. If the backend was
generated with the same slug, `pulumi stack init dev` stops with "stack already exists". Do not
`stack select` it: that stack is the backend's, and an apply from here would replace everything in
it with a bucket and a distribution. Change `name:` in `pulumi/Pulumi.yaml` — and the
`{{ cookiecutter.project_slug }}:` prefix of every key in `Pulumi.dev.yaml` with it — and `slug` too,
because the CI roles are named from it and the backend's are named the same way. Then run step 4
again.

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
pipeline, or from an agent with no terminal, type it: `just infra-up dev --yes`. Creating a
CloudFront distribution takes several minutes; `pulumi up` waits for it to deploy.

Steps 8 and 9 are the two halves of the conversation with the backend, and neither can come from
this stack because both belong to the other repository. **`API_URL`** is compiled into the bundle
as `VITE_API_URL`, so `cd.yml` refuses to build without it — a bundle built without one calls
`{{ cookiecutter.api_url }}`, deploys cleanly, and fails every request. **The backend's `corsOrigins`**
must name this site's origin, the `site_url` output (`https://<id>.cloudfront.net`), or every
request from the browser fails CORS while the API answers it correctly.{% if cookiecutter.include_auth == "yes" %} Set the backend's
`refreshTransport=cookie` in the same change: this app keeps the refresh token in an HttpOnly
cookie, and a backend sending it in the body has nothing for the app to keep.{% endif %}

Step 10 is last because the bucket is empty until CD fills it. Before the first deploy CloudFront
answers every path with a 403 from S3, mapped to `/index.html` — which does not exist yet.

### A domain of your own

{% if cookiecutter.include_auth == "yes" -%}
**This is not optional for a session that survives a reload.** The refresh cookie is
`SameSite=Strict`, and `*.cloudfront.net` and the backend's own hostname are two sites: signing in
works, and a reload signs the user out. Put the site and the API on one site you own —
`app.example.com` and `api.example.com` — and set `API_URL` to the API's address.

{% endif -%}
Nothing here creates DNS or a certificate: you own the name, and nothing in this repository can.
What it takes, in the stack: an ACM certificate **in us-east-1** — CloudFront reads certificates
(and WAF web ACLs) from that region only, whatever region the bucket is in — so a second
`aws.Provider` with `region: "us-east-1"` and the same `allowedAccountIds`; the domain in the
distribution's `aliases`; and `viewerCertificate` switched from `cloudfrontDefaultCertificate` to
that certificate's ARN with `sslSupportMethod: "sni-only"` and `minimumProtocolVersion:
"TLSv1.2_2021"`. Then a DNS record pointing the name at the distribution, the new origin in the
backend's `corsOrigins`, and the `site_url` export changed to match.

### Taking it down, which is not one command

```
1. just infra-destroy dev                 removes every resource the stack created
2. just infra-teardown                    removes the three things the stack never created
                                          (refuses when another project shares the backend)
3. nothing, 30 days on                    the KMS key finishes deleting on its own
```

**`pulumi destroy` does not undo step 3 of the setup order.** It removes what the stack created
and nothing else, and the state bucket, the KMS key and the OIDC provider were created by
`state-bucket.sh`. Outside a `prod` stack the site bucket is created with `forceDestroy`, so
`destroy` empties it; a `prod` site bucket must be emptied by hand first.

**The state bucket is probably not yours alone, and that is the default rather than the edge
case.** It is named for the account and region, so a backend deployed beside this site shares it,
and deleting it deletes the backend's state too. `teardown.sh` lists `.pulumi/stacks/` first and
refuses when it finds a directory this project does not own, printing the `pulumi stack rm
--remove-backups` path instead — that is the Pulumi standards' MUST-8; ask Athena for it. A listing
it cannot read counts as shared. The OIDC provider is shared across the whole account, so it is
deleted only when no IAM role still trusts it.

The order is the second trap. The bucket holds the state `pulumi destroy` reads, so `teardown.sh`
exports every stack in the backend and refuses while any of them still holds a resource. Then it
asks you to type the bucket name, and refuses outright when nothing can answer. There is
deliberately no `--yes` here.

**A KMS key cannot be deleted on demand.** `schedule-key-deletion` puts it in `PendingDeletion` for
30 days, and until then `aws kms cancel-key-deletion` reverses it.

`destroy` also leaves the GitHub repository variables behind, naming infrastructure that no longer
exists. Delete them in the repository settings, or with `gh variable delete <NAME>`, and take this
site's origin out of the backend's `corsOrigins`.

## Layout

```
infra/
├── justfile                    lock install test lint preview up sync-github destroy teardown
├── bootstrap/state-bucket.sh   plain aws CLI; the state bucket, its KMS key, the GitHub OIDC provider
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
├── scripts/sync-github.sh      copies stack outputs into GitHub repository variables
└── pulumi/
    ├── Pulumi.yaml             project, runtime, and the S3 backend URL
    ├── Pulumi.dev.yaml         per-stack configuration; a second environment is a sibling file
    ├── package.json            the stack's own dependencies; the application's are at the root
    ├── index.ts                what Pulumi runs: load the configuration, build the stack
    ├── src/
    │   ├── config.ts           typed, validated StackConfig; the only place a key is named
    │   ├── policy.ts           every IAM and bucket policy, as pure functions of ARNs
    │   ├── stack.ts            composition root and exports
    │   └── components/         site-bucket, cdn, identities
    └── tests/                  vitest against pulumi.runtime.setMocks — no credentials needed
```

## What the stack provisions

| Component | Resources | Cost posture |
| --- | --- | --- |
| `site-bucket` | one S3 bucket: all four public-access blocks, ACLs off (`BucketOwnerEnforced`), SSE-S3, no versioning, no website hosting | cents: a bundle is a few megabytes |
| `cdn` | an Origin Access Control (SigV4, always), a response headers policy, the distribution, and the bucket policy admitting only this distribution | cents at low traffic; CloudFront bills per request and per GB served |
| `identities` | two keyless GitHub roles — deploy and preview — and their policies | free |

**Nothing here bills while idle except the bucket's storage**, which for a bundle is a rounding
error. CloudFront bills for requests and data out, so the cost follows traffic, and its free tier
covers a small site. **No figure is quoted here on purpose**: prices change by region and over time
without this file changing. Price it in the AWS Pricing Calculator for
`{{ cookiecutter.aws_region }}` and read the real number off your first bill.

Each choice in the distribution, and why:

| Setting | Value | Why |
| --- | --- | --- |
| Origin access | Origin Access Control, `sigv4`, `always` | The bucket is private; CloudFront signs every request, and the bucket policy admits `cloudfront.amazonaws.com` only with `AWS:SourceArn` equal to this distribution. |
| Viewer protocol | `redirect-to-https` | Plain HTTP is answered with a redirect, never with content. |
| SPA fallback | 403 and 404 → `/index.html`, 200, not cached | The router is the browser's. S3 answers a missing key with 403 when the reader cannot list the bucket — CloudFront cannot, deliberately — so both are mapped. The cost: a real access-denied from the bucket is served as the app's shell. |
| Caching | managed `CachingOptimized`, compression on | Hashed `assets/` carry `immutable` from CD and are cached for a year; `index.html` carries `no-cache` and is revalidated. |
| Headers | HSTS (two years, subdomains, no preload), `nosniff`, `X-Frame-Options: DENY`, `strict-origin-when-cross-origin` | What a static site can promise without knowing its API. No CSP: its `connect-src` is the API's origin, a build-time value this stack never sees. |
| Price class | `PriceClass_100` | North America and Europe edges, the cheapest class. Everyone else is still served, from farther away. |
| Protocols | `http2and3`, IPv6 on | Free, and faster for clients that can use them. |
| Certificate | CloudFront's default `*.cloudfront.net` | Needs nothing. A domain of your own is above. |

The site bucket is **not versioned**: every object in it is a build output CD can write again from
any commit, so a rollback is CD re-run at the earlier commit, and versioning would keep every
deploy's deleted files as billed noncurrent versions.

## Who owns what

The stack owns the *shape*: the bucket and its settings, the distribution, its caching and headers,
and who may read and write. **The CD pipeline owns the bucket's contents**, and the stack declares
no object, so there is nothing for the two to fight over. Every preview, apply and destroy still
passes `--refresh`, so the plan is read against what is really there — a distribution somebody
edited in the console shows up as a diff rather than being silently reverted on the next apply.

How CD writes, in `.github/workflows/cd.yml`: two `aws s3 sync`s, because the two halves of a Vite
build want opposite caching and one sync sets one `Cache-Control`. `assets/` first, cached for a
year as `immutable`, without `--delete` so the live index keeps finding its files; then the rest,
`index.html` above all, as `no-cache`, with `--delete` for root files and `assets/` excluded. Then
an invalidation of `/index.html` and `/`, polled to `Completed` against a twenty-minute deadline —
not `aws cloudfront wait`, whose fixed cap is shorter than a slow invalidation — and only then the
previous deploy's hashed files are pruned. Last, a smoke test: `/` answers 200 and names this
build's entry script, and `/some/deep/route` answers 200 through the SPA fallback.

## Prepackaged, or manual

**It is prepackaged when the value or the decision already exists inside this repository, and manual
when it needs an account, a bill, a name you own, another repository, or a human looking at
something.**

Prepackaged — generated, wired together, and covered by a test:

| | Where it lives |
| --- | --- |
| The bucket, the distribution, the headers, the roles | `pulumi/src/stack.ts` |
| Every repository variable the pipelines read but one | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| The state bucket, its KMS key and the GitHub OIDC provider | `bootstrap/state-bucket.sh` |
| Removing them again, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| Build, upload, invalidate, smoke test | `.github/workflows/cd.yml` |
| The stack's tests and a preview on every pull request | `.github/workflows/infra.yml` |

The stack's exports, the variables `sync-github.sh` sets and the `vars.` names the workflows read
have to agree exactly, so `pulumi/tests/contract.test.ts` asserts that they do. A name changed in
one of the three is then a failing test rather than a deploy job that is skipped forever.

Manual, and no template can change that:

| | Why it cannot be shipped |
| --- | --- |
| An AWS account per environment, with billing | It costs money and names a payer |
| `./bootstrap/state-bucket.sh`, then `pulumi stack init` | A stack cannot create the bucket that holds its own state |
| `githubSubjectPrefix` | GitHub decides it per repository; `sync-github.sh` checks it |
| Reading `just infra-preview` before the first apply | The point of the step is that a human looked |
| `API_URL` | It is the backend's address, and the backend is another repository |
| The backend's `corsOrigins`{% if cookiecutter.include_auth == "yes" %} and `refreshTransport`{% endif %} | They are the backend stack's configuration |
| Protection rules on the `production` GitHub environment | The deploy role trusts that environment; its rules are the gate on a deploy |
| A domain, its DNS and its certificate | You own the name; nothing here can |
| Confirming `just infra-teardown` | Deleting a state bucket is irreversible |
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

Do not create an IAM user or an access key for any of this. You authenticate as yourself; CI
authenticates by exchanging GitHub's OIDC token. **Deploy** trusts a token from the `production`
environment of `{{ cookiecutter.github_repository }}` and can write the site bucket's objects and
invalidate this distribution — nothing else. **Preview** trusts a token from a pull request in the
same repository, can read the account (`ReadOnlyAccess`) and this project's state, and can write
only this project's lock objects.

## Adding a second environment

**One AWS account per environment is the recommendation**, production in an account of its own. A
second environment is then a new `Pulumi.<stack>.yaml` with its own `accountId`, and a
`state-bucket.sh` run against that account (set `AWS_ACCOUNT_ID` and `AWS_REGION`). `Pulumi.yaml`'s
backend URL names *this* account's bucket, and `PULUMI_BACKEND_URL` overrides it, so work on the
other account's stack with `PULUMI_BACKEND_URL=s3://pulumi-state-<account>-<region>?region=<region>`
set — and give its preview job the same variable. A `prod` stack makes the site bucket refuse a
`destroy` while it still holds objects.

## Related

- Ask Athena for the **Pulumi standards** — the graded rules this stack is built to satisfy — and
  for where Pulumi state lives on AWS (ADR-0021), and why this stack is TypeScript (ADR-0020).
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi AWS provider (`@pulumi/aws` v7) — https://www.pulumi.com/registry/packages/aws/
- Pulumi unit testing (`pulumi.runtime.setMocks`) — https://www.pulumi.com/docs/iac/concepts/testing/unit/
- CloudFront origin access control — https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-restricting-access-to-s3.html
- GitHub Actions OIDC in AWS — https://docs.github.com/en/actions/security-for-github-actions/security-hardening-your-deployments/configuring-openid-connect-in-amazon-web-services
