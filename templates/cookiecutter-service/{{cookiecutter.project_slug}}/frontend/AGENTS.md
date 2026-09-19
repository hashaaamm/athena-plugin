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

## Where a change goes

| Change | Files |
| --- | --- |
| New page | `src/routes/<page>.tsx` + a route in `src/router.tsx` (+ a nav entry in `app-shell.tsx`) |
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
| One test file | `cd frontend && just test-one src/routes/items.test.tsx` |
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
- Do not edit `src/lib/api/schema.d.ts`.
- Do not introduce a second state library, a second HTTP client, or a second styling system.
- Do not reach past the shell for chrome. A page that renders its own sidebar is a page that
  drifts from every other one.
