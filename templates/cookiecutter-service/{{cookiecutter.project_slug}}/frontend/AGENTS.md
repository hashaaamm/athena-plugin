# Agent rules — frontend

Read [../AGENTS.md](../AGENTS.md) and the handbook's `AGENTS.md` first.

There is no application here yet. Do not scaffold one as a side effect of another task — creating
it is its own decision, with its own PR.

When it exists:

- The handbook's frontend rules are authoritative. Ask Athena for them.
- The API client is **generated** from the backend's OpenAPI document. Never hand-write a type that
  the backend already describes, and run `just gen-api` on every API change.
- Commands go through `just`, from the repository root.
