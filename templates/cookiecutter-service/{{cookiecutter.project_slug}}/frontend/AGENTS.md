# Agent rules — frontend

Read [../AGENTS.md](../AGENTS.md) and the handbook's `AGENTS.md` first. This file covers the web
client only. Ask Athena for the frontend rules before you plan a change here — this is a summary
of what the code already does, not a substitute for them.

## The stack, and what not to add to it

React 19 · TypeScript (strict) · Vite · TanStack Router · TanStack Query · Tailwind v4 ·
shadcn/ui · react-hook-form + zod · openapi-fetch · Vitest + Testing Library · ESLint 9.

Adding a dependency is a decision, not a step. Ask Athena for the approved list first, and say
in your summary that you added one.

## Non-negotiable

- **The API client is generated.** `src/lib/api/schema.d.ts` comes from `just gen-api`. Never
  edit it, and never hand-write a type the backend already describes. Run `just gen-api` in the
  same change as the backend edit that motivated it.
- **One HTTP entry point.** Everything goes through `api` in `src/lib/api/client.ts`. A bare
  `fetch` to our own backend anywhere else skips the base URL, the error contract and the types.
- **Colour comes from tokens.** `src/index.css` holds the palette. A hex code in a component is
  a bug even when it looks right.
- **Every request state is rendered.** Loading, empty, error, and the data. A page that draws
  only the happy path shows the same blank table for "loading", "nothing here" and "the API is
  down".
- **No secret is a `VITE_` variable.** They are inlined into the bundle and shipped to the
  browser. Ask Athena for the secrets-management rules.
{%- if cookiecutter.use_postgres == "yes" %}

## Authentication

Four endpoints — `register`, `login`, `me`, `change-password` — and three decisions that were
made once. Read this before touching `src/lib/auth.ts`, `src/lib/api/auth.ts` or the guard in
`src/router.tsx`.

### The token lives in `sessionStorage`

**What that bought.** The session survives a page refresh. The access token lasts fifteen minutes
and this backend has no refresh endpoint, so an in-memory-only token means F5 signs the user out
— for a form half filled in, that is the difference between a demo and a product.

**What it cost, plainly.** `sessionStorage` is **not** protected from cross-site scripting. Any
script that executes in this page can read the token and use it until it expires, exactly as it
could read `localStorage`. Choosing `sessionStorage` over `localStorage` did not buy XSS safety;
it bought a smaller blast radius — the token is scoped to one tab, it is gone when that tab
closes, and a second tab starts signed out. The visible cost is that last part: open a link in a
new tab and you are not signed in there.

**If you are holding data worth stealing, do it differently.** The shape that removes the XSS
exposure is an httpOnly, `Secure`, `SameSite` cookie the browser attaches and JavaScript cannot
read, with a short-lived access token kept in memory and a refresh endpoint to renew it — plus
CSRF protection, because a cookie is sent on requests you did not initiate. That is backend work
this template has not done; ask Athena for the JWT authentication guide, whose refresh-and-
revocation step is where it starts. Do not simply move the token to `localStorage` and call it
persistence — that is strictly more exposure for a convenience the fifteen-minute lifetime does
not justify.

### A 401 is handled once, in middleware

`enforceSession` in `src/lib/auth.ts`, registered as `onResponse` middleware in
`src/lib/api/client.ts`. It clears the token and hard-navigates to `/login` — a full document
load, so the TanStack Query cache goes with the session rather than sitting in memory for the
next person at the keyboard.

Two guards make it safe, and both are tested: it does nothing when we held no token (a failed
sign-in is not a lost session), and it does not redirect to `/login` from `/login` (that is the
reload loop). **No component, hook or page handles a 401.** Adding one is how two behaviours
appear for the same status.

A **403 is not a 401** and must never be treated as one. `POST /auth/change-password` answers 403
for a wrong current password precisely so the session survives and the form shows the error.

### The whole shell is behind the guard

`appRoute` in `src/router.tsx` — the pathless layout route that renders the sidebar — holds the
`beforeLoad` check. Every page inside the application is under it, `/` included, so a visitor with
no token never reaches a screen and never fires a query. `/login` and `/register` hang off the
root instead, outside the shell: a sidebar of links to pages you cannot open is worse than no
sidebar. Both bounce a caller who already holds a token, and signing in lands on `/`.

A new page goes under `appRoute` and inherits the guard. A page that must be reachable signed out
is a route on `rootRoute` with its own chrome, and it is a decision worth writing down.

There is no `next` parameter — a `next` read from the URL and navigated to is an open redirect
unless it is validated against the route tree. Add it, validated, when there are enough guarded
routes for a redirect-back to be worth having.

**The guard is a UX boundary, not a security one.** The built bundle is static files, served to
anyone who asks for them; `beforeLoad` decides what renders, not what is downloadable. Every route
path, component and API shape in this application is readable by anyone who fetches the JavaScript.
What keeps data private is the backend answering 401 without a valid bearer token — never add a
field to a response on the basis that only a signed-in page displays it.

### Errors are the server's words, with one exception

Render `ApiError.message`. The exception is sign-in: **every** credential failure shows one fixed
sentence, so the UI cannot undo the backend's refusal to say whether an address is registered.
401 and 422 are the same sentence there for the same reason. Registration does disclose a taken
address — that is the backend's deliberate choice, not an accident to copy into the login form.

### Out of scope, on purpose

No refresh flow, no logout-everywhere, no roles, no password reset, no "remember me". The backend
has none of them. Each one is a backend change first.
{%- endif %}

## Where a change goes

| Change | Files |
| --- | --- |
{%- if cookiecutter.use_postgres == "yes" %}
| New page | `src/routes/<page>.tsx` + a route under `appRoute` in `src/router.tsx` (+ a nav entry in `app-shell.tsx`) |
{%- else %}
| New page | `src/routes/<page>.tsx` + a route in `src/router.tsx` (+ a nav entry in `app-shell.tsx`) |
{%- endif %}
| New resource | `src/lib/api/<resource>.ts`: fetchers first, then the hooks over them, keys in one object |
| New shared component | `src/components/` — `src/components/ui/` is shadcn's, added with its CLI |
| Backend API changed | `just gen-api`, then fix what stops compiling |

## Commands

Through `just`, from the repository root. Never invent a raw `docker compose` or `pnpm`
invocation in a script or a workflow.

| Task | Command |
| --- | --- |
| Run the stack | `just dev` (frontend on :3000, backend on :8000) |
| Lint and type check | `just lint` |
| Tests | `just test` |
| One test file | `cd frontend && just test-one src/lib/api/client.test.ts` |
| Regenerate the API client | `just gen-api` |
| Everything CI runs | `just check` |

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
{%- if cookiecutter.use_postgres == "yes" %}
- Do not handle a 401 anywhere but `enforceSession`, and do not read the token from anywhere but
  `src/lib/auth.ts`.
- Do not tell a user at sign-in whether an email address has an account.
{%- endif %}
- Do not edit `src/lib/api/schema.d.ts`.
- Do not introduce a second state library, a second HTTP client, or a second styling system.
- Do not reach past the shell for chrome. A page that renders its own sidebar is a page that
  drifts from every other one.
