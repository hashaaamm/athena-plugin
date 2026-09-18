# Privacy

This plugin is a client. It does not store your token, your repository, or the answers it
retrieves. What leaves your machine is what the agent sends to the Athena MCP server you
configured.

## What the plugin holds

Nothing. Cursor and Claude keep `ATHENA_TOKEN` in their own secret store (a plugin variable, or
your shell environment). This repository only declares the placeholder `${ATHENA_TOKEN}`.

## What the server receives

Each tool call is an HTTP request to `ATHENA_MCP_URL` (default
`https://mcp.engineeringathena.com/mcp`) with:

- the `Authorization: Bearer` header, so the server knows which person is asking
- the arguments the skill told the agent to send: the task, the requirements, optional file
  paths, and — for `athena_rules` — the diff or topics being checked

That is what is required to retrieve handbook pages. The plugin does not upload your repository
as a tree, and it does not send the token to anyone except the MCP URL you set.

## What the server stores

- The personal access token is stored as a SHA-256 digest. The plaintext value is shown once at
  creation and cannot be read back.
- Account credentials use Argon2id. Deleting an account revokes credentials immediately.
- Feedback submitted through `athena_feedback` goes to human reviewers. It is not written into
  the handbook corpus.

## What we do not do

- We do not use Plugin Data or User Content to train or fine-tune models.
- We do not sell, rent, or transfer Plugin Data to third parties.
- We do not charge for this plugin. The Marketplace listing is free. A token is how the hosted
  service tells callers apart.

## Self-hosted instances

If you set `ATHENA_MCP_URL` to your own host, that host is the processor. This file describes
the hosted service at `mcp.engineeringathena.com`.

## Contact

Questions about this plugin: open an issue on
[hashaaamm/athena-plugin](https://github.com/hashaaamm/athena-plugin).
Security incidents affecting the plugin: follow the host's own disclosure path, and for a
Cursor Marketplace listing notify `legal@cursor.com` as required by the Publisher Terms.
