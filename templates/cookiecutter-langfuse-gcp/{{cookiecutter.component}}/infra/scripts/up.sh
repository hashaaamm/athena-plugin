#!/usr/bin/env bash
# The first apply of an environment, from an operator's machine. Once.
#
#   ./infra/scripts/up.sh [ENV] [--break-glass]
#
# Every apply after this one runs from CI, as the apply identity this apply creates, when a change
# merges to main. This script refuses once `just sync-github` has handed the environment to CI —
# the repository variable <COMPONENT>_APPLY_SA is the handover — unless --break-glass is given, and
# a break-glass apply is an exception a person decides and records, followed by a CI plan that
# shows no changes.
#
# In cluster_mode "new" the first apply is two, as the official module's README requires: the
# Kubernetes and Helm providers are configured from the cluster the module creates, so a targeted
# apply creates the cluster (and the DNS zone or the reserved IP) first, and the full apply follows.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPONENT_DIR="$(cd "${HERE}/../.." && pwd)"
COMPONENT="$(basename "${COMPONENT_DIR}")"
COMPONENT_VAR="$(echo "${COMPONENT}" | tr '[:lower:]-' '[:upper:]_')"
TF="${HERE}/tf.sh"

ENVIRONMENT="dev"
BREAK_GLASS=0
for arg in "$@"; do
  case "${arg}" in
    --break-glass) BREAK_GLASS=1 ;;
    -*) echo "unknown option ${arg}" >&2; exit 2 ;;
    *) ENVIRONMENT="${arg}" ;;
  esac
done

VARS="environments/${ENVIRONMENT}.tfvars"
TFVARS="${COMPONENT_DIR}/infra/terraform/${VARS}"
[[ -f "${TFVARS}" ]] || { echo "no ${TFVARS}" >&2; exit 1; }

hcl_value() {
  sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\{0,1\}\([^\"]*\)\"\{0,1\}.*/\1/p" "${TFVARS}" | head -1
}

if [[ ! -t 0 ]]; then
  echo "Run this from a terminal: it applies, and Terraform asks before it does." >&2
  exit 1
fi

if command -v gh >/dev/null 2>&1; then
  REPO="$(hcl_value github_repository)"
  if gh variable get "${COMPONENT_VAR}_APPLY_SA" --repo "${REPO}" >/dev/null 2>&1; then
    if [[ "${BREAK_GLASS}" != 1 ]]; then
      cat >&2 <<EOF
${COMPONENT_VAR}_APPLY_SA is set in ${REPO}: this environment is applied by
.github/workflows/${COMPONENT}-infra.yml now, not from a laptop. Open a pull request.

A break-glass apply is ./infra/scripts/up.sh ${ENVIRONMENT} --break-glass — record why, and follow
it with a CI plan that shows no changes.
EOF
      exit 1
    fi
    echo "BREAK GLASS: applying from this machine although CI owns this environment." >&2
  fi
else
  echo "gh is not installed, so this cannot check whether CI already owns ${ENVIRONMENT}." >&2
fi

CLUSTER_MODE="$(hcl_value cluster_mode)"
CREATE_DNS_ZONE="$(hcl_value create_dns_zone)"

if [[ "${CLUSTER_MODE}" == "new" ]]; then
  targets=(-target='module.langfuse[0].google_container_cluster.this')
  if [[ "${CREATE_DNS_ZONE}" == "false" ]]; then
    targets+=(-target='module.langfuse[0].google_compute_global_address.ingress')
  else
    targets+=(-target='module.langfuse[0].google_dns_managed_zone.this')
  fi
  echo "==> Stage 1 of 2: the cluster and the DNS zone or address (module README, step 3)"
  "${TF}" apply -var-file="${VARS}" "${targets[@]}"
fi

echo "==> The full apply"
"${TF}" apply -var-file="${VARS}"

echo
"${TF}" output

cat <<EOF

The environment exists. Before anyone can open Langfuse:

  1. DNS. With create_dns_zone = true, add NS records for the hostname at your registrar (or the
     parent zone) pointing at dns_name_servers above. With false, add an A record pointing at
     ingress_ip. The managed certificate is issued only once the name resolves, which takes
     15-60 minutes after that: gcloud compute ssl-certificates list
  2. just sync-github ${ENVIRONMENT}, then give the repository's \`infrastructure\` environment
     required reviewers and limit it to main. From then on CI applies every change.
  3. Sign up at langfuse_url, then set allow_signup = false in ${VARS} in a pull request: the
     first change CI applies.
EOF
