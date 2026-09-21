# Frontend

A React single-page application: **React 19 + TypeScript + Vite**, TanStack Router and Query,
Tailwind v4 with shadcn/ui, and a **typed API client generated from the backend's own OpenAPI
document**. Vitest and Testing Library for the suite, ESLint 9 for the rest.

It ships a dashboard{% if cookiecutter.use_postgres == "yes" %} and one worked resource (items: list, create, delete){% endif %}. Both are
examples of the shape, not features. Delete them as soon as you have something real.

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

The file committed here is a seed, written by hand so a fresh clone type-checks before the
backend has ever run. The first `just gen-api` replaces it entirely.

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
│   └── ui/               # shadcn/ui primitives — `pnpm dlx shadcn@latest add <name>`
├── lib/
│   ├── api/
│   │   ├── client.ts     # the ONLY place that talks HTTP, plus the error contract
│   │   ├── schema.d.ts   # GENERATED — do not edit
│   │   ├── health.ts     # one module per resource: fetchers, then hooks
{%- if cookiecutter.use_postgres == "yes" %}
│   │   └── items.ts
{%- endif %}
│   ├── format.ts
│   └── utils.ts          # cn(), the shadcn class-merge helper
└── routes/               # one file per page
```

**Why fetchers and hooks are separate.** Each `lib/api/*.ts` exports plain async functions and
then thin `useQuery`/`useMutation` wrappers over them. Testing a fetcher needs no React, no
provider and no `renderHook` — which is why the fetchers are the part with tests.

**Why query keys live in an object.** `itemKeys.all`, `itemKeys.list(…)`. A typo in an
invalidation key is silent: the mutation succeeds, the list never refreshes, and it reads as a
caching bug for an afternoon.

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

## Production image

Two stages: pnpm builds the static files, `nginx-unprivileged` serves them on `${PORT}` as a
non-root user, with the SPA fallback and cache headers in `docker/default.conf.template`. The
runtime image carries the output, not the toolchain that produced it.

## What is deliberately not here

No server-side rendering, no data-fetching framework, no state-management library, no
component library beyond shadcn's primitives, and no auth. Each of those is a decision with
consequences; ask Athena for the relevant rules, decide once, and write it down in `AGENTS.md`
when you add it.
