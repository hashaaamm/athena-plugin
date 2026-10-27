#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates
# .github/workflows/{{ cookiecutter.component }}-cd.yml. Every variable is prefixed {{ cookiecutter.__component_var }}_, the component's name, so two
# components in one repository never write each other's.
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

command -v pulumi >/dev/null || { echo "pulumi is not installed"; exit 1; }
command -v gh >/dev/null || { echo "gh is not installed"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh is not authenticated: run 'gh auth login'"; exit 1; }

echo "stack = ${STACK}"
echo "repo  = ${REPO}"
echo

read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null
}

# Setting {{ cookiecutter.__component_var }}_WIF_PROVIDER un-gates CD, and CD deploys an image that refuses to start without the
# secrets it reads. The stack mounts each one only once its version is in stack configuration, and
# reports whether every one is as secrets_ready — so this stops here, before setting anything,
# rather than turning the first merge into a revision that never becomes ready.
READY="$(read_output secrets_ready || true)"
if [[ "${READY}" != "true" ]]; then
  cat >&2 <<EOF
  NOT READY  secrets_ready is '${READY:-unset}' for stack '${STACK}': the service would deploy
             without a secret it cannot start without.

  Generate them, apply the stack so it mounts the pinned versions, then run this script again:

    just infra-secrets ${STACK}
    just infra-up ${STACK}
EOF
  exit 1
fi
echo "  ok    every required secret has a pinned version"

# CD updates the service's image; it cannot create the service. Before the first deploy there is
# no service — `just infra-up` creates everything else — so un-gating CD now would send its first
# run at a service that does not exist.
SERVICE_URL="$(read_output service_url || true)"
if [[ -z "${SERVICE_URL}" ]]; then
  cat >&2 <<EOF
  NO SERVICE  service_url is empty for stack '${STACK}': the service is created by the first deploy,
              which builds the real image on this machine and creates the service on it.

  Run it, then run this script again:

    just infra-first-deploy ${STACK}
EOF
  exit 1
fi
echo "  ok    the service exists: ${SERVICE_URL}"

# stack output -> repository variable, every name prefixed with the component's, so another
# component's sync never overwrites this one's. The right-hand column must match the `vars.` names in
# .github/workflows/{{ cookiecutter.component }}-cd.yml and {{ cookiecutter.component }}-infra.yml exactly.
# infra/pulumi/contract_test.go asserts that it does, because the failure otherwise is a deploy
# job that is skipped forever and never says why.
#
# Every one of these exists once the first deploy has run. An empty value is a stack that has not
# been applied, so it stops the script rather than writing a blank into a pipeline.
REQUIRED=(
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

# Empty is the normal state for each of these: a project with no database has no migration job.
# An empty output DELETES the variable rather than setting it. Every workflow step that uses one is gated on it being
# non-empty, so a deleted variable is a step that quietly does not run — while a stale value left
# behind after the feature is switched off would send every deploy at a resource that no longer
# exists.
OPTIONAL=(
  "cloud_run_migrate_job:{{ cookiecutter.__component_var }}_CLOUD_RUN_MIGRATE_JOB"
)

for entry in "${REQUIRED[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'just infra-first-deploy ${STACK}' run?" >&2
    exit 1
  fi
  gh variable set "${variable}" --repo "${REPO}" --body "${value}"
  echo "  set   ${variable} = ${value}"
done

for entry in "${OPTIONAL[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -n "${value}" ]]; then
    gh variable set "${variable}" --repo "${REPO}" --body "${value}"
    echo "  set   ${variable} = ${value}"
  elif gh variable get "${variable}" --repo "${REPO}" >/dev/null 2>&1; then
    gh variable delete "${variable}" --repo "${REPO}"
    echo "  unset ${variable} (the stack has none)"
  else
    echo "  skip  ${variable} (the stack has none)"
  fi
done

cat <<EOF

Done. {{ cookiecutter.__component_var }}_WIF_PROVIDER is now set, which un-gates .github/workflows/{{ cookiecutter.component }}-cd.yml and the
preview in {{ cookiecutter.component }}-infra.yml, and {{ cookiecutter.__component_var }}_APPLY_SA, which un-gates its apply. From the next push
to main, CD builds and deploys every image, and every infrastructure change that merges is applied
by {{ cookiecutter.component }}-infra.yml. The applies so far ran from this machine because the apply account did not
exist yet; one from a laptop now is the exception Pulumi standards MUST-9 forbids.

Before that push, protect the GitHub environment 'infrastructure' in the repository settings:
required reviewers, and deployment branches limited to main. Only a job in that environment may
impersonate the apply account, which can change anything in the project — its rules are what
stands between a merge and a change to it.

Two things this script cannot do, because their values come from outside the stack:

  * {{ cookiecutter.__component_var }}_SENTRY_ORG and {{ cookiecutter.__component_var }}_SENTRY_PROJECT, plus the {{ cookiecutter.__component_var }}_SENTRY_AUTH_TOKEN repository
    *secret*, if you want a release marker on every deploy.
  * The value of a secret that originates elsewhere, such as the Sentry DSN. Add it on stdin, so it
    is in no file and no shell history:

      gcloud secrets versions add <secret-id> --data-file=- --project=$(read_output gcp_project)

    then put the version number it prints into infra/pulumi/Pulumi.${STACK}.yaml and merge that
    change: {{ cookiecutter.component }}-infra.yml applies it.

EOF
