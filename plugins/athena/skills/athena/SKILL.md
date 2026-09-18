---
name: athena
description: Use the Engineering Athena handbook before planning or writing code. Call it when a feature, endpoint, model, migration, dependency choice or review is on the table, and when a standard or a house convention would change what you write.
---

# Using the Engineering Athena handbook

Athena serves four kinds of content: **Rules** you adhere to, **Guides** you follow, **Flows** you
orchestrate, and **Knowledge** you hold in mind. It is retrieval, not a policy engine — it hands you
content, and you decide what applies to the repository in front of you.

## When to call it

Before you plan, and before you write, when the work involves any of:

- a new endpoint, model, migration, background job or service
- picking a library, a pattern or an approach where a house choice may exist
- reviewing a diff against standards
- anything where "how do we do this here?" has an answer you do not already hold

Do not call it for a typo, a rename, or a question about this repository's own code.

## The default: one call

`athena_context` returns all four categories at once. Read the request, work out what the change
actually needs, and pass that as `requirements` — one per thing:

```
athena_context(
  task="add an /orders endpoint where a user can create an order",
  requirements=[
    "a database model and a migration",
    "request and response schemas",
    "the business logic",
    "error handling",
    "the api route itself",
  ],
  framework="fastapi",
  lang="python",
)
```

**Stating the requirements is the whole trick.** Each one is searched for separately, and every rule
and guide comes back labelled with the requirement it answers. Without them the task is one query
and you get one query's worth of answer.

Work out the requirements by reading the repository first — what exists, what is missing, what the
change touches. `paths` sharpens it further: pass the files the change will write.

## Planning mode

When you are producing a plan rather than code, add `plan=True`. You get the work back as ordered
steps, each graded **must**, **should**, **may** or **optional** by the strongest rule that binds it,
with the verification a guide gave for it.

Carry those levels into the plan you show the person. `must` steps are not the optional pile, and
the order is the order to do the work in — do not re-sort by level.

## Step at a time, or across sub-agents

`mode="map"` returns titles, references and the exact call to fetch each piece, and inlines nothing.
Use it when:

- the person wants to go one step at a time rather than read everything at once
- you are about to fan work out, and each sub-agent should pull its own content into its own context

For genuinely multi-step work, a context response may offer a **flow**. Call `athena_flow` with that
reference: it returns the units, which of them collide over the same files, and a fan-out directive.
Then spawn one sub-agent per unit and have each call `athena_brief` for its own unit. Do not read
every unit's brief yourself — that puts the whole flow in one context, which is what the split exists
to prevent.

## The other tools

Reach for these when you already know which question you are asking.

| Tool | Ask it |
| --- | --- |
| `athena_rules` | "Here is the diff — what must hold?" Scope it with `paths` or `topics`; a framework alone is refused. |
| `athena_guide` | "How do I build X?" One guide, with its code and the rules that bind it. |
| `athena_knowledge` | "What must I keep in mind about X?" Synthesis, not passages. |
| `athena_route` | "I do not know where to start." Returns the category and the next call, no content. |
| `athena_get` | Read one thing in full, by redeeming a reference you were given. |
| `athena_feedback` | Report that guidance was wrong — the moment it fails, not at the end of the run. |

## When it asks instead of answers

A response can carry `clarify` and no content. That is not a failure and not an empty result — it
means two or more pages fit and picking one would be a guess the agent downstream cannot see.
`athena_guide` and `athena_route` both do it.

Do not retry the same call, and do not pick one of the named options at random. Either ask the
person the question, or answer it yourself from the repository — you can usually see which stack
you are in — and call again with `framework`, `lang` or a narrower `need`.

`gaps` is the opposite case and means the handbook has nothing. Say the guidance is missing.

## What to do with what comes back

**Apply it to this repository.** The handbook does not know your code. Take what fits, say plainly
what does not and why, and summarise for the person rather than pasting the response back at them.

**Read `provenance` on every rule.** `endorsed` means a human read the page and stood behind it —
that one is not negotiable. `unverified` means the page is finished and nobody has signed it off:
quote it, follow it unless your code has a reason not to, and say which. `draft`, `stale` and
`deprecated` are weaker still.

**Cite what you used** by title and reference — in the plan, in the commit message, in the review
comment. References are opaque, they are issued to you, and they are the only way to point at a page.

**Report a gap rather than filling it.** When a requirement comes back uncovered, say the guidance
is missing. Do not infer a rule from an adjacent page, and never invent one.

**Never claim the handbook says something you did not retrieve.**

## Installing

Two commands, and one credential. The plugin carries this skill and the MCP server together,
because they are two halves of one thing — the server can answer a question, and the skill is what
makes the agent ask.

```bash
# in Claude Code
/plugin marketplace add hashaaamm/athena-plugin
/plugin install athena@engineering-athena
```

Then put the token you were given where your shell will find it:

```bash
echo 'export ATHENA_TOKEN=ath_...' >> ~/.zshrc && exec zsh
```

The token is read from the environment, so it is never written into a file in your repository. A
credential in a tracked `.mcp.json` is a committed credential.

### Without the plugin

If you are self-hosting, or you want the server without the skill:

```bash
claude mcp add --scope user --transport http athena https://<your-athena-host>/mcp \
  --header "Authorization: Bearer $ATHENA_TOKEN"
```

User scope, not project scope: the token is tied to you rather than to the repository. Copy this
file to `~/.claude/skills/athena/SKILL.md` to get the skill as well.

### Getting a token

Ask whoever runs your Athena instance. If that is you, mint one over HTTP — the value is returned
once and never again, because only its SHA-256 digest is stored:

```bash
BASE=https://<your-athena-host>
curl -sX POST $BASE/api/v1/auth/register -H 'content-type: application/json' \
  -d '{"email":"you@example.com","password":"at-least-twelve-chars"}'
curl -sX POST $BASE/api/v1/auth/login -H 'content-type: application/json' \
  -d '{"email":"you@example.com","password":"at-least-twelve-chars"}'
curl -sX POST $BASE/api/v1/tokens -H "authorization: Bearer <access_token>" \
  -H 'content-type: application/json' -d '{"name":"laptop"}'
```

Lost it? Revoke and mint another. There is no way to read one back.
