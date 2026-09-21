# Frontend

A React single-page application: **React 19 + TypeScript + Vite**, TanStack Router and Query,
Tailwind v4 with shadcn/ui, and a **typed API client generated from the backend's own OpenAPI
document**. Vitest and Testing Library for the suite, ESLint 9 for the rest.

It ships a dashboard that reads `/health/ready` and says whether the backend is there. It is an
example of the shape, not a feature — replace it with whatever this product's home page actually
is.
{%- if cookiecutter.use_postgres == "yes" %}

It also ships the signed-in half of the backend's four auth endpoints: `/register`, `/login`, and
an `/account` page that reads `GET /auth/me` and changes a password. Three decisions came with
it, and every one of them has a cost somebody has to know about — where the token lives, what a
401 does, and which routes are behind the guard. They are written up, with the trade-off
accepted, in the **Authentication** section of [AGENTS.md](AGENTS.md). Read that before you touch
`src/lib/auth.ts`.
{%- endif %}

## Run it

From the repository root, because the frontend needs the backend beside it:

```bash
just dev            # backend + frontend; the app is on http://localhost:3000
just check          # everything CI runs, both components
just gen-api        # regenerate the API client — after every backend API change
```

From here, if you have Node and pnpm and would rather not use Docker:

```bash
pnpm install
cp .env.example .env.local
pnpm dev
```

## The one rule that is not optional

`src/lib/api/schema.d.ts` is **generated**. Never hand-write a type the backend already
describes, and run `just gen-api` on every API change. A generated client turns a renamed field
into a failed build; a hand-written one turns it into `undefined` in front of a user.

The file a new project starts with is that command's own output, captured against a backend
generated from this template so a fresh clone type-checks before the backend has ever run. It is
not hand-written, and the first `just gen-api` overwrites it.

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
{%- if cookiecutter.use_postgres == "yes" %}
│   ├── auth-card.tsx     # the chrome-less card the signed-out pages render in
{%- endif %}
│   └── ui/               # shadcn/ui primitives — `pnpm dlx shadcn@latest add <name>`
├── lib/
│   ├── api/
│   │   ├── client.ts     # the ONLY place that talks HTTP, plus the error contract
│   │   ├── schema.d.ts   # GENERATED — do not edit
{%- if cookiecutter.use_postgres == "yes" %}
│   │   ├── auth.ts       # register, login, me, change-password
{%- endif %}
│   │   └── health.ts     # one module per resource: fetchers, then hooks
{%- if cookiecutter.use_postgres == "yes" %}
│   ├── auth.ts           # where the token lives, and what a 401 means. No React.
│   ├── use-session.ts    # the same token, as React state
{%- endif %}
│   ├── format.ts
│   └── utils.ts          # cn(), the shadcn class-merge helper
└── routes/               # one file per page
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
| **Forms** | `react-hook-form` + a `zod` schema next to the form. The server validates regardless; the client validates so nobody waits for a round trip to learn a field is required. |
| **Errors** | Throw, and let TanStack Query hold the state. `unwrap()` in `lib/api/client.ts` turns the backend's `{ error: { code, message } }` into a thrown `ApiError` with the code intact. |
| **State** | Server state is TanStack Query's. Reach for a store only for state that is genuinely client-only, and ask Athena first. |
| **Tests** | Behaviour through the DOM — roles and text, not class names. Mock at the `api` client, not at `fetch`. |

## Environment

`.env.example` → `.env.local`. Only `VITE_`-prefixed variables reach the browser, they are
**inlined at build time**, and nothing in them is a secret — an API key here is a published API
key. Deployed, they are Docker build args (see `docker/Dockerfile.prod`), not Cloud Run
environment variables: a value passed to `docker run` arrives after the bundle already exists.

## Production image, and how it is deployed

Two stages: pnpm builds the static files, `nginx-unprivileged` serves them on `${PORT}` as a
non-root user, with the SPA fallback and cache headers in `docker/default.conf.template`. The
runtime image carries the output, not the toolchain that produced it.

`.github/workflows/frontend-cd.yml` builds that image on every merge to `main` touching
`frontend/`, tags it with the commit, and updates the image on this app's own Cloud Run service —
declared in `infra/pulumi/__main__.py`, with its own runtime service account, no database and no
secrets. Like every pipeline here it is inert until `WIF_PROVIDER` is set.

**The one thing to internalise:** the API's address is *compiled into the bundle*. CD passes it as
the `VITE_API_URL` build argument, taken from the `SERVICE_URL` repository variable that the
Pulumi stack exports and `just infra-sync-github` sets. So the backend's Cloud Run service has to
exist before the frontend can be built correctly, and changing where the API answers means
building a new image — re-run `just infra-sync-github`, then re-run Frontend CD. Setting an
environment variable on the running revision does nothing whatsoever: the bundle was written
before that variable existed. If the API later answers on a domain you own, set `FRONTEND_API_URL`
and it wins over `SERVICE_URL`.

## What is deliberately not here

No server-side rendering, no data-fetching framework, no state-management library, and no
component library beyond shadcn's primitives.
{%- if cookiecutter.use_postgres == "yes" %}
No refresh flow, no sign-out-everywhere, no roles, no password reset and no "remember me"
either — the backend has none of them, and each one is a backend change before it is a component.
{%- else %}
No sign-in flow, because this project has no database and therefore no user to sign in: generate
with `use_postgres=yes` and the backend's four auth endpoints and this application's pages for
them both arrive together.
{%- endif %}
Each of those is a decision with consequences; ask Athena for the relevant rules, decide once,
and write it down in `AGENTS.md` when you add it.
