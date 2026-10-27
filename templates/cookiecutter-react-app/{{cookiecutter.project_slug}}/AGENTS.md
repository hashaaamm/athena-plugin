# Agent rules — {{ cookiecutter.project_name }}

The general standards come from Athena: ask it for the frontend rules before you plan a change —
the athena skill says how. This file is a summary of what the code already does and a map of the
repository, not a substitute for them.

| Where | What it is |
| --- | --- |
| `src/`, `public/`, `index.html`, `package.json` | The React single-page application, at the repository root |
| `docker-compose.yml` | The Vite dev server, in a container, against the backend at `VITE_API_URL` |
{%- if cookiecutter.deploy_target == "gcp-cloud-run" %}
| `docker/` | The production image: the bundle, served by nginx on Cloud Run |
{%- endif %}
| `infra/` | Pulumi (TypeScript): {% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}a private S3 bucket behind CloudFront{% else %}Cloud Run serving the nginx image{% endif %} — [infra/README.md](infra/README.md) |
| `.github/workflows/` | `ci.yml` the application's gate, `cd.yml` the deploy, `infra.yml` the stack's gate and preview |

## What this application is

{{ cookiecutter.description }}

<<Replace this with one paragraph: who uses it, what it does, which backend it calls. An agent that
does not know this will make architecturally wrong choices confidently.>>

The backend is **another repository**. This one never runs it, never deploys it and never imports
from it; the only things that cross are the HTTP contract, captured in `src/lib/api/schema.d.ts`,
and two settings on the backend's side — `CORS_ORIGINS`{% if cookiecutter.include_auth == "yes" %} and `REFRESH_TRANSPORT=cookie`{% endif %} — that README.md lists.

## The stack, and what not to add to it

React 19 · TypeScript (strict) · Vite · TanStack Router · TanStack Query · Tailwind v4 ·
shadcn/ui · react-hook-form + zod 4 · openapi-fetch · Vitest + Testing Library · ESLint 9.

Adding a dependency is a decision, not a step. Ask Athena for the approved list first, and say
in your summary that you added one.

## Non-negotiable

- **The API client is generated.** `src/lib/api/schema.d.ts` comes from `just gen-api`. Never
  edit it, and never hand-write a type the backend already describes. Run `just gen-api` as soon
  as the backend change that motivated it is running.
- **One HTTP entry point.** Everything goes through `api` in `src/lib/api/client.ts`. A bare
  `fetch` to the backend anywhere else skips the base URL, the error contract and the types.
- **Colour comes from tokens.** `src/index.css` holds the palette. A hex code in a component is
  a bug even when it looks right.
- **Every request state is rendered.** Loading, empty, error, and the data. A page that draws
  only the happy path shows the same blank table for "loading", "nothing here" and "the API is
  down".
- **No secret is a `VITE_` variable.** They are inlined into the bundle and shipped to the
  browser. Ask Athena for the secrets-management rules.
- **The API origin is compiled in.** `VITE_API_URL` is read at build time, so a deployed bundle
  calls whatever `API_URL` held when CD built it. Changing where the API answers is a rebuild.
{%- if cookiecutter.include_auth == "yes" %}

## Authentication

Six backend endpoints — `register`, `login`, `refresh`, `logout`, `me`, `change-password` — and a
handful of decisions that were made once. Read this before touching `src/lib/auth.ts`,
`src/lib/api/client.ts`, `src/lib/api/auth.ts` or the guard in `src/router.tsx`.

### Two credentials, two places

**The refresh token is an httpOnly cookie.** The backend sets it on login, refresh and
change-password — `HttpOnly`, `Secure`, `SameSite=Strict`, scoped to `/api/v1/auth` — and never
puts it in a response body, provided it runs with `REFRESH_TRANSPORT=cookie`. No script in this
page can read it: not ours, not an injected one. It lasts thirty days, is spent and replaced on
every refresh, and a spent one coming back revokes the whole session on the server.

**The access token lives in memory** — a module variable in `src/lib/auth.ts`, nothing else
touches it, and it is in neither `sessionStorage` nor `localStorage`. A reload or a new tab starts
without one, and the route guard gets a fresh one from the cookie before anything renders. It
lasts fifteen minutes.

**What that bought.** A stolen session can no longer be *carried off*: the long-lived credential is
out of script's reach, and the one script can reach dies in fifteen minutes and with the page. A
new tab is signed in, and a reload keeps you signed in.

**What it did not buy.** Protection from cross-site scripting. Script running in this page can
still use the session while the page is open — call the API, read the access token. The defence
against XSS is a content security policy and not rendering untrusted HTML, not where a token is
kept. Never move the access token into web storage to "fix" anything; nothing here needs it.

**What the cookie costs: CSRF, and a same-site deployment.** A cookie rides along on requests
other pages start. `refresh` and `logout` are the only routes that read it, and they require the
`X-Requested-With` header and an allowlisted `Origin` — a third-party page cannot send the first
without a CORS preflight the backend refuses. `credentials: "include"` is set on the four calls
that send or receive the cookie (`SESSION_COOKIE` and `WITH_COOKIE` in `src/lib/api/client.ts`)
and on nothing else. And `SameSite=Strict` needs this app and the API on one *site* —
`app.example.com` and `api.example.com`, or `localhost:3000` and `localhost:8000`. The default
hostnames are not one site: this app answers on {% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}`*.cloudfront.net`{% else %}`*.run.app`, which is on the Public Suffix List{% endif %}, and the
backend on its own platform's name, so the browser drops the cookie: sign-in works, and a reload
or the fifteen-minute mark signs you out. Put both on a domain you own — README.md — rather than
loosening the backend's `REFRESH_COOKIE_SAMESITE`.

### A 401 is handled once, in middleware

`authMiddleware` in `src/lib/api/client.ts` is the only code that decides what a 401 means. For a
request that carried an access token it is almost always "that token expired", so it refreshes and
replays the request with the new token; the caller sees the replay's answer and never the 401.
Only when the refresh is refused, or the replay is refused as well, is the session over —
`endSession` in `src/lib/auth.ts` clears the token and hard-navigates to `/login`, a full document
load, so the TanStack Query cache goes with the session rather than sitting in memory for the next
person at the keyboard.

**One refresh at a time — this is a correctness rule.** A refresh token is single-use, so two
refreshes sent with the same cookie look exactly like a replay to the backend, and it answers a
replay by revoking the whole session. `refreshOnce` shares one in-flight refresh between every
caller in the tab, and serialises across tabs with the Web Locks API, because the cookie is shared
by all of them. Ten queries expiring together cost one refresh. Never call the refresh endpoint
anywhere but through `refreshSession`.

The guards that keep this safe are tested: a 401 from login, refresh or logout is an answer and is
handed back untouched; a request sent with no token is not refreshed for; there is no redirect to
`/login` from `/login` (that is the reload loop). **No component, hook or page handles a 401.**

A **403 is not a 401** and must never be treated as one. `POST /auth/change-password` answers 403
for a wrong current password precisely so the session survives and the form shows the error.

### The whole shell is behind the guard

`appRoute` in `src/router.tsx` — the pathless layout route that renders the sidebar — holds the
`beforeLoad` check. With no access token in memory it asks the refresh cookie first, through the
same single in-flight refresh, and redirects to `/login` only when that is refused. Every page
inside the application is under it, `/` included. `/login` and `/register` hang off the root
instead, outside the shell, and both bounce a caller who already holds an access token.

**The guard is a UX boundary, not a security one.** The built bundle is static files, served to
anyone who asks for them; `beforeLoad` decides what renders, not what is downloadable. What keeps
data private is the backend answering 401 without a valid bearer token — never add a field to a
response on the basis that only a signed-in page displays it.

### Errors are the server's words, with one exception

Render `ApiError.message`. The exception is sign-in: **every** credential failure shows one fixed
sentence, so the UI cannot undo the backend's refusal to say whether an address is registered.

### Out of scope, on purpose

No sign-out-everywhere button, no roles, no password reset, no "remember me". The backend has none
of them. Each one is a backend change first.
{%- endif %}

## Where a change goes

| Change | Files |
| --- | --- |
{%- if cookiecutter.include_auth == "yes" %}
| New page | `src/routes/<page>.tsx` + a route under `appRoute` in `src/router.tsx` (+ a nav entry in `app-shell.tsx`) |
{%- else %}
| New page | `src/routes/<page>.tsx` + a route in `src/router.tsx` (+ a nav entry in `app-shell.tsx`) |
{%- endif %}
| New resource | `src/lib/api/<resource>.ts`: fetchers first, then the hooks over them, keys in one object |
| New shared component | `src/components/` — `src/components/ui/` is shadcn's, added with its CLI |
| Backend API changed | `just gen-api`, then fix what stops compiling |
| Where it is served | `infra/` — read `infra/README.md` first |

## Commands

Through `just`, from the repository root. Never invent a raw `docker compose` or `pnpm`
invocation in a script or a workflow.

| Task | Command |
| --- | --- |
| Run the dev server | `just dev` (http://localhost:3000, calling `VITE_API_URL`) |
| Lint and type check | `just lint` |
| Tests | `just test` |
| One test file, watching | `just test-one src/lib/api/client.test.ts` |
| Regenerate the API client | `just gen-api` (the backend must be running) |
| Everything CI runs | `just check` |
| Dependency audit | `just audit` |
| Unit-test the Pulumi program | `just infra-test` |
| See what an apply would change | `just infra-preview` |

## Definition of done here

- [ ] `just check` passes
- [ ] The API client was regenerated if the backend's API changed
- [ ] Loading, empty and error states exist for anything that fetches
- [ ] Tests assert through roles and text, not class names, and mock at `api` — not at `fetch`
- [ ] No new dependency outside the handbook's approved list
- [ ] Nothing secret in a `VITE_` variable

## Do not

- Do not disable an ESLint rule, add `@ts-expect-error`, or cast to `any` to get to green.
  Surface the conflict instead.
{%- if cookiecutter.include_auth == "yes" %}
- Do not handle a 401 anywhere but `authMiddleware`, do not read the token from anywhere but
  `src/lib/auth.ts`, and do not refresh except through `refreshSession`.
- Do not put a token in `sessionStorage` or `localStorage`, and do not ask for the refresh token in
  a response body. Both undo the reason the refresh token is a cookie.
- Do not tell a user at sign-in whether an email address has an account.
{%- endif %}
- Do not edit `src/lib/api/schema.d.ts`.
- Do not introduce a second state library, a second HTTP client, or a second styling system.
- Do not modify `.github/workflows/` without saying so explicitly in your summary.
- Do not run `{% if cookiecutter.deploy_target == "aws-s3-cloudfront" %}aws{% else %}gcloud{% endif %}` mutations or anything that changes cloud state. That includes
  `just infra-up`, `just infra-sync-github` and the bootstrap script. `just infra-test` and
  `just infra-preview` change nothing; everything else under `infra/` needs a human who has read
  the preview.
- Do not run `just infra-destroy` or `just infra-teardown`, ever, and do not suggest removing the
  guards on them. The teardown script deletes the Pulumi state bucket, which is irreversible, is
  the only record of what exists, and may hold the backend's state too.

## Recurring failures

<<Keep a list of the three things that most often break locally. It is the cheapest runbook there
is, and agents use it immediately.>>
