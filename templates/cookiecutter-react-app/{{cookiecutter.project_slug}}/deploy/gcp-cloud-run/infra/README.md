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
| `pulumi` CLI | preview, up, destroy, stack outputs | `pulumi version` |
| `gcloud` | the bootstrap and teardown scripts, and your own authentication | `gcloud --version` |
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

```
0. cd infra && just lock                           resolves pnpm-lock.yaml; commit it
1. gcloud + pulumi installed and authenticated     nothing is created
2. a GCP project with billing attached             the first thing that can cost money
3. ./infra/bootstrap/state-bucket.sh               plain gcloud; a stack cannot create its own state store
4. pulumi stack init dev --secrets-provider=…      points at that bucket, with the KMS key from step 3
5. just infra-preview                              read it, by a human
6. just infra-up                                   creates the service on a placeholder image
7. just infra-sync-github                          copies the stack's outputs into repository variables
8. gh variable set API_URL …                       the backend's address, by hand
9. the backend: corsOrigins = this stack's site_url, applied in the backend's repository
10. merge to main                                  cd.yml is no longer inert; the real image deploys
```

Step 3 is a script rather than a stack because a stack cannot create the bucket that holds its own
state. It creates the bucket `gs://{{ cookiecutter.gcp_project_id }}-pulumi-state` and the KMS key ring `pulumi` with
the key `stack` that encrypts stack configuration, and enables the three APIs the stack cannot
enable for itself. **They are the same names the backend templates' bootstrap uses**, so if the
backend this site calls already deploys into this project, step 3 finds them and changes nothing —
run it anyway; it is idempotent.

**One name to check at step 4.** The backend's directory in the shared bucket is named for its
Pulumi project, and this stack's is `{{ cookiecutter.project_slug }}` — the slug. If the backend was
generated with the same slug, `pulumi stack init dev` stops with "stack already exists". Do not
`stack select` it: that stack is the backend's, and an apply from here would replace everything in
it. Change `name:` in `pulumi/Pulumi.yaml` — and the `{{ cookiecutter.project_slug }}:` prefix of every
key in `Pulumi.dev.yaml` with it — and `slug` too, because the service, the registry repository and
the service accounts are named from it and would collide with the backend's. Then run step 4 again.

Step 6 asks for confirmation and **refuses to run with nothing there to confirm**. The recipe does
not pass `--yes` for you; this command creates billable resources and a human reading the preview
is the point. From a pipeline, or from an agent with no terminal: `just infra-up dev --yes`.

Steps 8 and 9 are the two halves of the conversation with the backend, and neither can come from
this stack because both belong to the other repository. **`API_URL`** is compiled into the bundle
as `VITE_API_URL`, so `cd.yml` refuses to build without it — a bundle built without one calls
`{{ cookiecutter.api_url }}`, deploys cleanly, and fails every request. **The backend's `corsOrigins`**
must name this site's origin, the `site_url` output (`https://<service>-<hash>.<region>.run.app`), or
every request from the browser fails CORS while the API answers it correctly.{% if cookiecutter.include_auth == "yes" %} Set the backend's
`refreshTransport=cookie` in the same change: this app keeps the refresh token in an HttpOnly
cookie, and a backend sending it in the body has nothing for the app to keep.{% endif %}

Step 10 is last because the service exists before the image does. Artifact Registry is empty when
the stack is first applied, so `pulumi up` creates the service on Google's public `hello` container,
and CD replaces it with the real image on the first merge.

### A domain of your own

{% if cookiecutter.include_auth == "yes" -%}
**This is not optional for a session that survives a reload.** The refresh cookie is
`SameSite=Strict`, `run.app` is on the Public Suffix List — so every `*.run.app` hostname is a site
of its own — and the site and the backend are two of them: signing in works, and a reload signs the
user out. Put the site and the API on one site you own — `app.example.com` and `api.example.com` —
and set `API_URL` to the API's address.

{% endif -%}
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
so a backend deployed beside this site shares it, and deleting it deletes the backend's state too.
`teardown.sh` lists `.pulumi/stacks/` first and refuses when it finds a directory this project does
not own, printing the `pulumi stack rm --remove-backups` path instead — the Pulumi standards'
MUST-8. It also exports every stack in the backend and refuses while any still holds a resource,
asks you to type the bucket name, and refuses outright when nothing can answer. The KMS key is
shared exactly when the bucket is, and destroying its versions makes the other project's stack
configuration unreadable.

`destroy` also leaves the GitHub repository variables behind. Delete them, and take this site's
origin out of the backend's `corsOrigins`.

## Layout

```
infra/
├── justfile                    lock install test lint preview up sync-github destroy teardown
├── bootstrap/state-bucket.sh   plain gcloud; the state bucket, its KMS key, the bootstrap APIs
├── bootstrap/teardown.sh       the inverse; removes what `pulumi destroy` cannot reach
├── scripts/sync-github.sh      copies stack outputs into GitHub repository variables
└── pulumi/
    ├── Pulumi.yaml             project, runtime, and the GCS backend URL
    ├── Pulumi.dev.yaml         per-stack configuration; a second environment is a sibling file
    ├── package.json            the stack's own dependencies; the application's are at the root
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
| `identities` | a runtime account (logs only), a deploy account and a preview account, a Workload Identity pool and provider pinned to `{{ cookiecutter.github_repository }}` | free |
| `service` | one Cloud Run service: nginx on 8080, probed at `/`, 256 MiB, 0 to 2 instances, public | **cents, and zero while idle** |
| `deploy-access` | the deploy account's three grants, on the repository, the service and the runtime account | free |

**Cloud Run scales to zero**: with no requests there is no instance and no charge, and nginx starts
in well under a second, so a cold visitor waits for a container start rather than a timeout. CPU is
billed only while a request is in flight. **No figure is quoted here on purpose**: price it in the
Google Cloud pricing calculator for `{{ cookiecutter.gcp_region }}` and read the real number off your first bill.

## Who owns what

The stack owns the service's *shape* — its account, scaling, memory, probe and who may invoke it.
**The CD pipeline owns exactly one field: the image.** It is declared with `ignoreChanges`, and
every preview, apply and destroy passes `--refresh`.

Both halves are needed. `ignoreChanges` suppresses the diff between the program and Pulumi's state;
it does not tell state that CD changed the image. Without `--refresh`, program and state both still
say `hello`, there is no diff to ignore, and the next apply of any unrelated field sends the whole
template — stale image included — and Cloud Run rolls the site back to the placeholder. The `just
infra-*` recipes are where that is enforced, and `tests/contract.test.ts` fails if one of them loses
the flag.

## Prepackaged, or manual

**It is prepackaged when the value or the decision already exists inside this repository, and manual
when it needs an account, a bill, a name you own, another repository, or a human looking at
something.**

| Prepackaged | Where it lives |
| --- | --- |
| The APIs, the registry, the accounts, Workload Identity, the service | `pulumi/src/stack.ts` |
| Every repository variable the pipelines read but one | `scripts/sync-github.sh`, straight out of `pulumi stack output` |
| The state bucket and its KMS key | `bootstrap/state-bucket.sh` |
| Removing them again, which `pulumi destroy` cannot | `bootstrap/teardown.sh`, via `just infra-teardown` |
| Image build, deploy and smoke test | `.github/workflows/cd.yml` |
| The stack's tests and a preview on every pull request | `.github/workflows/infra.yml` |

The stack's exports, the variables `sync-github.sh` sets and the `vars.` names the workflows read
have to agree exactly, so `pulumi/tests/contract.test.ts` asserts that they do.

| Manual | Why it cannot be shipped |
| --- | --- |
| A GCP project per environment, with billing | It costs money and names a payer |
| `./bootstrap/state-bucket.sh`, then `pulumi stack init` | A stack cannot create the bucket that holds its own state |
| Reading `just infra-preview` before the first apply | The point of the step is that a human looked |
| `API_URL` | It is the backend's address, and the backend is another repository |
| The backend's `corsOrigins`{% if cookiecutter.include_auth == "yes" %} and `refreshTransport`{% endif %} | They are the backend stack's configuration |
| Protection rules on the `production` GitHub environment | cd.yml's job runs in it; its rules are the gate on a deploy |
| A domain and its DNS | You own the name; nothing here can |
| Confirming `just infra-teardown` | Deleting a state bucket is irreversible |
| Branch protection on `main`, and requiring CI to merge | A GitHub setting, not a file |

Do not create a service-account key for any of this. You authenticate as yourself; CI exchanges
GitHub's OIDC token through Workload Identity Federation, and only workflows in
`{{ cookiecutter.github_repository }}` can. **Deploy** can push to this registry repository, update
this service and run it as the runtime account — nothing project-wide. **Preview** can read the
project (`viewer`, `iam.securityReviewer`) and this backend's state, and decrypt its configuration.
Both are impersonable by any workflow in the repository, as the backend templates' CI account is:
the `production` environment's protection rules are what gate a deploy.

## Adding a second environment

**One GCP project per environment is the recommendation.** A second environment is then a new
`Pulumi.<stack>.yaml` naming its own `project`, a `state-bucket.sh` run against it (set
`GCP_PROJECT` and `GCP_REGION`), and `PULUMI_BACKEND_URL=gs://<project>-pulumi-state` when working
on that stack, because `Pulumi.yaml`'s backend URL names this project's bucket.

## Related

- Ask Athena for the **Pulumi standards** and the **Cloud Run service standards** — the graded rules
  this stack is built to satisfy — and why this stack is TypeScript (ADR-0020).
- Ask Athena for the **GitHub Actions standards** before changing anything in `.github/workflows/`.
- Pulumi GCP provider (`@pulumi/gcp` v10) — https://www.pulumi.com/registry/packages/gcp/
- Pulumi unit testing (`pulumi.runtime.setMocks`) — https://www.pulumi.com/docs/iac/concepts/testing/unit/
- Workload Identity Federation with GitHub — https://github.com/google-github-actions/auth#preferred-direct-workload-identity-federation
