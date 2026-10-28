# Agent rules — {{ cookiecutter.project_name }} (self-hosted Langfuse)

The general standards come from Athena: ask it for what a change needs before planning it — the
athena skill says how. This file is the map of the `{{ cookiecutter.component }}` component — this folder of the
repository — and the rules specific to it. The repository root's `AGENTS.md` lists every component.

**This component is Terraform by design.** It deploys Langfuse with Langfuse's own Terraform module,
because the person who generated it chose that. Athena's Pulumi standards — MUST-1, "declare
infrastructure in Pulumi", and its "MUST NOT introduce Terraform" — do not apply to it. Athena may
return those rules for a change here; do not "fix" this component into Pulumi. Athena's Terraform
standards do apply.

## When something does not work

Read upstream before changing anything, in this order, and say which source the fix came from:

1. **The module at the pinned version, 1.0.10**: its README and `variables.tf` —
   <https://github.com/langfuse/langfuse-terraform-gcp/tree/1.0.10>. A variable that is not in
   that `variables.tf` does not exist; do not invent one.
2. **Langfuse's GCP deployment guide** — <https://langfuse.com/self-hosting/deployment/gcp> — and the
   self-hosting docs it links (configuration, upgrade, authentication and SSO).
3. **The Helm chart at the pinned version, 2.1.7** — <https://github.com/langfuse/langfuse-k8s>,
   its `values.yaml` and `TROUBLESHOOTING.md`.

Known first-deploy symptoms are in the module's README: an `ERR_SSL_VERSION_OR_CIPHER_MISMATCH` for
up to an hour is a managed certificate still `PROVISIONING`, not a fault.

## Where things live

| Path | What it is |
| --- | --- |
| `infra/terraform/` | The root module. `new_cluster.tf` calls the official module; `existing_cluster.tf` mirrors it for a cluster this component does not own, citing the module file and line for each resource |
| `infra/terraform/environments/` | `<env>.tfvars` and `<env>.backend.hcl`, one pair per environment, committed, nothing secret |
| `infra/terraform/tests/` | `terraform test` plans against mocked providers; no credentials |
| `infra/bootstrap/state-bucket.sh` | APIs and the state bucket, created or checked; never Terraform |
| `infra/scripts/` | `tf.sh` (Terraform natively or in the pinned image), `up.sh`, `sync-github.sh`, `teardown.sh` |
| `.github/workflows/{{ cookiecutter.component }}-infra.yml`, `{{ cookiecutter.component }}-ci.yml` | at the repository root; they read only `{{ cookiecutter.__component_var }}_*` repository variables |

## Commands

From this folder, or as `just {{ cookiecutter.component }} <recipe>` from the root. Never a raw `terraform` or
`docker` invocation in place of a recipe.

| Task | Command |
| --- | --- |
| Everything CI's static job runs, no credentials | `just lint` |
| Format | `just fmt` |
| What an apply would change | `just plan` |
| Regenerate the lock file after a constraint change | `just lock` |
| Outputs (URL, name servers, identities) | `just output` |

`just bootstrap`, `just up`, `just sync-github` and `just teardown` create, change or destroy cloud
resources. Do not run them unless the person asks for exactly that.

## Rules for changes here

- **Applies run in CI.** After the first apply, every change is a pull request whose plan is read and
  which `{{ cookiecutter.component }}-infra.yml` applies on merge. Do not apply from a laptop; `just up` refuses,
  and `--break-glass` is the person's decision, not yours.
- **No secret in a file.** Not in `.tf`, not in `.tfvars`, not in a variable default. A secret Langfuse
  needs from outside — an SSO client secret, an LLM key — goes in a Kubernetes secret a person
  creates, and Terraform references it by name, as `google_sso` does.
- **The state is secret.** It holds every generated Langfuse password and key. Never print state,
  `terraform output -json` of a sensitive value, or a saved plan; never upload a plan file.
- **Never replace `ENCRYPTION_KEY` or `SALT`.** Both are `random_bytes` in state. A plan that replaces
  either breaks stored credentials and every API key; stop and tell the person.
- **Versions move in their own pull request**: the module tag, `langfuse_chart_version`,
  `langfuse_app_version`, a provider constraint (then `just lock`). A module bump also carries its
  changes into `existing_cluster.tf`.
- **A new resource type** needs its role in `apply_roles` in `identities.tf`, in the same change, or
  the apply on main fails on a permission.
- **Only variables the module declares.** Check them in the module's `variables.tf` at the pinned tag.
