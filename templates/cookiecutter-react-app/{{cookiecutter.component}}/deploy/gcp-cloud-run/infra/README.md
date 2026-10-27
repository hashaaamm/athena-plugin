# Infrastructure

Pulumi in TypeScript, the application's own language (ADR-0020). One component per concern, in
`pulumi/src/components/`, composed in `pulumi/src/stack.ts`, which reads top to bottom as a
dependency graph.

Nothing here has been applied yet. A freshly generated repository has a stack that type-checks and
unit-tests without any cloud access, and has never created a resource. Everything below is what
turns that into a site on GCP — Cloud Run serving the nginx image in `docker/` — in the order it
has to happen.

## What runs where

Unlike the application, whose recipes all run in a container, **every recipe in `infra/` runs on
your machine.** Pulumi runs the TypeScript program with the Node and the `node_modules` on the
machine running `pulumi`, and `pulumi`, `gcloud` and `gh` act with your own credentials; putting
them in a container would mean mounting those credentials into it. So this directory, and only this
directory, has host prerequisites:

| Tool | Needed for | Check |
| --- | --- | --- |
| Node 24 and pnpm | `just infra-test`, `just infra-lint`, and every `pulumi` command (it runs `index.ts`) | `node --version`, `pnpm --version` |
| Docker with `buildx` | `just infra-first-deploy`, which builds and pushes the first image | `docker buildx version` |
| `pulumi` CLI | preview, up, destroy, stack outputs | `pulumi version` |
| `gcloud` | the bootstrap and teardown scripts, the first deploy's push, and your own authentication | `gcloud --version` |
| `gh` | `just infra-sync-github` | `gh auth status` |
| `python3` | `just infra-teardown` only (it reads JSON from `pulumi`) | `python3 --version` |

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
1. gcloud + pulumi installed and authenticated     nothing is created
2. a GCP project with billing attached             the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain gcloud; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
5. pulumi config set --plaintext apiUrl https://…  the backend's address, which the site serves under /api/
6. just infra-preview, then just infra-up          read it, by a human; creates everything but the service
7. just infra-first-deploy                         builds and pushes the real image, creates the service on it
8. just infra-sync-github                          copies the stack's outputs into {{ cookiecutter.__component_var }}_* repository variables
   protect the `infrastructure` environment        required reviewers, main only — GitHub settings
9. git push                                        CD owns every image, {{ cookiecutter.component }}-infra.yml every infra change
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

There is no `githubSubjectPrefix` step here, unlike on AWS: Workload Identity Federation matches the
token's `repository` claim, which is the same whichever form of `sub` GitHub issues.

Step 3 is a script rather than a stack because a stack cannot create the bucket that holds its own
state. It creates the bucket `gs://{{ cookiecutter.gcp_project_id }}-pulumi-state` and the KMS key ring `pulumi` with
the key `stack` that encrypts stack configuration, and enables the three APIs the stack cannot
enable for itself. **They are the same names the backend templates' bootstrap uses**, so if the
backend this site calls already deploys into this project, step 3 finds them and changes nothing —
run it anyway; it is idempotent.

**One backend, one directory per component.** The state bucket holds a directory per Pulumi project,
and this stack's project is `{{ cookiecutter.__name }}` — the project's slug and this component's
name — so it sits beside the backend's, and beside any other component's, without a rename. Every
resource it creates carries the same name, and every repository variable it syncs is prefixed
`{{ cookiecutter.__component_var }}_`.

Step 5 is **`apiUrl`**, the backend's own address — `https://<its host>`, its stack's
`service_url`; run `just infra-up` after setting it. By default the site serves that API on its own
origin: the image's nginx proxies `/api/` and `/health/` to it, and the bundle calls the page's own
origin; `apiSameOrigin` turns that off. Either way the stack derives what Vite compiles into the
bundle as `VITE_API_URL` and exports it as `api_url` — `/` while the site serves the API, `apiUrl`
itself when the browser calls it directly — so it is stack configuration and not something typed
into GitHub: the first deploy builds the image with it, and `just infra-sync-github` copies it into
`{{ cookiecutter.__component_var }}_API_URL`, which CD builds with. A bundle built without one calls
`{{ cookiecutter.api_url }}`, deploys cleanly, and fails every request — so the first deploy, the sync
and CD each refuse to go on without it. To point the site at a different backend later, change
`apiUrl` in a pull request and merge it, which applies it. With the API on the site's origin that
is the whole change: the host is the service's runtime environment, not part of the image, and the
apply rolls a new revision on the image CD last deployed. Calling it directly, run
`just infra-sync-github` afterwards, and re-run CD.

`just infra-up` asks for confirmation and **refuses to run with nothing there to confirm**. The
recipe does not pass `--yes` for you; this command creates billable resources and a human reading
the preview is the point. From a pipeline, or from an agent with no terminal:
`just infra-up dev --yes`. It creates the APIs, the registry, the accounts and Workload Identity —
**and no Cloud Run service**, because there is no real image to run yet, and a service on a public
placeholder would answer 200 while serving nothing of yours.

**Step 7 ships the real build, from your machine.** `just infra-first-deploy` refuses unless the
stack has been applied (the registry exists) with the `apiUrl` it is configured with, and refuses
when the application has uncommitted changes (anything outside `infra/`). Then it builds
`docker/Dockerfile.prod` for `linux/amd64` with `docker buildx build`, with the `api_url` export as
the `VITE_API_URL` build argument CD passes, tags it `<image_repo>/<service>:<commit>` — the name CD
pushes — and pushes it after `gcloud auth configure-docker`. It sets `serviceImage` to that image,
runs `pulumi up --refresh` (which asks for confirmation; `just infra-first-deploy dev --yes` is the
unattended form) — the apply that creates the service gives it `API_UPSTREAM_HOST` too — and
smoke-tests `/`, a deep route and, while the site serves the API, `/health/live` through it. The Dockerfile's build stage runs on your
machine's own platform — the bundle is the same bytes on every architecture — so an Apple-silicon
laptop runs Node natively rather than under emulation. Commit `pulumi/Pulumi.dev.yaml` afterwards:
`serviceImage` and `apiUrl` are in it.

From then on the image is CD's: the service declares it with `ignoreChanges`, so a later change to
`serviceImage` does nothing, and `--refresh` on every apply keeps the image CD last deployed.

Step 8 un-gates CD, and step 9 hands it every image from then on.{% if cookiecutter.include_auth == "yes" %} The backend's stack needs
`refreshTransport=cookie` — this app keeps the refresh token in an HttpOnly cookie, and a backend
sending it in the body has nothing for the app to keep — and `corsOrigins` naming this site's origin,
the `site_url` output (`https://<service>-<hash>.<region>.run.app`), which the cookie routes' CSRF
check reads.{% else %} With the API on the site's origin the backend needs no `corsOrigins` for this app; calling it
directly, its `corsOrigins` must name the `site_url` output
(`https://<service>-<hash>.<region>.run.app`), or every request from the browser fails CORS while
the API answers it correctly.{% endif %}

### The API on the site's own origin

**The default, and what keeps a session across a reload without a domain of your own.** The image's
nginx proxies two prefixes, `/api/` and `/health/` — the API's routes and the health endpoints the
dashboard reads — to the host in `apiUrl`, which the stack gives the service as `API_UPSTREAM_HOST`.
Everything else is the SPA, with its fallback to `index.html` as before. The bundle is built with
`VITE_API_URL=/`, and the app resolves that against the page's own origin, so it calls
`https://<service>-<hash>.<region>.run.app/api/v1/…`, and nginx forwards the request to the API.
`/` rather than the site's URL because that URL does not exist until the first deploy has created
the service on an image already built — and the same bundle then works unchanged on a domain of
your own.

Why it is there: `run.app` is on the Public Suffix List, so every `*.run.app` hostname is a site of
its own, and the site and the backend are two of them (as are `*.cloudfront.net` and `*.on.aws`).
Called directly, the browser never sends the API's `SameSite=Strict` refresh cookie back to it, and
every reload signs the user out. Under one origin the cookie is first-party and the session
survives. Locally nothing changes: `localhost:3000` and `localhost:8000` are one site already — a
port does not make a second one — so `just dev` calls the API at its own address.

How the proxy is set, in `docker/default.conf.template`, and why:

| Setting | Value | Why |
| --- | --- | --- |
| Upstream | `proxy_pass https://$api_host`, with no URI part | The request's own path and query reach the API unchanged. |
| Host and SNI | the API's own host: `proxy_set_header Host`, `proxy_ssl_server_name on`, `proxy_ssl_name` | Cloud Run's front end routes on `Host` and presents a certificate for the service's own name, so the API is sent its own name, never the site's. Every other header passes through: `Authorization`, the refresh cookie, `Origin`. |
| TLS | `proxy_ssl_verify on` against the image's CA bundle, TLS 1.2 and 1.3 | nginx verifies nothing about an upstream unless told to. |
| Resolution | `resolver` from the container's own `/etc/resolv.conf`, re-resolved after 60 s, IPv4 only | A variable host is looked up when a request needs it; a literal one would be resolved once at start and kept for the instance's life. |
| Timeouts | 10 s to connect, 60 s to answer | The same as CloudFront on the AWS target. A request the API takes longer over is a 504. |
| Body size | 32 MB | Cloud Run's own ceiling on an HTTP/1 request. nginx's default, 1 MB, would refuse an upload the API accepts. |
| Unset upstream | `/api/` and `/health/` answer 404 | The image's default `API_UPSTREAM_HOST` is empty, so the template always renders, and the image runs anywhere. |

The upstream is runtime configuration, not part of the image: the image CD builds is the same
whichever backend it fronts, and changing the backend is an apply, not a rebuild. A client-side
route under `/api/` or `/health/` never reaches the app.

**What the backend's stack needs.**{% if cookiecutter.include_auth == "yes" %} `refreshTransport=cookie`, and `corsOrigins` still set to
this site's `site_url`. Same origin needs no CORS, but both backend templates' cookie routes check
the request's `Origin` against that list, and a browser sends `Origin` on every `POST`, same-origin
ones included — so without it, sign-in sets the cookie and every refresh is a 403. Both backend
stacks also refuse `refreshTransport=cookie` with no `corsOrigins` at all.{% else %} Nothing for this app: same-origin requests
need no CORS, so `corsOrigins` does not have to name this site.{% endif %} The backend's Cloud Run service stays public
at its own address, for anything that is not this browser app.

What it costs: every API request passes through this service too, so an instance is billed for CPU
while it waits on the API — `cpuIdle` bills only while a request is in flight, and a proxied one is
in flight for as long as the API takes. Each instance takes 80 requests at once, and waiting on the
network costs nginx almost nothing. **Turn it off** with `pulumi config set apiSameOrigin false` and
an apply, when the site and the API share a domain you own —
[below](#a-domain-of-your-own) — and the browser then calls `apiUrl` directly.

### After setup, CI applies every change

Steps 3 to 8 cannot run from CI: the identity CI applies with — the apply service account,
`{{ cookiecutter.__name }}-apply-<env>` — is one of the things step 6 creates. So they run from an
operator's machine, once per environment, and that is the one exception the Pulumi standards' MUST-9
makes. From then on **every infrastructure change merges and is applied by
`{{ cookiecutter.component }}-infra.yml`**: a pull request gets the stack tests and the preview's
diff as a comment, and the merge to `main` runs `pulumi up --refresh` on the `dev` stack as the
apply account. Applying from a laptop after setup is the exception MUST-9 forbids — it skips the
review the preview exists for and leaves no audit trail. `just infra-up` stays for setting up an
environment.

The workflow watches the stack (`infra/`) and the component's workflows, not the application, so
a merge that changes only the app is published by CD and starts no apply.

Only a job in the `infrastructure` GitHub environment of `{{ cookiecutter.github_repository }}` may
impersonate the apply account: the Workload Identity provider maps GitHub's `environment` claim, and
the account's `workloadIdentityUser` binding names that one value. Its roles are predefined, one
admin role per service the stack enables — `APPLY_ROLES` in
`pulumi/src/components/identities.ts` — plus admin on the state bucket and its key alone. Not
`roles/owner`: ask Athena for the GCP infrastructure standards, which rule basic roles out in
production. That keeps every grant readable; it does not make the account weak.
`resourcemanager.projectIamAdmin` can grant any role in the project, and the stack needs it to bind
project roles, so in effect the account can do anything in the project. What bounds it is the
environment and the project:

- **Protect the `infrastructure` environment before step 9**, in the repository's settings under
  Environments: required reviewers, and deployment branches limited to `main`. GitHub creates an
  environment with no rules the first time a job names it, and an unprotected one is an apply
  nobody approved. No stack can set these rules — they belong to the repository, and the reviewers
  are people.
- **One GCP project per environment**, production in its own, keeps the account's reach to the
  environment it applies.

A resource type the stack gains is a role added to `APPLY_ROLES` in the same pull request; without
it the apply on `main` fails with a permission error naming the missing one. Applies queue, one at
a time, and are never cancelled: a `pulumi up` stopped halfway leaves resources half-changed and the
stack locked. When one fails, `main` is ahead of the project until the fix merges — fix forward with
another pull request, and the merge applies it.

### A domain of your own

**Still the better production setup**, though the API on the site's own origin means a session
survives a reload without one. With the site on `app.example.com` and the API on `api.example.com`,
the two are one site, the browser can call the API directly — no second service in the path, no
60-second ceiling on a request — and `apiSameOrigin false` builds the bundle with `apiUrl` set to the
API's address. Keeping `apiSameOrigin` on with a domain also works: the bundle calls whatever origin
served it.

Nothing here creates DNS: you own the name, and nothing in this repository can. A Cloud Run domain
mapping or a global external Application Load Balancer in front of the service are the two ways
in; either one needs the domain verified first, then the new origin in the backend's `corsOrigins`
and the `site_url` export changed to match.

### Taking it down, which is not one command

```
1. just infra-destroy dev                 removes every resource the stack created
2. just infra-teardown                    removes the state bucket and the KMS key versions
                                          (refuses when another project shares the backend)
3. 30 days on, by hand                    the key ring and key, once their versions are destroyed
```

**`pulumi destroy` does not undo step 3 of the setup order.** It removes what the stack created
and nothing else. **The state bucket is probably not yours alone**: it is named for the GCP project,
so every component of this repository deployed into the project shares it — the backend's included
— and deleting it deletes their state too.
`teardown.sh` lists `.pulumi/stacks/` first and refuses when it finds a directory this project does
not own, printing the `pulumi stack rm --remove-backups` path instead — the Pulumi standards'
MUST-8. It also exports every stack in the backend and refuses while any still holds a resource,
asks you to type the bucket name, and refuses outright when nothing can answer. The KMS key is
shared exactly when the bucket is, and destroying its versions makes the other project's stack
configuration unreadable.

**Taking down the whole repository** is therefore one order, not one command per component:
`just <component> infra-destroy` for every component and every stack; then, from each component but
the last, `pulumi -C infra/pulumi stack rm <stack> --remove-backups` to remove its emptied state;
and `just infra-teardown` last, from the one component whose state is all the bucket still holds.
`teardown.sh` refuses at any earlier point, and says which project is still there.

`destroy` also leaves the GitHub repository variables behind. Delete this component's — every one
is prefixed `{{ cookiecutter.__component_var }}_` — and take this site's origin out of the backend's
`corsOrigins`.

## Layout

```
infra/
├── justfile                    lock install test lint preview up first-deploy sync-github destroy teardown
├── bootstrap/state-bucket.sh   plain gcloud; the state bucket, its KMS key, the bootstrap APIs
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
├── scripts/first-deploy.sh     builds and pushes the first image here, and creates the service on it
├── scripts/sync-github.sh      copies stack outputs into prefixed GitHub repository variables
└── pulumi/
    ├── Pulumi.yaml             project, runtime, and the GCS backend URL
    ├── Pulumi.dev.yaml         per-stack configuration; a second environment is a sibling file
    ├── package.json            the stack's own dependencies; the application's are in the folder above
    ├── index.ts                what Pulumi runs: load the configuration, build the stack
    ├── src/
    │   ├── config.ts           typed, validated StackConfig; the only place a key is named
    │   ├── stack.ts            composition root and exports
    │   └── components/         apis, registry, identities, service, deploy-access
    └── tests/                  vitest against pulumi.runtime.setMocks — no credentials needed
```

## What the stack provisions

| Component | Resources | Cost posture |
| --- | --- | --- |
| `apis` | Cloud Run, Artifact Registry, IAM, IAM Credentials and STS enabled | free |
| `registry` | one Docker repository, keeping the newest 10 images and nothing older than 30 days | cents |
| `identities` | a runtime account (logs only), a deploy account, a preview account and an apply account, a Workload Identity pool and provider pinned to `{{ cookiecutter.github_repository }}` | free |
| `service` | one Cloud Run service, created by the first deploy on its image: nginx on 8080, probed at `/`, 256 MiB, 0 to 2 instances, public, with `API_UPSTREAM_HOST` while it serves the API | **cents, and zero while idle**; a proxied API request bills this service's CPU while the API answers |
| `deploy-access` | the deploy account's three grants, on the repository, the service (once it exists) and the runtime account | free |

**Cloud Run scales to zero**: with no requests there is no instance and no charge, and nginx starts
in well under a second, so a cold visitor waits for a container start rather than a timeout. CPU is
billed only while a request is in flight. **No figure is quoted here on purpose**: price it in the
Google Cloud pricing calculator for `{{ cookiecutter.gcp_region }}` and read the real number off your first bill.

## Who owns what

The stack owns the service's *shape* — its account, scaling, memory, probe, the API's host in its
environment, and who may invoke it.
**The CD pipeline owns exactly one field: the image**, from its first run on; the first deploy set
the one before it. It is declared with `ignoreChanges`, and every preview, apply and destroy passes
`--refresh`.

Both halves are needed. `ignoreChanges` suppresses the diff between the program and Pulumi's state;
it does not tell state that CD changed the image. Without `--refresh`, program and state both still
say the first deploy's image, there is no diff to ignore, and the next apply of any unrelated field
sends the whole template — stale image included — and Cloud Run rolls the site back to it. The `just
infra-*` recipes are where that is enforced, and `tests/contract.test.ts` fails if one of them loses
the flag.

## Prepackaged, or manual

**It is prepackaged when the value or the decision already exists inside this repository, and manual
when it needs an account, a bill, a name you own, another repository, or a human looking at
something.**

| Prepackaged | Where it lives |
| --- | --- |
| The APIs, the registry, the accounts, Workload Identity, the service | `pulumi/src/stack.ts` |
| Every repository variable the pipelines read, the API's address included | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| The state bucket and its KMS key | `bootstrap/state-bucket.sh` |
| Removing them again, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| The first image, and the service created on it | `scripts/first-deploy.sh`, via `just infra-first-deploy` |
| Every later image: build, deploy and smoke test | `.github/workflows/{{ cookiecutter.component }}-cd.yml` at the repository root |
| The stack's tests and a preview on every pull request, and the apply of every change merged to `main` | `.github/workflows/{{ cookiecutter.component }}-infra.yml` |

The stack's exports, the variables `sync-github.sh` sets and the `vars.` names the workflows read
have to agree exactly, so `pulumi/tests/contract.test.ts` asserts that they do.

| Manual | Why it cannot be shipped |
| --- | --- |
| A GCP project per environment, with billing | It costs money and names a payer |
| `./bootstrap/state-bucket.sh`, then `pulumi stack init` | A stack cannot create the bucket that holds its own state |
| Reading `just infra-preview` before the first apply | The point of the step is that a human looked |
| `apiUrl`, and whether the site serves it (`apiSameOrigin`) | It is the backend's address, which this component cannot know until the backend is deployed |
| The backend's `corsOrigins`{% if cookiecutter.include_auth == "yes" %} and `refreshTransport`{% endif %} | They are the backend stack's configuration |
| Protection rules on the `production` GitHub environment | The CD job runs in it; its rules are the gate on a deploy |
| Protection rules on the `infrastructure` GitHub environment | Only that environment may impersonate the apply account; its rules are the gate on every apply after setup |
| A domain and its DNS | You own the name; nothing here can |
| Confirming `just infra-teardown` | Deleting a state bucket is irreversible |
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

Do not create a service-account key for any of this. You authenticate as yourself; CI exchanges
GitHub's OIDC token through Workload Identity Federation, and only workflows in
`{{ cookiecutter.github_repository }}` can. **Deploy** can push to this registry repository, update
this service and run it as the runtime account — nothing project-wide. **Preview** can read the
project (`viewer`, `iam.securityReviewer`) and this backend's state, and decrypt its configuration.
Both are impersonable by any workflow in the repository, as the backend templates' CI account is:
the `production` environment's protection rules are what gate a deploy. **Apply** is narrower in
who may reach it — a job in the `infrastructure` environment and nothing else — and wider in what
it holds, for the reasons in [After setup, CI applies every change](#after-setup-ci-applies-every-change).

## Adding a second environment

**One GCP project per environment is the recommendation.** A second environment is then a new
`Pulumi.<stack>.yaml` naming its own `project`, a `state-bucket.sh` run against it (set
`GCP_PROJECT` and `GCP_REGION`), and `PULUMI_BACKEND_URL=gs://<project>-pulumi-state` when working
on that stack, because `Pulumi.yaml`'s backend URL names this project's bucket.
`{{ cookiecutter.component }}-infra.yml` previews and applies the `dev` stack; a second stack is a second pair of jobs, with
that backend URL.

## Related

- Ask Athena for the **Pulumi standards** and the **Cloud Run service standards** — the graded rules
  this stack is built to satisfy — and why this stack is TypeScript (ADR-0020).
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi GCP provider (`@pulumi/gcp` v10) — https://www.pulumi.com/registry/packages/gcp/
- Pulumi unit testing (`pulumi.runtime.setMocks`) — https://www.pulumi.com/docs/iac/concepts/testing/unit/
- Workload Identity Federation with GitHub — https://github.com/google-github-actions/auth#preferred-direct-workload-identity-federation
