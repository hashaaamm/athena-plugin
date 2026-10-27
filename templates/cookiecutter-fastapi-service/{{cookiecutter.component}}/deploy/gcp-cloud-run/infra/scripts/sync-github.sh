#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates
# .github/workflows/{{ cookiecutter.component }}-cd.yml.
#
# Every variable is prefixed {{ cookiecutter.__component_var }}_: the repository may hold other components, and each
# reads only its own. A repository variable without the prefix is not this component's.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. Workload
# Identity Federation means CI holds no credential at all — it exchanges GitHub's own OIDC token
# for a short-lived Google one, and every value below is a public identifier: a project id, a
# region, a service-account email, a provider resource path. None of them grants anything alone.
#
# The only credentials in this system live in Secret Manager, are read by the runtime service
# account, and never reach GitHub.
#
# Run after `just infra-first-deploy`, from anywhere:
#
#   ./infra/scripts/sync-github.sh [stack]
#
set -euo pipefail

STACK="${1:-dev}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"
REPO="${GITHUB_REPOSITORY:-{{ cookiecutter.github_repository }}}"

command -v gh >/dev/null || { echo "gh is not installed"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh is not authenticated: run 'gh auth login'"; exit 1; }

echo "stack = ${STACK}"
echo "repo  = ${REPO}"
echo

read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null
}

# Un-gating CD before every secret the service needs has a pinned version deploys a revision that
# refuses to start — a server with no signing key will not serve — or a migration with no password.
# The stack knows, and says so in `secrets_ready`.
SECRETS_READY="$(read_output secrets_ready || true)"
if [[ "${SECRETS_READY}" != "true" ]]; then
  cat >&2 <<EOF
  NOT READY  stack '${STACK}' reports secrets_ready='${SECRETS_READY:-<missing>}': a secret the
             service or its migration needs has no pinned version yet. Generate, store and pin
             them, apply so the service mounts them, then run this script again:

    just infra-secrets ${STACK}
    just infra-up ${STACK}
EOF
  exit 1
fi
echo "  ok    every required secret has a pinned version"

# There is no placeholder image, so the service exists only after the first deploy has built the
# real one. Un-gating CD before then points a pipeline at a service that is not there.
if [[ -z "$(read_output service_url || true)" ]]; then
  cat >&2 <<EOF
  NO SERVICE  stack '${STACK}' exports no service_url: the service is created by the first deploy,
              which builds the real image from this checkout. Run it, then this script again:

    just infra-first-deploy ${STACK}
EOF
  exit 1
fi
echo "  ok    the service exists"

# Exported and deliberately never synced: `secrets_ready` gates this script, and the Cloud SQL
# connection name is read by `infra/scripts/secrets.sh` and by a human at a terminal; no workflow
# reads either. Listed so the contract test can tell "deliberately not synced" from "forgotten".
# shellcheck disable=SC2034
declare -a NOT_SYNCED=(secrets_ready cloud_sql_instance)

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/{{ cookiecutter.component }}-*.yml exactly; a unit test asserts that it does,
# because the failure otherwise is a deploy job that is skipped forever and never says why.
declare -a MAPPING=(
  "gcp_project:{{ cookiecutter.__component_var }}_GCP_PROJECT"
  "gcp_region:{{ cookiecutter.__component_var }}_GCP_REGION"
  "image_repo:{{ cookiecutter.__component_var }}_IMAGE_REPO"
  "wif_provider:{{ cookiecutter.__component_var }}_WIF_PROVIDER"
  "deploy_sa:{{ cookiecutter.__component_var }}_DEPLOY_SA"
  "apply_sa:{{ cookiecutter.__component_var }}_APPLY_SA"
  "runtime_sa:{{ cookiecutter.__component_var }}_RUNTIME_SA"
  "cloud_run_service:{{ cookiecutter.__component_var }}_CLOUD_RUN_SERVICE"
  "service_url:{{ cookiecutter.__component_var }}_SERVICE_URL"
)

for entry in "${MAPPING[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}")"
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'pulumi up' run for stack '${STACK}'?" >&2
    exit 1
  fi
  gh variable set "${variable}" --repo "${REPO}" --body "${value}"
  echo "  set ${variable} = ${value}"
done

# Outside the loop above, because an empty value here is the normal state rather than a bug: a
# project with no database has no migration job, and cd.yml skips the migrate step on an empty
# variable. Still *set* rather than skipped — a stale value left behind after a database is removed
# would send every deploy at a job that no longer exists.
migrate_job="$(read_output cloud_run_migrate_job || true)"
gh variable set {{ cookiecutter.__component_var }}_CLOUD_RUN_MIGRATE_JOB --repo "${REPO}" --body "${migrate_job}"
echo "  set {{ cookiecutter.__component_var }}_CLOUD_RUN_MIGRATE_JOB = ${migrate_job:-(empty — no migration job)}"

cat <<EOF

Done. {{ cookiecutter.__component_var }}_WIF_PROVIDER is now set, which un-gates
.github/workflows/{{ cookiecutter.component }}-cd.yml and the preview in {{ cookiecutter.component }}-infra.yml, and {{ cookiecutter.__component_var }}_APPLY_SA its
apply. From the next push to main, CD owns every image and {{ cookiecutter.component }}-infra.yml applies every
infrastructure change that merges. The applies so far ran from this machine because the apply
account did not exist yet; one from a laptop now is the exception Pulumi standards MUST-9 forbids.

Before that push, protect the GitHub environment 'infrastructure' in the repository settings:
required reviewers, and deployment branches limited to main. Only a job in that environment may
impersonate the apply account, which can change anything in the project — its rules are what
stands between a merge and a change to it.

Two things this script cannot do, because their values come from outside the stack:

  * {{ cookiecutter.__component_var }}_SENTRY_ORG and {{ cookiecutter.__component_var }}_SENTRY_PROJECT, plus the {{ cookiecutter.__component_var }}_SENTRY_AUTH_TOKEN repository
    *secret*, if you want a release marker on every deploy.
  * A value only a human has — a Sentry DSN — in a container the stack created empty. Add it on
    stdin, so it is never an argument or a file, pin the number it prints, and merge the pin —
    {{ cookiecutter.component }}-infra.yml applies it:

      gcloud secrets versions add <secret-id> --data-file=- --project=$(read_output gcp_project)
      pulumi -C infra/pulumi config set --plaintext sentryDsnVersion <number> --stack ${STACK}

EOF
