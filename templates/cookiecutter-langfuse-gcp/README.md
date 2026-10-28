# cookiecutter-langfuse-gcp

> **This is a published copy.** It is generated from Athena's source and rewritten on every publish, so an edit made here is lost at the next one. Report a problem as an issue on the repository you found it in.

**Self-hosted [Langfuse](https://langfuse.com) on Google Cloud, generated as one component of a
repository.** It writes `<repo-root>/<component>/`, `langfuse` by default: a Terraform root module
that calls Langfuse's own module,
[`langfuse/langfuse-terraform-gcp`](https://github.com/langfuse/langfuse-terraform-gcp) 1.0.10, the
way [Langfuse's GCP deployment guide](https://langfuse.com/self-hosting/deployment/gcp) does — GKE
Autopilot, Cloud SQL, Memorystore, a bucket, in-cluster ClickHouse and the official Helm chart —
plus what a team needs around it: a CMEK state bucket, keyless CI with a read-only plan identity and
an apply identity gated by a GitHub environment, a plan on every pull request and an apply on every
merge.

It is Terraform because that is how Langfuse ships and maintains its GCP deployment. Every other
template here is Pulumi; this one is not, by design.

## Use it

Run it from anywhere, with `-o` naming the repository root.

```bash
uvx cookiecutter gh:hashaaamm/athena-plugin --directory templates/cookiecutter-langfuse-gcp -o .   # interactive
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-langfuse-gcp -o . \
  gcp_project_id=acme-obs-dev gcp_region=europe-west1 github_repository=acme/platform \
  hostname=langfuse.acme.dev

# Into a GKE cluster you already run, with state in a bucket other components share
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-langfuse-gcp -o . \
  gcp_project_id=acme-obs-dev github_repository=acme/platform hostname=langfuse.acme.dev \
  cluster_mode=existing existing_cluster_name=shared existing_cluster_location=europe-west1 \
  state_bucket=acme-obs-dev-tf-state
```

| Input | Default | What it decides |
| --- | --- | --- |
| `project_name` | `Langfuse` | the heading in the generated README and AGENTS.md |
| `component` | `langfuse` | the folder, the workflow prefix, the `just` module, the module's `name`; 6–16 characters |
| `gcp_project_id`, `gcp_region` | — | where everything is created; one project per environment |
| `github_repository` | — | the only repository Workload Identity Federation trusts |
| `profile` | `small` | `small` (~$250–330/month) or `ha` (the module's defaults, ~$1,000/month) |
| `hostname` | `langfuse.example.com` | the URL; you delegate DNS for it after the first apply |
| `cluster_mode` | `new` | `new`: the module creates the cluster. `existing`: install into `existing_cluster_name` in `existing_cluster_location` |
| `state_bucket` | `<project>-tf-state` | a new bucket the bootstrap creates, or an existing one it checks; state goes under `<component>/<env>` |

Generating the same component twice into one repository is refused before anything is written.

## What lands outside the folder

Three things, each namespaced by the component, as every template here does it:

- `.github/workflows/<component>-infra.yml` and `<component>-ci.yml`;
- `mod <component>` in the root `justfile`;
- a line in the root `AGENTS.md`.

## Then

Read the generated `<component>/README.md`. It has the cost table, the first-deploy sequence — the
first apply runs once from an operator's machine, every later one from CI — DNS, single sign-on, how
to send traces over OTLP, upgrades, backups and teardown. `just lint` checks everything without
credentials; nothing touches the cloud before `just bootstrap`.

The module keeps the secrets it generates — database and ClickHouse passwords, `SALT`,
`NEXTAUTH_SECRET`, `ENCRYPTION_KEY` — in Terraform state. The state bucket is therefore
CMEK-encrypted, versioned, closed to the public and readable only by the operators and the two CI
identities.
