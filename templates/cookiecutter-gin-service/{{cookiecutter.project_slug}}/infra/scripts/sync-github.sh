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

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/cd.yml — and in frontend-cd.yml, once a frontend is grafted in — exactly.
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

# Empty is the normal state for each of these: a project with no database has no migration job,
# and a stack with frontendEnabled off has no frontend service. An empty output DELETES the
# variable rather than setting it. Every workflow step that uses one is gated on it being
# non-empty, so a deleted variable is a step that quietly does not run — while a stale value left
# behind after the feature is switched off would send every deploy at a resource that no longer
# exists.
OPTIONAL=(
  "cloud_run_migrate_job:CLOUD_RUN_MIGRATE_JOB"
  "cloud_run_frontend_service:CLOUD_RUN_FRONTEND_SERVICE"
  "frontend_url:FRONTEND_URL"
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
  * The value inside any Secret Manager container the stack created empty. Add one with:

      gcloud secrets versions add <secret-id> --data-file=- --project=$(read_output gcp_project)

EOF

if [[ -n "$(read_output cloud_run_frontend_service || true)" ]]; then
  cat <<EOF
CLOUD_RUN_FRONTEND_SERVICE and SERVICE_URL are set, so a frontend-cd.yml in this repository can
deploy. It compiles SERVICE_URL into the bundle as VITE_API_URL, which is why this script runs
before the first frontend build and not after: a bundle built without it would ship pointing at
http://localhost:8000, load fine, and fail every request.

Re-run this script whenever the backend's URL changes, then re-run the Frontend CD workflow — the
origin is baked into the image, so an old image keeps calling the old address. If the API answers
on a domain you own instead, set FRONTEND_API_URL by hand; it wins over SERVICE_URL.

EOF
fi
