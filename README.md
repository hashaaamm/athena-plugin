# Engineering Athena — agent plugin

Rules, guides, flows and knowledge for the code you are about to write, retrieved before you plan
or write it.

This repository is how Athena is installed, in **Claude Code** and in **Cursor**. The repository
root is the plugin in both formats, which is what lets one `skills/` tree serve both clients rather
than two copies of the same file disagreeing over time.

The handbook itself is not here. It is served through the MCP server, against the question you
actually asked.

## Not in a marketplace yet

**Athena is not listed in the Cursor Marketplace, and there is no public Claude Code marketplace
entry.** Installing means pointing your client at this repository directly. Both paths below work
today; neither needs a listing.

A marketplace submission is coming. Until it lands, an install stays pinned to the commit you
imported — the handbook itself does not, because standards and content live on the server and
change without anybody reinstalling anything.

## Install

Athena is two things in every client: an **MCP server** that answers questions, and a **skill** that
makes the agent ask. Install one without the other and you have half a product — a server nobody
calls, or a paragraph of advice with nothing behind it.

The quickest route is to hand this to your coding agent:

> Install Engineering Athena from `https://github.com/hashaaamm/athena-plugin`. It is not in a
> marketplace, so add the repository directly. Install **both** halves — the MCP server from the
> plugin manifest, and `skills/athena/SKILL.md`. The server needs `ATHENA_TOKEN` set to the
> personal token I will give you; it is never written into a repository. Then confirm the `athena`
> MCP server is connected and the skill is loaded.

Then give it your `ath_...` token. If you would rather do it by hand:

### Claude Code

```bash
/plugin marketplace add hashaaamm/athena-plugin
/plugin install athena@engineering-athena
```

That adds *this repository* as a marketplace source — it is not the public marketplace. Then put
the token where the plugin can reach it. It declares `Bearer ${ATHENA_TOKEN}` and never holds the
value, because this repository is public and your token is not:

```bash
echo 'export ATHENA_TOKEN=ath_...' >> ~/.zshrc && exec zsh
```

### Cursor

1. Open **Customize → Plugins**.
2. Add from GitHub repository: `https://github.com/hashaaamm/athena-plugin`.
3. Install **engineering-athena**.
4. When Cursor prompts, paste your personal `ath_...` token. Leave the MCP URL on the default
   unless you self-host.

The plugin is called `athena` to Claude Code and `engineering-athena` to Cursor. Deliberate:
`/plugin install athena@engineering-athena` reads better than the alternative, and a listing wants
a name nobody else will claim.

### Either way

Restart the client. `/mcp` in Claude Code, or Settings → MCP in Cursor, should list `athena`, and
the agent should reach for it the next time you ask for a feature.

Both clients load the **same** `skills/athena/SKILL.md`. Not a port of it and not generated from
it — one file, so neither client can be given advice the other was not.

## Getting a token

Every caller authenticates; there is no anonymous access. Ask whoever runs your Athena instance for
a token. It is issued per person, which is what lets the service tell callers apart and what makes a
reference issued to you resolve only for you.

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

## Licence and data

MIT, in [LICENSE](LICENSE). What the plugin and the hosted MCP do with what an agent sends is in
[PRIVACY.md](PRIVACY.md).

**This repository is a distribution channel, not a content channel.** What is here is what cannot be
delivered any other way — a plugin has to be fetched from a URL. The handbook's rules, guides, flows
and knowledge are delivered through the MCP server, where they can be retrieved against the question
you actually asked; publishing them as files would be a worse product and a bigger one.
