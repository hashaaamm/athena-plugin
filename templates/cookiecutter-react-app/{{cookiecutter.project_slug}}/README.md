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

## The backend is another repository

This repository is the browser client and nothing else. The API it calls is generated from the
Athena service templates — FastAPI or Gin, which answer the same HTTP contract — into a repository
of its own, with its own stack and its own pipeline. Three things cross between them, and each one
has to be set on purpose:

| | Where it is set | Why |
| --- | --- | --- |
| The API's address | `VITE_API_URL` in `.env` locally; the `API_URL` repository variable deployed | Vite compiles it into the bundle. CD refuses to build without it. |
| This app's origin, on the backend | `CORS_ORIGINS` in the backend's `.env` locally (`http://localhost:3000`); its stack's `corsOrigins` deployed, set to this stack's `site_url` | Without it the API answers every request and the browser still refuses to hand the body to the page. |
{%- if cookiecutter.include_auth == "yes" %}
| How the refresh token travels | `REFRESH_TRANSPORT=cookie` in the backend's `.env` locally (with `REFRESH_COOKIE_SECURE=false`, because localhost is plain HTTP); its stack's `refreshTransport=cookie` deployed | This app keeps the refresh token in an HttpOnly cookie and never reads it. A backend still on `body` answers sign-in, and the first reload signs you out. |
{%- endif %}
{%- if cookiecutter.include_auth == "yes" %}

**A session that survives a reload needs this app and the API on one site** — a domain you own,
`app.example.com` and `api.example.com`. The default hostnames are two sites:
{% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}`*.cloudfront.net`{% else %}`*.run.app`, which is on the Public Suffix List,{% endif %} and the backend's own
platform name (`*.run.app`, or `*.on.aws` for ECS Express Mode). On them, signing in works, and a
reload signs you out, because the browser will not send a `SameSite=Strict` cookie across sites.
`infra/README.md` says where the domain goes.
{%- endif %}

## Run it

```bash
just lock           # once, after generating: resolves pnpm-lock.yaml in a container; commit it
just dev            # the dev server on http://localhost:3000, calling VITE_API_URL
just check          # everything CI runs: lint, types, tests, the production bundle
just gen-api        # regenerate the API client — after every backend API change
```

`just dev` runs Vite in a container, so Node is not a prerequisite. Start the backend first, from
its own repository (`just dev` there serves it on `{{ cookiecutter.api_url }}`); this app shows the API as
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
Deployed, CD passes `API_URL` to the build as `VITE_API_URL`{% if cookiecutter.deploy_target == "gcp-cloud-run" %}, a Docker build argument (see `docker/Dockerfile.prod`), not a Cloud Run environment variable: a value passed to `docker run` arrives after the bundle already exists{% else %}; nothing set on the bucket or the distribution afterwards changes what the bundle calls{% endif %}.

## How it is deployed

{% if cookiecutter.deploy_target == "aws-s3-cloudfront" -%}
`.github/workflows/cd.yml` builds the bundle on every merge to `main` that touches the application,
uploads it to the site bucket — hashed `assets/` cached for a year, `index.html` never — invalidates
the index on CloudFront, waits for that to finish, and smoke-tests `/` and a deep route. Routing is
the browser's: CloudFront answers any path the bucket has no object for with `index.html` and a 200.
{%- else -%}
Two stages in `docker/Dockerfile.prod`: pnpm builds the static files, `nginx-unprivileged` serves
them on `${PORT}` as a non-root user, with the SPA fallback and cache headers in
`docker/default.conf.template`. `.github/workflows/cd.yml` builds that image on every merge to
`main` that touches the application, tags it with the commit, updates the image on this app's Cloud
Run service and smoke-tests `/` and a deep route.
{%- endif %}

Like every pipeline here it is inert until configured — `infra/README.md` is the order of
operations, from the bootstrap script to the first deploy. **The API's address is compiled into the
bundle**, so changing where the API answers means a new build: update `API_URL`, then re-run CD.

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
