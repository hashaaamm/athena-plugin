#!/usr/bin/env bash
# Destroy everything this component created in one environment. Irreversible: the traces in
# ClickHouse, the Postgres database and the bucket go with it.
#
#   ./infra/scripts/teardown.sh [ENV]
#
# The order is not a preference:
#
#   1. deletion_protection = false, merged and APPLIED BY CI. Cloud SQL's protection is a setting on
#      the instance and the cluster's is recorded in state, so a destroy cannot get past either until
#      an apply has turned them off. This script checks the variable file and stops if it has not
#      been done.
#   2. this script, from an operator's machine: `terraform destroy`. A destroy is not an apply CI
#      runs, so this is the one routine laptop operation after setup — it prints the destroy plan,
#      then asks for the project id typed out, which is the confirmation, and there is no flag that
#      skips that.
#   3. the state. The state bucket is not this component's alone — other components may keep their
#      state under other prefixes in it — so this script never deletes it. It prints how to remove
#      this component's prefix, and how to see whether anything else is in the bucket first.
#
# In cluster_mode "existing" the cluster stays. What goes is the namespace this component owns, the
# Helm releases it installed (Langfuse, the ClickHouse operator and, if it installed it,
# cert-manager — check nothing else in the cluster uses them), and the managed resources beside it.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPONENT_DIR="$(cd "${HERE}/../.." && pwd)"
TF="${HERE}/tf.sh"

ENVIRONMENT="${1:-dev}"
VARS="environments/${ENVIRONMENT}.tfvars"
TFVARS="${COMPONENT_DIR}/infra/terraform/${VARS}"
BACKEND="${COMPONENT_DIR}/infra/terraform/environments/${ENVIRONMENT}.backend.hcl"

value() {
  sed -n "s/^[[:space:]]*$2[[:space:]]*=[[:space:]]*\"\{0,1\}\([^\"[:space:]]*\)\"\{0,1\}.*/\1/p" "$1" | head -1
}

PROJECT="$(value "${TFVARS}" project_id)"
BUCKET="$(value "${BACKEND}" bucket)"
PREFIX="$(value "${BACKEND}" prefix)"

if [[ "$(value "${TFVARS}" deletion_protection)" != "false" ]]; then
  cat >&2 <<EOF
deletion_protection is not false in ${VARS}.

Set it to false in a pull request, let the infra workflow apply it on merge, and run this again.
A destroy cannot remove a protected Cloud SQL instance or cluster, and stopping halfway leaves
some of the environment running with part of its state gone.
EOF
  exit 1
fi

if [[ ! -t 0 ]]; then
  echo "Refusing to run with nothing there to confirm. Run it from a terminal." >&2
  exit 1
fi

# In cluster_mode "new" the Kubernetes and Helm providers are configured from the cluster, so once a
# destroy has deleted it, nothing in the configuration can be planned again ("Failed to construct
# REST client"). A teardown that stopped after that point is finished by naming what is left in
# state, which plans without the providers.
#
# (No hash-sign array length in this file: cookiecutter renders it, and Jinja reads that brace
# pair as the start of a comment. Hence the counter.)
targets=()
remaining=0
in_state="$("${TF}" state list)"
if [[ "$(value "${TFVARS}" cluster_mode)" == "new" ]] \
  && ! grep -qxF 'module.langfuse[0].google_container_cluster.this' <<<"${in_state}"; then
  while IFS= read -r address; do
    [[ -z "${address}" || "${address}" == data.* || "${address}" == *.data.* ]] && continue
    targets+=("-target=${address}")
    remaining=$((remaining + 1))
  done <<<"${in_state}"
  if [[ "${remaining}" == 0 ]]; then
    echo "Nothing of '${ENVIRONMENT}' is left in state; there is nothing to destroy."
    exit 0
  fi
  echo "The cluster is already gone; destroying the ${remaining} resources still in state."
fi

# What goes, read before confirming.
"${TF}" plan -destroy -input=false -var-file="${VARS}" ${targets[@]+"${targets[@]}"}

cat <<EOF

About to destroy every resource of '${ENVIRONMENT}' in project ${PROJECT}: the Langfuse release, ClickHouse
and its volumes, Cloud SQL, Memorystore, the bucket of events and media, DNS, the CI identities
and — in cluster_mode "new" — the cluster and its network. There is no undo.

Type the project id to confirm.
EOF
read -r -p "  ${PROJECT}: " reply
if [[ "${reply}" != "${PROJECT}" ]]; then
  echo "    did not match — nothing was destroyed"
  exit 1
fi

# Three things go with something else rather than on their own, and leave state first.
#
# Cloud SQL's database and role go with the instance, not before it. Dropping the database first
# fails while the Langfuse pods are still connected ("database langfuse is being accessed by other
# users"), and dropping the role while it owns the schema fails too ("role langfuse cannot be dropped
# because some objects depend on it"), so the destroy stops half way. The official module sets no
# deletion_policy on either, so they leave state here, after the confirmation; the instance's
# deletion removes both. existing_cluster.tf sets deletion_policy = "ABANDON" on its own pair, which
# has the same effect; they are listed for state written before it did.
#
# The module's private services connection goes with its network. Deleting it right after Cloud
# SQL fails for a while ("Producer services (e.g. CloudSQL, Cloud Memstore, etc.) are still using
# this connection"); deleting the network removes the peering. The provider's own answer is
# deletion_policy = "ABANDON", which the module does not set either. In "existing" mode the
# connection is the cluster network's, not this component's, and is never in state.
for address in \
  'module.langfuse[0].google_sql_database.langfuse' \
  'module.langfuse[0].google_sql_user.langfuse' \
  'module.langfuse[0].google_service_networking_connection.private_service_connection' \
  'google_sql_database.existing[0]' \
  'google_sql_user.existing[0]'; do
  if [[ -n "$("${TF}" state list "${address}" 2>/dev/null || true)" ]]; then
    "${TF}" state rm "${address}"
  fi
done

"${TF}" destroy -input=false -auto-approve -var-file="${VARS}" ${targets[@]+"${targets[@]}"}

cat <<EOF

Destroyed. What is left, deliberately:

  * the state: gs://${BUCKET}/${PREFIX}/. Look at what else the bucket holds before touching it —
      gcloud storage ls gs://${BUCKET}/
    and remove only this component's prefix once you no longer need its history:
      gcloud storage rm --recursive gs://${BUCKET}/${PREFIX}/
    The bucket and its KMS key stay; other components may use them.
  * the repository variables and the workflow. Remove the component's variables (gh variable
    delete), its folder and .github/workflows/$(basename "${COMPONENT_DIR}")-*.yml in one pull request.
  * the NS records at your registrar, which now point at a zone that no longer exists.
EOF
