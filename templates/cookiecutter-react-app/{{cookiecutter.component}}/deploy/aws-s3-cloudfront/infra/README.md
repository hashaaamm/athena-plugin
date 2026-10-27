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
| Node 24 and pnpm | `just infra-test`, `just infra-lint`, every `pulumi` command (it runs `index.ts`), and the first deploy's build | `node --version`, `pnpm --version` |
| `pulumi` CLI | preview, up, destroy, stack outputs | `pulumi version` |
| `aws` CLI v2 | the bootstrap and teardown scripts, the first deploy's upload, and your own authentication | `aws --version` |
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

Every command below runs from this component's folder, `{{ cookiecutter.component }}/`; from the
repository root the same recipes are `just {{ cookiecutter.component }} infra-up` and so on.

```
0. cd infra && just lock                           resolves pnpm-lock.yaml; commit it
1. aws + pulumi installed and authenticated        nothing is created
2. an AWS account for this environment             the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain aws CLI; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
5. pulumi config set --plaintext githubSubjectPrefix …   the OIDC subject GitHub issues for this repository
   pulumi config set --plaintext apiUrl https://…        the backend's address, which the site serves under /api/*
6. just infra-preview, then just infra-up          read it, by a human; creates the bucket, the distribution, the roles
7. just infra-first-deploy                         builds the real bundle here and publishes it: the site is live
8. just infra-sync-github                          copies the stack's outputs into {{ cookiecutter.__component_var }}_* repository variables
   protect the `infrastructure` environment        required reviewers, main only — GitHub settings
9. git push                                        CD owns every deploy, {{ cookiecutter.component }}-infra.yml every infra change
```

Steps 3 to 8 run from your machine, once per environment. Every infrastructure change after them
merges and is applied by `{{ cookiecutter.component }}-infra.yml` — see
[After setup, CI applies every change](#after-setup-ci-applies-every-change).

{% if cookiecutter.include_auth == "yes" -%}
And on the backend's side, in its own component: `refreshTransport=cookie`, and `corsOrigins` set to
this stack's `site_url` — see [The API on the site's own origin](#the-api-on-the-sites-own-origin)
for why the second is still needed.
{%- else -%}
On the backend's side, nothing: the site serves the API on its own origin, so the browser needs no
CORS — see [The API on the site's own origin](#the-api-on-the-sites-own-origin).
{%- endif %}

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

**One backend, one directory per component.** The state bucket holds a directory per Pulumi project,
and this stack's project is `{{ cookiecutter.__name }}` — the project's slug and this component's
name — so it sits beside the backend's, and beside any other component's, without a rename. Every
resource it creates carries the same name, and every repository variable it syncs is prefixed
`{{ cookiecutter.__component_var }}_`.

Step 5 is two settings. The first is there because GitHub decides the shape of the `sub` claim in
this repository's OIDC tokens, and the CI roles trust exactly one. Many repositories now get the
immutable form, `repo:<owner>@<owner-id>/<repo>@<repo-id>`, so that a renamed or re-registered
account cannot inherit the trust; a role written for the name-only `repo:<owner>/<repo>` then admits
no token, and every CD run fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity".
Ask the repository, after it exists on GitHub:

```bash
pulumi -C infra/pulumi config set --plaintext githubSubjectPrefix \
  "$(gh api repos/{{ cookiecutter.github_repository }}/actions/oidc/customization/sub --jq '.sub_claim_prefix // empty')"
```

Empty is the name-only form. `just infra-sync-github` makes the same request and refuses to un-gate
CD while the stack trusts anything else, printing the command that fixes it.

The second is **`apiUrl`**, the backend's own address — `https://<its host>`, its stack's
`service_url`. By default the distribution serves that API on the site's own origin, under `/api/*`
and `/health/*`, and the bundle calls the page's own origin; `apiSameOrigin` turns that off. Either
way the stack derives what Vite compiles into the bundle as `VITE_API_URL` and exports it as
`api_url` — `/` while the site serves the API, `apiUrl` itself when the browser calls it directly —
so it is stack configuration and not something typed into GitHub: the first deploy builds with it,
and `just infra-sync-github` copies it into `{{ cookiecutter.__component_var }}_API_URL`, which CD
builds with. A bundle built without one calls `{{ cookiecutter.api_url }}`, deploys cleanly, and
fails every request — so the first deploy, the sync and CD each refuse to go on without it. To point
the site at a different backend later, change `apiUrl` in a pull request and merge it, which applies
it. With the API on the site's origin that is the whole change: the bundle calls `/` either way.
Calling it directly, run `just infra-sync-github` afterwards, and re-run CD.

`just infra-up` asks for confirmation and **refuses to run with nothing there to confirm** —
"--yes must be passed in to proceed when running in non-interactive mode". The recipe does not pass
`--yes` for you; this command creates billable resources and a human reading the preview is the
point. From a pipeline, or from an agent with no terminal, type it: `just infra-up dev --yes`.
Creating a CloudFront distribution takes several minutes; `pulumi up` waits for it to deploy. The
bucket is empty afterwards, and CloudFront answers every path with a 403 from S3: the `index.html`
every route is served does not exist yet.

**Step 7 ships the real build, from your machine.** `just infra-first-deploy` refuses unless the stack
has been applied with the `apiUrl` it is configured with, and refuses when the application has
uncommitted changes (anything outside `infra/`). Then it builds the bundle with the commands CD uses —
`pnpm install --frozen-lockfile`, `pnpm build` with `VITE_API_URL` set to the `api_url` export — and
publishes it with `scripts/publish.sh`, the same script CD runs: the two syncs, the invalidation, the
prune and the smoke test of `/`, a deep route and, while the site serves the API, `/health/live`
through it. It asks before it uploads; `just infra-first-deploy dev --yes` is the unattended form. It
needs Node 24, pnpm and the `aws` CLI on the host, and your credentials for
`{{ cookiecutter.aws_account_id }}`.

Step 8 un-gates CD, and step 9 hands it every deploy from then on.{% if cookiecutter.include_auth == "yes" %} The backend's stack needs
`refreshTransport=cookie` — this app keeps the refresh token in an HttpOnly cookie, and a backend
sending it in the body has nothing for the app to keep — and `corsOrigins` naming this site's origin,
the `site_url` output (`https://<id>.cloudfront.net`), which the cookie routes' CSRF check reads.{% else %} With the API on the site's
origin the backend needs no `corsOrigins` for this app; calling it directly, its `corsOrigins` must
name the `site_url` output (`https://<id>.cloudfront.net`), or every request from the browser fails
CORS while the API answers it correctly.{% endif %}

### The API on the site's own origin

**The default, and what keeps a session across a reload without a domain of your own.** The
distribution has a second origin, the host in `apiUrl`, and two ordered behaviours in front of it,
`/api/*` and `/health/*` — the API's routes and the health endpoints the dashboard reads. The bundle
is built with `VITE_API_URL=/`, and the app resolves that against the page's own origin, so it calls
`https://<id>.cloudfront.net/api/v1/…`, and CloudFront forwards the request to the API. The same
bundle works unchanged on a domain of your own later.

Why it is there: `*.cloudfront.net` and the backend's own hostname — `*.on.aws` for ECS Express
Mode, `*.run.app` for Cloud Run — are two sites (both suffixes are on the Public Suffix List, so even
two names under one of them are two sites). Called directly, the browser never sends the API's
`SameSite=Strict` refresh cookie back to it, and every reload signs the user out. Under one origin
the cookie is first-party and the session survives. Locally nothing changes: `localhost:3000` and
`localhost:8000` are one site already — a port does not make a second one — so `just dev` calls the
API at its own address.

How the API behaviours are set, and why:

| Setting | Value | Why |
| --- | --- | --- |
| Cache policy | managed `CachingDisabled` | No answer is ever served to the wrong caller. |
| Origin request policy | managed `AllViewerExceptHostHeader` | Every header, cookie and query string reaches the API — `Authorization`, the refresh cookie, `Origin` — except `Host`. ECS Express Mode's load balancer and Cloud Run's front end route on `Host`, and the API's certificate names its own host, so it is sent its own name. |
| Methods | all seven | The API takes writes; the site's own behaviour takes only `GET` and `HEAD`. |
| Viewer protocol | `https-only` | Not a redirect: a redirected `POST` arrives as a `GET` with no body. |
| Origin | `https-only`, TLS 1.2, 60-second read timeout | Sixty seconds is the most CloudFront allows without a quota increase. A request the API takes longer over is a 504. |
| Response headers | the site's policy | HSTS, `nosniff`, `DENY` framing on the API's answers too. |

**The SPA fallback moved with it.** It is a viewer-request CloudFront Function on the site's own
behaviour — a path whose last segment has no extension is served `/index.html` — not the
distribution's custom error responses. Those apply to every behaviour, so they would have turned the
API's own 403s and 404s into the app's shell with a 200. The cost: a client-side route whose last
segment has a dot (`/users/jane.doe`) is looked up as a file, and S3 answers 403; and a client-side
route under `/api/` or `/health/` never reaches the app.

**What the backend's stack needs.**{% if cookiecutter.include_auth == "yes" %} `refreshTransport=cookie`, and `corsOrigins` still set to
this site's `site_url`. Same origin needs no CORS, but both backend templates' cookie routes check
the request's `Origin` against that list, and a browser sends `Origin` on every `POST`, same-origin
ones included — so without it, sign-in sets the cookie and every refresh is a 403. Both backend
stacks also refuse `refreshTransport=cookie` with no `corsOrigins` at all.{% else %} Nothing for this app: same-origin requests
need no CORS, so `corsOrigins` does not have to name this site.{% endif %} The API stays reachable at its own address,
for anything that is not this browser app.

What it costs: every API request is one more hop, through the edge, and CloudFront bills for those
requests and their bytes as it does for the site's. **Turn it off** with
`pulumi config set apiSameOrigin false` and an apply, when the site and the API share a domain you
own — [below](#a-domain-of-your-own) — and the browser then calls `apiUrl` directly.

### After setup, CI applies every change

Steps 3 to 8 cannot run from CI: the identity CI applies with — the apply role,
`{{ cookiecutter.__name }}-apply-<env>` — is one of the things step 6 creates. So they run from an
operator's machine, once per environment, and that is the one exception the Pulumi standards' MUST-9
makes. From then on **every infrastructure change merges and is applied by
`{{ cookiecutter.component }}-infra.yml`**: a pull request gets the stack tests and the preview's
diff as a comment, and the merge to `main` runs `pulumi up --refresh` on the `dev` stack as the
apply role. Applying from a laptop after setup is the exception MUST-9 forbids — it skips the
review the preview exists for and leaves no audit trail. `just infra-up` stays for setting up an
environment.

The workflow watches the stack (`infra/`) and the component's workflows, not the application, so
a merge that changes only the app is published by CD and starts no apply.

The apply role trusts a token from the `infrastructure` GitHub environment of
`{{ cookiecutter.github_repository }}` and nothing else, and it holds `AdministratorAccess`. That
is deliberate. `pulumi up` creates and deletes everything in this stack — the site bucket and its
policy, the distribution, the IAM roles and their policies — and writes its own state; a
hand-written list covering all of it still has to write IAM, the apply role's own policy among it,
and the right to write IAM is the right to grant anything. A narrower list would not be narrower in
effect. It would only break, as an AccessDenied halfway through an apply on `main`, the first time
the stack gained a resource type. What bounds the role is the environment and the account:

- **Protect the `infrastructure` environment before step 9**, in the repository's settings under
  Environments: required reviewers, and deployment branches limited to `main`. GitHub creates an
  environment with no rules the first time a job names it, and an unprotected one is an apply
  nobody approved. No stack can set these rules — they belong to the repository, and the reviewers
  are people.
- **One AWS account per environment** keeps the role's reach to the environment it applies.

Applies queue, one at a time, and are never cancelled: a `pulumi up` stopped halfway leaves
resources half-changed and the stack locked. When one fails, `main` is ahead of the account until
the fix merges — fix forward with another pull request, and the merge applies it.

### A domain of your own

**Still the better production setup**, though the API on the site's own origin means a session
survives a reload without one. With the site on `app.example.com` and the API on `api.example.com`,
the two are one site, the browser can call the API directly — no extra hop through the edge, no
60-second ceiling on a request — and `apiSameOrigin false` builds the bundle with `apiUrl` set to the
API's address. Keeping `apiSameOrigin` on with a domain also works: the bundle calls whatever origin
served it.

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
case.** It is named for the account and region, so every component of this repository deployed
into this account shares it — the backend's included — and deleting it deletes their state too. `teardown.sh` lists `.pulumi/stacks/` first and
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

**Taking down the whole repository** is therefore one order, not one command per component:
`just <component> infra-destroy` for every component and every stack; then, from each component but
the last, `pulumi -C infra/pulumi stack rm <stack> --remove-backups` to remove its emptied state;
and `just infra-teardown` last, from the one component whose state is all the bucket still holds.
`teardown.sh` refuses at any earlier point, and says which project is still there.

`destroy` also leaves the GitHub repository variables behind, naming infrastructure that no longer
exists. Delete this component's — every one is prefixed `{{ cookiecutter.__component_var }}_` — in
the repository settings, or with `gh variable delete <NAME>`, and take this site's origin out of the
backend's `corsOrigins`.

## Layout

```
infra/
├── justfile                    lock install test lint preview up first-deploy sync-github destroy teardown
├── bootstrap/state-bucket.sh   plain aws CLI; the state bucket, its KMS key, the GitHub OIDC provider
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
├── scripts/first-deploy.sh     builds the bundle here and publishes it, before CD exists
├── scripts/publish.sh          upload, invalidate, prune, smoke test — run by CD and the first deploy
├── scripts/sync-github.sh      copies stack outputs into prefixed GitHub repository variables
└── pulumi/
    ├── Pulumi.yaml             project, runtime, and the S3 backend URL
    ├── Pulumi.dev.yaml         per-stack configuration; a second environment is a sibling file
    ├── package.json            the stack's own dependencies; the application's are in the folder above
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
| `cdn` | an Origin Access Control (SigV4, always), a response headers policy, the SPA routing function, the distribution — with the API as its second origin by default — and the bucket policy admitting only this distribution | cents at low traffic; CloudFront bills per request and per GB served, the API's requests included, and per function invocation |
| `identities` | three keyless GitHub roles — deploy, preview and apply — and their policies | free |

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
| SPA fallback | a viewer-request CloudFront Function on the default behaviour: a path with no extension in its last segment is served `/index.html` | The router is the browser's. A function, not 403/404 error responses, because those apply to every behaviour and would serve the API's own errors as the app's shell. A rewritten request is cached as `/index.html`, which carries `no-cache` and is what the publish script invalidates. |
| The API | `/api/*` and `/health/*` to `apiUrl`, uncached, every viewer header but `Host` | One origin for the app and the API — [above](#the-api-on-the-sites-own-origin). Absent with `apiSameOrigin false`. |
| Caching | managed `CachingOptimized`, compression on | Hashed `assets/` carry `immutable` from CD and are cached for a year; `index.html` carries `no-cache` and is revalidated. |
| Headers | HSTS (two years, subdomains, no preload), `nosniff`, `X-Frame-Options: DENY`, `strict-origin-when-cross-origin` | What a static site can promise without knowing its API. No CSP yet: a wrong one breaks the page more thoroughly than a missing one. With the API on this origin, `connect-src 'self'` is the one to start from. |
| Price class | `PriceClass_100` | North America and Europe edges, the cheapest class. Everyone else is still served, from farther away. |
| Protocols | `http2and3`, IPv6 on | Free, and faster for clients that can use them. |
| Certificate | CloudFront's default `*.cloudfront.net` | Needs nothing. A domain of your own is above. |

The site bucket is **not versioned**: every object in it is a build output CD can write again from
any commit, so a rollback is CD re-run at the earlier commit, and versioning would keep every
deploy's deleted files as billed noncurrent versions.

## Who owns what

The stack owns the *shape*: the bucket and its settings, the distribution, its caching and headers,
and who may read and write. **The publish script owns the bucket's contents** — run once by
`just infra-first-deploy`, and by CD on every merge after that — and the stack declares no object,
so there is nothing for the two to fight over. Every preview, apply and destroy still
passes `--refresh`, so the plan is read against what is really there — a distribution somebody
edited in the console shows up as a diff rather than being silently reverted on the next apply.

How it writes, in `scripts/publish.sh`: two `aws s3 sync`s, because the two halves of a Vite
build want opposite caching and one sync sets one `Cache-Control`. `assets/` first, cached for a
year as `immutable`, without `--delete` so the live index keeps finding its files; then the rest,
`index.html` above all, as `no-cache`, with `--delete` for root files and `assets/` excluded. Then
an invalidation of `/index.html` and `/`, polled to `Completed` against a twenty-minute deadline —
not `aws cloudfront wait`, whose fixed cap is shorter than a slow invalidation — and only then the
previous deploy's hashed files are pruned. Last, a smoke test: `/` answers 200 and names this
build's entry script, `/some/deep/route` answers 200 through the SPA fallback, and — when the bundle
was built with `/` — `/health/live` answers the API's 200 JSON through the `/health/*` behaviour, not
the app's shell.

## Prepackaged, or manual

**It is prepackaged when the value or the decision already exists inside this repository, and manual
when it needs an account, a bill, a name you own, another repository, or a human looking at
something.**

Prepackaged — generated, wired together, and covered by a test:

| | Where it lives |
| --- | --- |
| The bucket, the distribution, the headers, the roles | `pulumi/src/stack.ts` |
| Every repository variable the pipelines read, the API's address included | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| The state bucket, its KMS key and the GitHub OIDC provider | `bootstrap/state-bucket.sh` |
| Removing them again, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| The first deploy: build, then publish | `scripts/first-deploy.sh`, via `just infra-first-deploy` |
| Upload, invalidate, prune, smoke test | `scripts/publish.sh`, run by the first deploy and by CD |
| Build and publish on every merge | `.github/workflows/{{ cookiecutter.component }}-cd.yml` at the repository root |
| The stack's tests and a preview on every pull request, and the apply of every change merged to `main` | `.github/workflows/{{ cookiecutter.component }}-infra.yml` |

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
| `apiUrl`, and whether the site serves it (`apiSameOrigin`) | It is the backend's address, which this component cannot know until the backend is deployed |
| The backend's `corsOrigins`{% if cookiecutter.include_auth == "yes" %} and `refreshTransport`{% endif %} | They are the backend stack's configuration |
| Protection rules on the `production` GitHub environment | The deploy role trusts that environment; its rules are the gate on a deploy |
| Protection rules on the `infrastructure` GitHub environment | The apply role trusts that environment; its rules are the gate on every apply after setup |
| A domain, its DNS and its certificate | You own the name; nothing here can |
| Confirming `just infra-teardown` | Deleting a state bucket is irreversible |
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

Do not create an IAM user or an access key for any of this. You authenticate as yourself; CI
authenticates by exchanging GitHub's OIDC token. **Deploy** trusts a token from the `production`
environment of `{{ cookiecutter.github_repository }}` and can write the site bucket's objects and
invalidate this distribution — nothing else. **Preview** trusts a token from a pull request in the
same repository, can read the account (`ReadOnlyAccess`) and this project's state, and can write
only this project's lock objects. **Apply** trusts a token from the `infrastructure` environment of
the same repository and holds `AdministratorAccess`, for the reasons in
[After setup, CI applies every change](#after-setup-ci-applies-every-change).

## Adding a second environment

**One AWS account per environment is the recommendation**, production in an account of its own. A
second environment is then a new `Pulumi.<stack>.yaml` with its own `accountId`, and a
`state-bucket.sh` run against that account (set `AWS_ACCOUNT_ID` and `AWS_REGION`). `Pulumi.yaml`'s
backend URL names *this* account's bucket, and `PULUMI_BACKEND_URL` overrides it, so work on the
other account's stack with `PULUMI_BACKEND_URL=s3://pulumi-state-<account>-<region>?region=<region>`
set — and give its preview and apply jobs the same variable; `{{ cookiecutter.component }}-infra.yml` previews and applies
the `dev` stack, so a second stack is a second pair of jobs. A `prod` stack makes the site bucket refuse a
`destroy` while it still holds objects.

## Related

- Ask Athena for the **Pulumi standards** — the graded rules this stack is built to satisfy — and
  for where Pulumi state lives on AWS (ADR-0021), and why this stack is TypeScript (ADR-0020).
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi AWS provider (`@pulumi/aws` v7) — https://www.pulumi.com/registry/packages/aws/
- Pulumi unit testing (`pulumi.runtime.setMocks`) — https://www.pulumi.com/docs/iac/concepts/testing/unit/
- CloudFront origin access control — https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-restricting-access-to-s3.html
- GitHub Actions OIDC in AWS — https://docs.github.com/en/actions/security-for-github-actions/security-hardening-your-deployments/configuring-openid-connect-in-amazon-web-services
