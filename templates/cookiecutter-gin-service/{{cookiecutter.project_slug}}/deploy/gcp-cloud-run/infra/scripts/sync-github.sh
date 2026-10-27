#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates cd.yml.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. Workload
# Identity Federation means CI holds no credential at all — it exchanges GitHub's own OIDC token
# for a short-lived Google one, and every value below is a public identifier: a project id, a
# region, a service-account email, a provider resource path. None of them grants anything alone.
#
# The only credentials in this system live in Secret Manager, are read by the runtime service
# account, and never reach GitHub.
#
# Run after `pulumi up`, from anywhere:
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

# Setting WIF_PROVIDER un-gates CD, and CD deploys an image that refuses to start without the
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

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/cd.yml exactly.
# infra/pulumi/contract_test.go asserts that it does, because the failure otherwise is a deploy
# job that is skipped forever and never says why.
#
# Every one of these exists the moment `pulumi up` has run. An empty value is a stack that has not
# been applied, so it stops the script rather than writing a blank into a pipeline.
REQUIRED=(
  "gcp_project:GCP_PROJECT"
  "gcp_region:GCP_REGION"
  "image_repo:IMAGE_REPO"
  "wif_provider:WIF_PROVIDER"
  "deploy_sa:DEPLOY_SA"
  "runtime_sa:RUNTIME_SA"
  "cloud_run_service:CLOUD_RUN_SERVICE"
  "service_url:SERVICE_URL"
)

# Empty is the normal state for each of these: a project with no database has no migration job.
# An empty output DELETES the variable rather than setting it. Every workflow step that uses one is gated on it being
# non-empty, so a deleted variable is a step that quietly does not run — while a stale value left
# behind after the feature is switched off would send every deploy at a resource that no longer
# exists.
OPTIONAL=(
  "cloud_run_migrate_job:CLOUD_RUN_MIGRATE_JOB"
)

for entry in "${REQUIRED[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'pulumi up' run for stack '${STACK}'?" >&2
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

Done. WIF_PROVIDER is now set, which un-gates .github/workflows/cd.yml.

Two things this script cannot do, because their values come from outside the stack:

  * SENTRY_ORG and SENTRY_PROJECT, plus the SENTRY_AUTH_TOKEN repository *secret*, if you want a
    release marker on every deploy.
  * The value of a secret that originates elsewhere, such as the Sentry DSN. Add it on stdin, so it
    is in no file and no shell history:

      gcloud secrets versions add <secret-id> --data-file=- --project=$(read_output gcp_project)

    then put the version number it prints into infra/pulumi/Pulumi.${STACK}.yaml and run
    \`just infra-up ${STACK}\`.

EOF
