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

# Exported and deliberately never synced: `secrets_ready` gates this script, and the Cloud SQL
# connection name is read by `infra/scripts/secrets.sh` and by a human at a terminal; no workflow
# reads either. Listed so the contract test can tell "deliberately not synced" from "forgotten".
# shellcheck disable=SC2034
declare -a NOT_SYNCED=(secrets_ready cloud_sql_instance)

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/cd.yml exactly; a unit test asserts that it does, because the failure otherwise
# is a deploy job that is skipped forever and never says why.
declare -a MAPPING=(
  "gcp_project:GCP_PROJECT"
  "gcp_region:GCP_REGION"
  "image_repo:IMAGE_REPO"
  "wif_provider:WIF_PROVIDER"
  "deploy_sa:DEPLOY_SA"
  "runtime_sa:RUNTIME_SA"
  "cloud_run_service:CLOUD_RUN_SERVICE"
  "service_url:SERVICE_URL"
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
gh variable set CLOUD_RUN_MIGRATE_JOB --repo "${REPO}" --body "${migrate_job}"
echo "  set CLOUD_RUN_MIGRATE_JOB = ${migrate_job:-(empty — no migration job)}"

cat <<EOF

Done. WIF_PROVIDER is now set, which un-gates .github/workflows/cd.yml.

Two things this script cannot do, because their values come from outside the stack:

  * SENTRY_ORG and SENTRY_PROJECT, plus the SENTRY_AUTH_TOKEN repository *secret*, if you want a
    release marker on every deploy.
  * A value only a human has — a Sentry DSN — in a container the stack created empty. Add it on
    stdin, so it is never an argument or a file, pin the number it prints, and apply:

      gcloud secrets versions add <secret-id> --data-file=- --project=$(read_output gcp_project)
      # put the printed number in infra/pulumi/Pulumi.${STACK}.yaml (e.g. sentryDsnVersion)
      just infra-up ${STACK}

EOF
