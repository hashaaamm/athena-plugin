# Frontend

A placeholder. Nothing is built here yet — the directory exists so that adding a web client is
`pnpm create` inside it plus three lines elsewhere, rather than a repository reshuffle.

## What already anticipates you

| Already in place | What to do when the real app lands |
| --- | --- |
| Root `justfile` delegates `lint`, `fmt`, `test` here | Replace the placeholder recipes in `frontend/justfile` |
| Root `docker-compose.yml` orchestrates the stack | Add a `frontend` service; the commented block shows where |
| `.github/workflows/ci.yml` is filtered on `backend/**` | Add `frontend-ci.yml` with its own `paths:` filter, so neither component waits on the other's runner |
| Root `.env.example` is stack-wide | Vite reads `VITE_`-prefixed variables; add them there |
| `.gitignore` already ignores `node_modules/` and `dist/` | Nothing |

## Conventions

The handbook's `docs/rules/frontend/` pages apply. The API client is generated from the backend's
OpenAPI document — wire up a `just gen-api` recipe and run it on every API change, so a stale
client is a compile error rather than a runtime surprise
(`docs/rules/backend/project-structure.md` MUSTs it for monorepos).
