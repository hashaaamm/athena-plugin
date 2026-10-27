# {{ cookiecutter.project_name }}

{{ cookiecutter.description }}

A React single-page application: **React 19 + TypeScript + Vite**, TanStack Router and Query,
Tailwind v4 with shadcn/ui, and a **typed API client generated from the backend's own OpenAPI
document**. Vitest and Testing Library for the suite, ESLint 9 for the rest. It deploys to
{% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}**a private S3 bucket behind CloudFront**{% else %}**Cloud Run, as an nginx image**{% endif %}, with the Pulumi stack in `infra/`.

It ships a dashboard that reads `/health/ready` and says whether the backend is there. It is an
example of the shape, not a feature — replace it with whatever this product's home page actually
is.
{%- if cookiecutter.include_auth == "yes" %}

It also ships the signed-in half of the backend's auth endpoints: `/register`, `/login`, and an
`/account` page that reads `GET /auth/me` and changes a password. **The application shell is behind
the guard, `/` included** — the only pages reachable without a token are sign-in and registration.
Three decisions came with it, and every one of them has a cost somebody has to know about — where
the token lives, what a 401 does, and what that guard does and does not protect. They are written
up, with the trade-off accepted, in the **Authentication** section of [AGENTS.md](AGENTS.md). Read
that before you touch `src/lib/auth.ts`.
{%- endif %}

## The backend is another component

This folder is one component of its repository: the browser client and nothing else. The API it
calls is generated from the Athena service templates — FastAPI or Gin, which answer the same HTTP
contract — into a folder of its own beside this one, with its own stack and its own pipeline. Three
things cross between them, and each one has to be set on purpose:

| | Where it is set | Why |
| --- | --- | --- |
| The API's address | `VITE_API_URL` in `.env` locally; deployed, the stack's `apiUrl` — the backend's own address — from which the stack derives what the bundle calls, which `just infra-sync-github` copies into `{{ cookiecutter.__component_var }}_API_URL` | Vite compiles it into the bundle. The first deploy and CD both refuse to build without it. |
| This app's origin, on the backend | `CORS_ORIGINS` in the backend's `.env` locally (`http://localhost:3000`); {% if cookiecutter.include_auth == "yes" %}its stack's `corsOrigins` deployed, set to this stack's `site_url` | Locally the browser calls another origin, and without it the API answers every request and the browser still refuses to hand the body to the page. Deployed the API is on this site's origin and needs no CORS, but the cookie routes' CSRF check accepts only the origins listed there. |{% else %}nothing deployed, while the site serves the API | Locally the browser calls another origin, and without it the API answers every request and the browser still refuses to hand the body to the page. Deployed the API is on this site's own origin, which needs no CORS. |{% endif %}
{%- if cookiecutter.include_auth == "yes" %}
| How the refresh token travels | `REFRESH_TRANSPORT=cookie` in the backend's `.env` locally (with `REFRESH_COOKIE_SECURE=false`, because localhost is plain HTTP); its stack's `refreshTransport=cookie` deployed | This app keeps the refresh token in an HttpOnly cookie and never reads it. A backend still on `body` answers sign-in, and the first reload signs you out. |
{%- endif %}

**Deployed, the site serves the API on its own origin.** {% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}The distribution forwards `/api/*` and `/health/*`{% else %}The image's nginx proxies `/api/` and `/health/`{% endif %} to
the stack's `apiUrl`, and the bundle is built with `VITE_API_URL=/`, which the API client resolves
against the page's own origin. The default hostnames — {% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}`*.cloudfront.net`{% else %}`*.run.app`{% endif %} for this app,
`*.run.app` or `*.on.aws` for the backend — are on the Public Suffix List, so called directly the
two would be two sites{% if cookiecutter.include_auth == "yes" %}: signing in would work, and every reload would sign you out, because the
browser does not send a `SameSite=Strict` cookie across sites. Under one origin the refresh cookie
is first-party and the session survives a reload{% endif %}. A domain of your own for both,
`app.example.com` and `api.example.com`, is still the better production setup, and
`apiSameOrigin false` then has the bundle call the API directly. `infra/README.md` has both.

Locally nothing changes: `just dev` calls the API at its own address, and `localhost:3000` and
`localhost:8000` are one site already — a port does not make a second one.

## Run it

From this folder — or from the repository root as `just {{ cookiecutter.component }} <recipe>`:

```bash
just lock           # once, after generating: resolves pnpm-lock.yaml in a container; commit it
just dev            # the dev server on http://localhost:3000, calling VITE_API_URL
just check          # everything CI runs: lint, types, tests, the production bundle
just gen-api        # regenerate the API client — after every backend API change
```

`just dev` runs Vite in a container, so Node is not a prerequisite. Start the backend first, from
its own folder (`just dev` there serves it on `{{ cookiecutter.api_url }}`); this app shows the API as
unreachable until it answers, rather than refusing to start.

If you have Node 24 and pnpm and would rather not use Docker:

```bash
pnpm install
cp .env.example .env
pnpm dev
```

## The one rule that is not optional

`src/lib/api/schema.d.ts` is **generated**. Never hand-write a type the backend already
describes, and run `just gen-api` on every API change. A generated client turns a renamed field
into a failed build; a hand-written one turns it into `undefined` in front of a user.

The file a new project starts with is that command's own output, captured against a backend
generated from the service templates so a fresh clone type-checks before the backend has ever run.
It is not hand-written, and the first `just gen-api` overwrites it.

## Layout

```
src/
├── main.tsx              # mount: fonts, providers, app
├── App.tsx               # hands the route tree to the router
├── router.tsx            # every route, in one readable file
├── index.css             # the theme — Tailwind v4 has no config file
├── components/
│   ├── app-shell.tsx     # the chrome: sidebar + outlet + error boundary
│   ├── error-boundary.tsx
│   ├── status-screens.tsx # 404 and error, in full-page and in-shell variants
│   ├── stat-card.tsx
{%- if cookiecutter.include_auth == "yes" %}
│   ├── auth-card.tsx     # the chrome-less card the signed-out pages render in
{%- endif %}
│   └── ui/               # shadcn/ui primitives — `pnpm dlx shadcn@latest add <name>`
├── lib/
│   ├── api/
│   │   ├── client.ts     # the ONLY place that talks HTTP, plus the error contract
│   │   ├── schema.d.ts   # GENERATED — do not edit
{%- if cookiecutter.include_auth == "yes" %}
│   │   ├── auth.ts       # register, login, me, change-password
{%- endif %}
│   │   └── health.ts     # one module per resource: fetchers, then hooks
{%- if cookiecutter.include_auth == "yes" %}
│   ├── auth.ts           # where the token lives, and what a 401 means. No React.
│   ├── use-session.ts    # the same token, as React state
{%- endif %}
│   ├── format.ts
│   └── utils.ts          # cn(), the shadcn class-merge helper
└── routes/               # one file per page
infra/                    # where it is served — infra/README.md
```

**Why fetchers and hooks are separate.** Each `lib/api/*.ts` exports plain async functions and
then thin `useQuery`/`useMutation` wrappers over them. Testing a fetcher needs no React, no
provider and no `renderHook` — which is why the fetchers are the part with tests.

**Why query keys belong in an object per resource.** `<x>Keys.all`, `<x>Keys.list(…)`, declared
once in the resource's module and never spelled out at a call site. A typo in an invalidation key
is silent: the mutation succeeds, the list never refreshes, and it reads as a caching bug for an
afternoon.

## Conventions worth knowing before the first PR

| | |
| --- | --- |
| **Colour** | Tokens in `src/index.css`, never a hex code in a component. Two sets: the product palette (`text-ink`, `bg-surface`) and shadcn's own (`bg-primary`, `border-border`). |
| **Components** | `pnpm dlx shadcn@latest add dialog` writes into `src/components/ui/`. Treat what it writes as yours — edit it in place rather than wrapping it. |
| **Icons** | Phosphor (`@phosphor-icons/react`). |
| **Forms** | `react-hook-form` + a `zod` 4 schema next to the form. The server validates regardless; the client validates so nobody waits for a round trip to learn a field is required. |
| **Errors** | Throw, and let TanStack Query hold the state. `unwrap()` in `lib/api/client.ts` turns the backend's `{ error: { code, message } }` into a thrown `ApiError` with the code intact. |
| **State** | Server state is TanStack Query's. Reach for a store only for state that is genuinely client-only, and ask Athena first. |
| **Tests** | Behaviour through the DOM — roles and text, not class names. Mock at the `api` client, not at `fetch`. |

## Environment

`.env.example` → `.env`. Only `VITE_`-prefixed variables reach the browser, they are **inlined
at build time**, and nothing in them is a secret — an API key here is a published API key.
Deployed, CD passes `{{ cookiecutter.__component_var }}_API_URL` — the stack's `api_url`: `/` while the site serves the API, else its `apiUrl` — to the build as `VITE_API_URL`{% if cookiecutter.deploy_target == "gcp-cloud-run" %}, a Docker build argument (see `docker/Dockerfile.prod`), not a Cloud Run environment variable: a value passed to `docker run` arrives after the bundle already exists{% else %}; nothing set on the bucket or the distribution afterwards changes what the bundle calls{% endif %}.

## How it is deployed

{% if cookiecutter.deploy_target == "aws-s3-cloudfront" -%}
The first deploy is `just infra-first-deploy`, from your machine. After that,
`.github/workflows/{{ cookiecutter.component }}-cd.yml` at the repository root builds the bundle on
every merge to `main` that touches this folder, and `infra/scripts/publish.sh`, the script the
first deploy ran, uploads it to the site bucket — hashed `assets/` cached for a year, `index.html` never — invalidates
the index on CloudFront, waits for that to finish, and smoke-tests `/`, a deep route and the API
through `/health/live`. Routing is the browser's: a CloudFront Function serves `index.html` for any
path whose last segment has no extension, and `/api/*` and `/health/*` go to the API.
{%- else -%}
Two stages in `docker/Dockerfile.prod`: pnpm builds the static files, `nginx-unprivileged` serves
them on `${PORT}` as a non-root user, with the SPA fallback, the cache headers and the API proxy
for `/api/` and `/health/` in `docker/default.conf.template`. The first image is built and pushed
from your machine by `just infra-first-deploy`, which creates the Cloud Run service on it. After
that, `.github/workflows/{{ cookiecutter.component }}-cd.yml` at the repository root builds the
image on every merge to `main` that touches this folder, tags it with the commit, updates the image
on this app's Cloud Run service and smoke-tests `/`, a deep route and the API through `/health/live`.
{%- endif %}

Like every pipeline here it is inert until configured — `infra/README.md` is the order of
operations, from the bootstrap script to the first deploy. That setup runs from your machine once;
after it, `.github/workflows/{{ cookiecutter.component }}-infra.yml` applies every infrastructure change merged to `main`, as
the stack's apply {% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}role{% else %}service account{% endif %}, in the `infrastructure` GitHub environment. Protect that environment —
required reviewers, `main` only — before the first push; `just infra-up` from a laptop after setup
is the exception the Pulumi standards' MUST-9 forbids. **Where the bundle calls the API is
compiled into it.** While the site serves the API that is `/`, so pointing it at another backend is
only `apiUrl` in `infra/pulumi/Pulumi.<stack>.yaml`, merged, which applies it. Calling the API
directly, it is a new build too: merge the change, then run `just infra-sync-github` and re-run CD.

## What is deliberately not here

No server-side rendering, no data-fetching framework, no state-management library, and no
component library beyond shadcn's primitives.
{%- if cookiecutter.include_auth == "yes" %}
No sign-out-everywhere button, no roles, no password reset and no "remember me" either — the
backend has none of them, and each one is a backend change before it is a component.
{%- else %}
No sign-in flow: this project was generated with `include_auth=no`. Generating with
`include_auth=yes` brings the sign-in, registration and account screens, which need a backend with a
database.
{%- endif %}
Each of those is a decision with consequences; ask Athena for the relevant rules, decide once,
and write it down in `AGENTS.md` when you add it.
