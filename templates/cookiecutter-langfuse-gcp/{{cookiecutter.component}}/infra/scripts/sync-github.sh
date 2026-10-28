#!/usr/bin/env bash
# Copy the configuration's CI outputs into GitHub repository variables — the handover that turns
# on .github/workflows/<component>-infra.yml's plan and apply jobs.
#
#   ./infra/scripts/sync-github.sh [ENV]
#
# Every variable is prefixed with the component's name in capitals: the repository may hold other
# components, and each reads only its own.
#
# It sets no GitHub secret, because there is none to set. Workload Identity Federation means CI
# holds no credential: it exchanges GitHub's own OIDC token for a short-lived Google one, and every
# value below is a public identifier.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPONENT_DIR="$(cd "${HERE}/../.." && pwd)"
COMPONENT="$(basename "${COMPONENT_DIR}")"
COMPONENT_VAR="$(echo "${COMPONENT}" | tr '[:lower:]-' '[:upper:]_')"
TF="${HERE}/tf.sh"

ENVIRONMENT="${1:-dev}"
TFVARS="${COMPONENT_DIR}/infra/terraform/environments/${ENVIRONMENT}.tfvars"
REPO="${GITHUB_REPOSITORY:-$(sed -n 's/^[[:space:]]*github_repository[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "${TFVARS}" | head -1)}"

command -v gh >/dev/null || { echo "gh is not installed"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh is not authenticated: run 'gh auth login'"; exit 1; }

echo "environment = ${ENVIRONMENT}"
echo "repo        = ${REPO}"
echo

# output -> repository variable. The right-hand names must match the `vars.` names in
# .github/workflows/<component>-infra.yml exactly, or the job they gate is skipped forever.
declare -a MAPPING=(
  "wif_provider:${COMPONENT_VAR}_WIF_PROVIDER"
  "plan_service_account:${COMPONENT_VAR}_PLAN_SA"
  "apply_service_account:${COMPONENT_VAR}_APPLY_SA"
)

for entry in "${MAPPING[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$("${TF}" output -raw "${output}" 2>/dev/null || true)"
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'just up ${ENVIRONMENT}' completed?" >&2
    exit 1
  fi
  gh variable set "${variable}" --repo "${REPO}" --body "${value}"
  echo "  set ${variable} = ${value}"
done

cat <<EOF

Done. ${COMPONENT}-infra.yml now plans every pull request that touches ${COMPONENT}/ and applies
every merge to main — as ${COMPONENT_VAR}_APPLY_SA, which only a job in the \`infrastructure\`
environment can become.

Do this now, in the repository's Settings > Environments > infrastructure (GitHub creates the
environment on the first run that names it; create it by hand to set it up first):

  - Required reviewers: the people who may approve an infrastructure change.
  - Deployment branches and tags: main only.

Without them an apply runs on merge with nobody approving it, as an identity that can change
anything in ${ENVIRONMENT}'s project.
EOF
