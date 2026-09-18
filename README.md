# Engineering Athena — Claude Code plugin

Rules, guides, flows and knowledge for the code you are about to write, retrieved before you plan
or write it.

This repository is how Athena is installed, in **Claude Code** and in **Cursor**. It holds three
things:

- a **plugin** for Claude Code, carrying the MCP server and the skill together;
- a **cookiecutter template** for a FastAPI service built to the handbook's standards.

Cursor installs the same two halves by hand and reads the same skill file, so there is only one
copy of the guidance in here.

The handbook itself is not here. It is served through the MCP server, against the question you
actually asked.

## Install

Athena is two things in every client: an **MCP server** that answers questions, and a **file of
guidance** that makes the agent ask. Install one without the other and you have half a product — a
server nobody calls, or a paragraph of advice with nothing behind it.

Both clients are first-class. Pick yours.

### Claude Code

The plugin carries both halves, so this is two commands:

```bash
/plugin marketplace add hashaaamm/athena-plugin
/plugin install athena@engineering-athena
```

Then put the token where the plugin can reach it — it declares `Bearer ${ATHENA_TOKEN}` and never
holds the value itself, because this repository is public and your token is not:

```bash
echo 'export ATHENA_TOKEN=ath_...' >> ~/.zshrc && exec zsh
```

### Cursor

Cursor has no plugin mechanism, so the two halves install separately — but it reads the **same
skill file**, so there is nothing to convert.

**The server** — add this to `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "athena": {
      "url": "https://mcp.engineeringathena.com/mcp",
      "headers": { "Authorization": "Bearer ath_..." }
    }
  }
}
```

The file in your **home directory**, not a `.cursor/mcp.json` inside a repository: the token goes
in literally, and a token in a tracked file is a committed credential.

**The skill** — one command:

```bash
mkdir -p .cursor/skills/athena && curl -fsSL \
  https://raw.githubusercontent.com/hashaaamm/athena-plugin/main/plugins/athena/skills/athena/SKILL.md \
  -o .cursor/skills/athena/SKILL.md
```

Byte for byte the file the Claude Code plugin ships. Not a port of it, not generated from it — the
same file, so the two clients cannot be given different advice.

### Either way

Restart the client. `/mcp` in Claude Code, or Settings → MCP in Cursor, should list `athena`, and
the agent should reach for it the next time you ask for a feature.

Both clients read the same `SKILL.md`, so neither can end up giving a different answer to the same
question.

## Getting a token

Every caller authenticates; there is no anonymous access. Ask whoever runs your Athena instance
for a token. It is issued per person, which is what lets the service tell callers apart and what
makes a reference issued to you resolve only for you.

## The service template

A FastAPI service laid out the way the handbook argues for: routers that call a facade, a facade
that calls services, services that call repositories, and nothing reaching backwards. Alembic,
Docker, CI and CD, a justfile, tests, and an `AGENTS.md` so an agent opening the repository knows
the rules before it writes anything.

```bash
pipx install cookiecutter   # or: uv tool install cookiecutter
cookiecutter gh:hashaaamm/athena-plugin --directory templates/cookiecutter-service
```

It asks for a project name and derives the rest. `include_frontend` adds a React workspace;
`use_postgres` and `use_sentry` drop the parts you are not using rather than leaving them stubbed.

## What you get



Four categories, and the difference is what you do with each:

| Category | You | Binding |
| --- | --- | --- |
| **Rules** | adhere — deviating needs a reason | yes, graded MUST / SHOULD / MAY |
| **Guides** | follow, to build one specific thing | no, but the rules they cite are |
| **Flows** | orchestrate, one sub-agent per step | only through what they compose |
| **Knowledge** | hold in mind while doing something else | no |

The tool an agent reaches for first is `athena_context`: it takes a task and the requirements read
out of it, and answers all four categories in one call with every rule and guide labelled by the
requirement it answers. `plan=True` returns the work as ordered steps graded must / should / may /
optional, each with the verification its guide gave for it.

## Pointing at your own instance

```bash
export ATHENA_MCP_URL=https://your-host/mcp
```

Defaults to `https://mcp.engineeringathena.com/mcp`.

## What is in here

```
.claude-plugin/marketplace.json           the marketplace
plugins/athena/.claude-plugin/plugin.json the plugin, and the MCP server definition
plugins/athena/skills/athena/SKILL.md     the skill — both clients read this one file
templates/cookiecutter-service/           the FastAPI service template
```

And nothing else, on purpose. **This repository is a distribution channel, not a content channel.**
What is here is what cannot be delivered any other way: a plugin has to be fetched from a
marketplace, and `cookiecutter` works by cloning a git repository, so both need a public URL. The
handbook's rules, guides, flows and knowledge are delivered through the MCP server, where they can
be retrieved against the question you actually asked — publishing them as files would be a worse
product and a bigger one.

The skill and the template are authored in the handbook's own repository and copied here in one
direction. There is exactly one copy of each: two files that are supposed to say the same thing
will not, and the disagreement is invisible until somebody gets different advice from the same
product depending on which editor they opened.
