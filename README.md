# Engineering Athena — Claude Code plugin

Rules, guides, flows and knowledge for the code you are about to write, retrieved before you plan
or write it.

This repository is the distribution: a plugin marketplace holding one plugin. The plugin carries
two things that only work together — an **MCP server** that answers questions about engineering
standards, and a **skill** that teaches an agent when to ask. A server nobody calls is a server
that does nothing, and a skill with nothing behind it is a paragraph of advice.

The handbook itself lives elsewhere and is not in this repository.

## Install

```bash
# in Claude Code
/plugin marketplace add hashaaamm/athena-plugin
/plugin install athena@engineering-athena
```

Then put your token where your shell will find it:

```bash
echo 'export ATHENA_TOKEN=ath_...' >> ~/.zshrc && exec zsh
```

Restart Claude Code. `/mcp` should list `athena`, and the agent should reach for it the next time
you ask for a feature.

**Why an environment variable rather than the plugin holding the token.** The plugin is public and
your token is not. It is read from the environment at connect time, so it is never written into a
file in a repository — a credential in a tracked `.mcp.json` is a committed credential.

## Getting a token

Every caller authenticates; there is no anonymous access. Ask whoever runs your Athena instance
for a token. It is issued per person, which is what lets the service tell callers apart and what
makes a reference issued to you resolve only for you.

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
.claude-plugin/marketplace.json          the marketplace
plugins/athena/.claude-plugin/plugin.json the plugin, and the MCP server definition
plugins/athena/skills/athena/SKILL.md     the skill
```

The skill is authored in the handbook's own repository and synced here, so there is one source of
truth for it rather than two copies drifting apart.
