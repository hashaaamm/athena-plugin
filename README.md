# Engineering Athena — agent plugin

Rules, guides, flows and knowledge for the code you are about to write, retrieved before you plan
or write it.

This repository is how Athena is installed, in **Claude Code** and in **Cursor**. It holds three
things:

- a **plugin** for both clients, carrying the MCP server and the skill together;
- a **cookiecutter template** for a FastAPI service built to the handbook's standards.

The repository root is the plugin in both formats, which is what lets one `skills/` tree serve
both clients rather than two copies of the same file disagreeing over time.

The plugin is called `athena` to Claude Code and `engineering-athena` to Cursor. Deliberate:
`/plugin install athena@engineering-athena` reads better than the alternative, and a marketplace
listing wants a name nobody else will claim.

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

The plugin is a Cursor Plugin: `.cursor-plugin/plugin.json`, `mcp.json`, `skills/athena/SKILL.md`,
and the listing logo. Cursor asks for `ATHENA_TOKEN` at install time. The value is never in this
repository.

**From GitHub** (works today):

1. Open **Customize → Plugins**.
2. Add from GitHub repository: `https://github.com/hashaaamm/athena-plugin`.
3. Install **engineering-athena**.
4. When Cursor prompts, paste your personal `ath_...` token. Leave the MCP URL on the default
   unless you self-host.

**Official Marketplace** (after review): same screen, search **Engineering Athena**, then Install.
Submit the repo at [cursor.com/marketplace/publish](https://cursor.com/marketplace/publish).

**Local development:**

```bash
mkdir -p ~/.cursor/plugins/local/engineering-athena
rsync -a --exclude .git --exclude templates \
  /path/to/athena-plugin/ ~/.cursor/plugins/local/engineering-athena/
```

Then **Developer: Reload Window**, and confirm the skill and MCP server under Customize. Do not
put a token in any file you copy.

**Team Marketplace:** import this repository under Dashboard → Plugins & MCPs, then turn on
**Enable Auto Refresh**. Cursor re-indexes at most every ten minutes; clients pick up the new
commit on the next focus.

### Either way

Restart the client. `/mcp` in Claude Code, or Settings → MCP in Cursor, should list `athena`, and
the agent should reach for it the next time you ask for a feature.

Both clients load the **same** `skills/athena/SKILL.md`. Not a port of it and not generated from
it — one file, so neither client can be given advice the other was not.

**To update**, in Claude Code:

```bash
/plugin marketplace update
```

In Cursor, an official Marketplace or Team Marketplace install refreshes itself. A personal
GitHub add can stay pinned to the commit you first imported — prefer the marketplace if you
want updates. Knowledge updates need neither: the handbook lives on the server, so standards
and content change without anybody reinstalling anything. A plugin release is only needed when
a *workflow* or the MCP contract changes.

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

Set `ATHENA_MCP_URL`. In Cursor it is a plugin variable you fill in at install time; in Claude Code
it is an environment variable:

```bash
export ATHENA_MCP_URL=https://your-host/mcp
```

Defaults to `https://mcp.engineeringathena.com/mcp`.

## What is in here

```
.claude-plugin/marketplace.json   the Claude Code marketplace
.claude-plugin/plugin.json        the Claude Code manifest, and its MCP server definition
.cursor-plugin/marketplace.json   the Cursor marketplace listing for this repo
.cursor-plugin/plugin.json        the Cursor manifest, logo, and install-time token variable
assets/logo.svg                   the listing icon
mcp.json                          the MCP server definition Cursor reads — placeholders only
skills/athena/SKILL.md            the skill — both clients load this one file
templates/cookiecutter-service/   the FastAPI service template
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
