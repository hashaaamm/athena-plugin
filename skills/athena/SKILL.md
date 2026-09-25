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

### Say the subject. A filename is not a subject.

Athena does not infer what you are doing from what you are touching, and it will not pretend to. A
path answers "which files", and the rules are written about "which question" — adding a column,
renaming one and fixing a slow query all touch the same model file and are bound by different
rules. So the subject is yours to state, in `requirements` here and in `topics` on `athena_rules`.

`paths` still earns its place, and it is worth passing alongside the subject rather than instead of
it: a page that declares it governs `app/models/**` will surface a rule you did not know to ask
about, which is the entire reason to consult a handbook. But a path only answers when some page
declared it. When none has, `athena_rules` returns no rules and an `advice` string saying so.

**That empty answer means "nobody wrote down which files this governs" — never "no rules apply".**
Do not read it as a clean bill of health and carry on. Name the subject and ask again.

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
reference.

## Running a flow: you orchestrate, you do not execute

A flow response carries `fan_out`, and `fan_out.required` is **always true**. It is an instruction,
not a summary to skim. A flow you execute yourself, unit after unit in your own context, is the one
way of running it that is wrong — and it is the easy mistake, because executing feels like progress
and delegating feels like overhead.

It is wrong for a specific reason. By the last unit, your context is mostly the previous units'
rules, file lists and output, and the agent writing that unit is the one paying least attention to
it. The plan/brief split exists to stop exactly that, and `athena_brief` returns a ready-made
`spawn_prompt` precisely so you never have to read the work you are handing off.

**The trigger is the unit count, not the size of the job.** `athena_flow` came back with more than
one unit: fan out. Anything else — a guide, a context response, a `mode="map"` list, or a flow that
pruned down to a single unit — you do yourself. A two-step guide split across two sub-agents is
ceremony; a fourteen-unit deploy run in one context is the mess this section exists to prevent.

So, mechanically:

1. **Walk `fan_out.groups` in order.** Each has a 1-based `group`. Never start one early — a later
   group depends on something an earlier one produces.
2. **Inside a group, look at `run_concurrently`.** When it is true, spawn *every* step in that group
   at once, in a single message with one sub-agent call per step. Spawning them one after another
   and waiting for each is a serial run wearing a parallel directive. `mode` is the overall
   shape — `sequential` means every group is one step, and still one sub-agent per step.
3. **One sub-agent per step in `steps`.** Give it `brief_call` and have it call `athena_brief`
   itself, in its own context, and use the returned `spawn_prompt` as its system prompt. Pass that
   through without reading it.
4. **Pass `must_not_edit` to each sub-agent** as files it may not touch. That list is how two agents
   in one group stay out of each other's way.
5. **Stop before an `external` unit.** A unit with `external: true` in `units` has effects outside
   the repository and spends money — a project, a bucket, a live service. Confirm with the person
   before you spawn it, every time. Read the flag off each unit: the directive says the condition
   is present, not which steps or how many.
6. **Wait for the whole group, then check `after_each_group`** before starting the next one. Each
   sub-agent reports the files it changed and whether its `verify` checks passed; that result is
   what you gate on, and it is all you take back.
7. **On a failure, do what `on_failure` says** — which includes reporting it with `athena_feedback`,
   for each unit whose verification failed.

Your own context stays thin on purpose: the plan, the ordering, the checkpoints and each sub-agent's
summary — never a unit's contents. Do not fetch briefs for units you are not about to run, and do not
read a unit's brief in order to "check" a sub-agent — you are reassembling in your context the thing
you just split.

## The request is the scope

A flow covers a whole process. The person asked for part of it. **Do the part they asked for.**

"Set up a Django app" reaches the React + Django flow, because that flow is where the Django
knowledge lives. It is not permission to set up React, and it is not a reason to ask whether they
would like React too. They told you what they wanted; treating that as an open question spends
their turn re-answering it.

So: read the scope out of the request, drop the units outside it, and get on with the rest.

**Then check what you dropped was not load-bearing.** Every unit declares `produces`, and later
units declare `requires`. A dropped unit whose output nothing needs is simply gone. A dropped unit
whose output a later unit requires is different: that later unit now has an unmet requirement and
needs adapting.

In the Django case that is exactly what happens — the frontend unit produces `react-app`, and the
infrastructure unit requires it because it declares two Cloud Run services. Dropping React means
the infrastructure unit builds one service, not two.

Say so in one line — "no frontend, so the stack declares one Cloud Run service rather than two" —
and carry on. Do not silently emit a plan whose fourth unit cannot run, and do not turn it back
into a question. A deviation the person can see is fine; one they discover at deploy time is not.

Pass `already_done` for capabilities the repository genuinely has, so the flow drops those steps
itself rather than you skipping them by hand.

## Asking the person, when you must

Sometimes a question is unavoidable: two pages fit equally and the repository does not say which,
or a choice changes what gets written and nothing in the request implies it.

Ask it as a **choice, not a blank**. Offer the concrete options you are actually deciding between
and let the person pick one, using whatever affordance your client has for a structured choice
rather than asking them to type the answer in prose. A free-text prompt makes the person guess
what shape of answer you wanted, and then you parse their guess.

Never ask more than one thing at once, and never ask about something the request already settled.

## The other tools

Reach for these when you already know which question you are asking.

| Tool | Ask it |
| --- | --- |
| `athena_rules` | "Here is the diff — what must hold?" **Say what the change is about: `topics=['authorization', 'migrations']`.** `paths` narrows it further, but only answers for files a page declares it governs — see below. A framework alone is refused. |
| `athena_guide` | "How do I build X?" One guide, with its code and the rules that bind it. |
| `athena_knowledge` | "What must I keep in mind about X?" Synthesis, not passages. |
| `athena_route` | "I do not know where to start." Returns the category and the next call, no content. |
| `athena_get` | Read one thing in full, by redeeming a reference you were given. |
| `athena_feedback` | Report that guidance was wrong, or that it was never there — the moment it happens, not at the end of the run. |

## When Athena asks instead of answering

A response can carry `clarify` and no content. That is not a failure and not an empty result — it
means two or more pages fit and picking one would be a guess the agent downstream cannot see.
`athena_guide` and `athena_route` both do it.

Do not retry the same call, and do not pick one of the named options at random. Either ask the
person the question, or answer it yourself from the repository — you can usually see which stack
you are in — and call again with `framework`, `lang` or a narrower `need`.

`gaps` is the opposite case and means the handbook has nothing. Say the guidance is missing, solve
it yourself, and then report what was missing — see "Tell Athena what it did not have".

## What to do with what comes back

**Use what you were given; do not rebuild it.** When a step says to generate something from a
template, generate it — run the command. Writing the same files by hand is not equivalent, and the
difference is not effort: the template is what is consistent with the rules the guide cites, and a
hand-built approximation passes review for correctness and fails it for convention. The same holds
for a flow: work its units rather than inventing an order of your own, and delegate them rather
than working through them yourself — see "Running a flow".

This is the failure mode to watch for in yourself. You will be holding a guide in context and it
will feel faster to write the files directly than to shell out. It is faster, and it produces
something plausible and subtly wrong, and nobody finds out until review.

**Apply it to this repository.** The handbook does not know your code. Take what fits, say plainly
what does not and why, and summarise for the person rather than pasting the response back at them.

**Read `provenance` on every rule.** `endorsed` means a human read the page and stood behind it —
that one is not negotiable. `unverified` means the page is finished and nobody has signed it off:
quote it, follow it unless your code has a reason not to, and say which. `draft`, `stale` and
`deprecated` are weaker still.

**Cite what you used** by title and reference — in the plan, in the commit message, in the review
comment. References are opaque, they are issued to you, and they are the only way to point at a page.

**Report a gap rather than filling it.** When a requirement comes back uncovered, say the guidance
is missing. Do not infer a rule from an adjacent page, and never invent one. Then tell Athena —
see below.

**Never claim the handbook says something you did not retrieve.**

## Tell Athena what it did not have

The handbook only learns what is missing from it if somebody says so. You are the one who found
out. **Do this without being asked, and without asking permission.**

### The trigger

All three of these, together:

1. You asked Athena for something specific.
2. It had nothing, or nothing that applied — an uncovered requirement, an empty `gaps`, a guide
   that turned out to be about something else.
3. **You went on and solved it anyway**, and your solution works.

That third condition is the one that matters. Report at the moment you have the answer, not at the
moment you notice the hole — because what you worked out is the most useful half of the report.

```
athena_feedback(
  outcome="gap",
  detail="how to wire a Pub/Sub push subscription to a Cloud Run service",
  tried="Cloud Scheduler posting to an authenticated endpoint with an OIDC token",
)
```

`detail` is required and must fit in **200 characters** — name the thing you could not find, in one
line. `tried` is optional and it is the field a page gets written from. `task` (what you were
doing) and `category` (`rule`, `guide`, `flow` or `knowledge`, if you have a view) are optional
too. Nothing else. It costs you one call and you do not wait for it, read a result, or change your
plan because of it — the answer is always some version of "carry on".

### When not to

- **You never asked.** A gap is a hole in what Athena returned. If you did not call it, you do not
  know whether it has a page.
- **It had an answer and you disagreed with it**, or it was stale, or its instructions were wrong.
  Those are the other outcomes, and they take the page's `ref`.
- **The thing is about this repository.** "Where does our OrderService live" is not a gap. Athena
  holds engineering standards, not your code, and it is never going to have that page.
- **You have not solved it yet.** Finish first. A report filed mid-struggle describes the
  confusion rather than the answer.
- **You already reported it this session.** Once per gap. Athena counts callers, so filing the
  same thing five times looks like one caller who repeats themselves, not five who need the page.

The failure mode to watch in yourself is over-reporting: every session touches something the
handbook does not cover, because no handbook covers everything. Reserve this for the things that
actually cost you time and that another engineer would plainly have wanted a page for. Two or
three reports in a long session is a lot.

### Keep it clean

Everything you send is read by a person and stored. Write a summary, never a paste. No logs, no
stack traces, no diffs, no file contents, no environment variables, no URLs from your repository,
no customer names. If a sentence would be odd to say out loud in somebody else's standup, do not
send it.

## Installing

The plugin carries this skill and the MCP server together, because they are two halves of one
thing — the server can answer a question, and the skill is what makes the agent ask. The token
is never written into this repository. A credential in a tracked `.mcp.json` is a committed
credential. What the hosted server does with a tool call is in `PRIVACY.md`.

### Cursor

Install **engineering-athena** from Customize → Plugins (Marketplace, or add
`https://github.com/hashaaamm/athena-plugin` from GitHub). Cursor prompts for `ATHENA_TOKEN`.
Leave the MCP URL on the default unless you self-host.

### Claude Code

```bash
/plugin marketplace add hashaaamm/athena-plugin
/plugin install athena@engineering-athena
echo 'export ATHENA_TOKEN=ath_...' >> ~/.zshrc && exec zsh
```

### Without the plugin

If you are self-hosting, or you want the server without the skill, add the MCP at user scope —
the token is tied to you rather than to the repository — and copy this file to
`~/.cursor/skills/athena/SKILL.md` or `~/.claude/skills/athena/SKILL.md`.

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
