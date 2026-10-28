#!/usr/bin/env bash
# Prepare the project and the Terraform state bucket. Run once per environment, by a human, before
# the first `just init`.
#
# Deliberately not Terraform: a configuration cannot create the bucket that holds its own state.
# A short script that checks before it acts is the honest answer to that bootstrap hole.
#
#   ./infra/bootstrap/state-bucket.sh [ENV] [--bucket NAME] [--harden] [--kms-key-ring NAME] [--kms-key NAME]
#
#   ENV            the environment whose files to read: environments/<ENV>.tfvars and
#                  environments/<ENV>.backend.hcl. Default: dev.
#   --bucket NAME  use this bucket instead of the one the backend file names, and write the name
#                  into both files. Point it at an existing bucket to share it with other
#                  components: each keeps its state under its own prefix.
#   --harden       for an existing bucket: turn on what the checks below find missing — versioning,
#                  public access prevention, uniform access and a CMEK default key. Without it, an
#                  existing bucket is only checked, never changed.
#   --kms-key-ring NAME, --kms-key NAME
#                  the key ring and key the state is encrypted with, created if missing. Default:
#                  terraform-state and state. Choose them before the first run: Cloud KMS never
#                  deletes a key ring or a key, so a name, once used, stays taken in the project.
#
# What it does, every step checking before it creates, so it is safe to re-run:
#
#   1. enables the APIs the official module and this configuration need;
#   2. a NEW bucket: creates it in the region with uniform bucket-level access, public access
#      prevention and versioning, creates a Cloud KMS key ring and key beside it, lets Cloud
#      Storage's service agent use the key, and makes the key the bucket's default — so the state,
#      which holds every secret Langfuse runs with, is encrypted with a key this project controls.
#      Project viewers lose the object read a new bucket's policy gives them by default;
#   3. an EXISTING bucket: checks versioning and public access prevention (fails without them),
#      and uniform access and a CMEK default key (warns without them), and changes nothing unless
#      --harden is given;
#   4. prints who can read the bucket. Everyone listed can read every Langfuse secret.
#
# This is the first command in the setup that creates something outside the repository. A bucket,
# a key and the APIs cost pennies a month; nothing that runs Langfuse is created here.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TF_DIR="${HERE}/../terraform"

ENVIRONMENT="dev"
NEW_BUCKET=""
HARDEN=0
KEYRING="terraform-state"
KEY="state"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --bucket) NEW_BUCKET="${2:?--bucket needs a name}"; shift 2 ;;
    --harden) HARDEN=1; shift ;;
    --kms-key-ring) KEYRING="${2:?--kms-key-ring needs a name}"; shift 2 ;;
    --kms-key) KEY="${2:?--kms-key needs a name}"; shift 2 ;;
    -h|--help) sed -n '2,37p' "${BASH_SOURCE[0]}"; exit 0 ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) ENVIRONMENT="$1"; shift ;;
  esac
done

TFVARS="${TF_DIR}/environments/${ENVIRONMENT}.tfvars"
BACKEND="${TF_DIR}/environments/${ENVIRONMENT}.backend.hcl"
[[ -f "${TFVARS}" ]] || { echo "no ${TFVARS}" >&2; exit 1; }
[[ -f "${BACKEND}" ]] || { echo "no ${BACKEND}" >&2; exit 1; }

# Read `key = "value"` from an HCL file. Enough for the flat files this component writes.
hcl_value() {
  sed -n "s/^[[:space:]]*$2[[:space:]]*=[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$1" | head -1
}

# Rewrite `key = "value"` in place, keeping the file's alignment. Portable across GNU and BSD sed.
hcl_set() {
  local file="$1" key="$2" value="$3" tmp
  tmp="$(mktemp)"
  sed "s|^\([[:space:]]*$2[[:space:]]*=[[:space:]]*\)\"[^\"]*\"|\1\"${value}\"|" "${file}" >"${tmp}"
  cat "${tmp}" >"${file}"
  rm -f "${tmp}"
  grep -q "^[[:space:]]*${key}[[:space:]]*=[[:space:]]*\"${value}\"" "${file}" || {
    echo "could not set ${key} in ${file}" >&2
    exit 1
  }
}

PROJECT="${GCP_PROJECT:-$(hcl_value "${TFVARS}" project_id)}"
REGION="${GCP_REGION:-$(hcl_value "${TFVARS}" region)}"
[[ -n "${PROJECT}" && -n "${REGION}" ]] || { echo "project_id and region must be set in ${TFVARS}" >&2; exit 1; }

if [[ -n "${NEW_BUCKET}" ]]; then
  hcl_set "${BACKEND}" bucket "${NEW_BUCKET}"
  hcl_set "${TFVARS}" state_bucket "${NEW_BUCKET}"
  echo "wrote bucket ${NEW_BUCKET} into $(basename "${BACKEND}") and $(basename "${TFVARS}") — commit both"
fi
BUCKET_NAME="$(hcl_value "${BACKEND}" bucket)"
PREFIX="$(hcl_value "${BACKEND}" prefix)"
[[ -n "${BUCKET_NAME}" ]] || { echo "no bucket in ${BACKEND}" >&2; exit 1; }
if [[ "$(hcl_value "${TFVARS}" state_bucket)" != "${BUCKET_NAME}" ]]; then
  echo "state_bucket in $(basename "${TFVARS}") does not match bucket in $(basename "${BACKEND}");" \
    "run this with --bucket ${BUCKET_NAME} to align them" >&2
  exit 1
fi
BUCKET="gs://${BUCKET_NAME}"

echo "project = ${PROJECT}"
echo "region  = ${REGION}"
echo "bucket  = ${BUCKET} (state under ${PREFIX}/)"
echo

echo "==> Enabling APIs"
# The first group is what this script and Terraform's own backend need. The rest are the official
# module's list (its README, "Usage", step 1) plus Cloud SQL Admin, which its provider calls, and
# the three Workload Identity Federation needs. Enabling them here rather than in Terraform keeps
# `serviceusage` admin off the apply identity, and the first plan from failing on a disabled API.
gcloud services enable \
  storage.googleapis.com \
  cloudkms.googleapis.com \
  cloudresourcemanager.googleapis.com \
  serviceusage.googleapis.com \
  compute.googleapis.com \
  container.googleapis.com \
  containerfilesystem.googleapis.com \
  certificatemanager.googleapis.com \
  dns.googleapis.com \
  redis.googleapis.com \
  networkconnectivity.googleapis.com \
  servicenetworking.googleapis.com \
  sqladmin.googleapis.com \
  iam.googleapis.com \
  iamcredentials.googleapis.com \
  sts.googleapis.com \
  --project="${PROJECT}"

# Create the key ring and key in LOCATION if missing, let Cloud Storage's service agent use it, and
# print the key's resource name.
ensure_key() {
  local location="$1" key_name
  if ! gcloud kms keyrings describe "${KEYRING}" --location="${location}" --project="${PROJECT}" >/dev/null 2>&1; then
    gcloud kms keyrings create "${KEYRING}" --location="${location}" --project="${PROJECT}" >&2
  fi
  if ! gcloud kms keys describe "${KEY}" --keyring="${KEYRING}" --location="${location}" --project="${PROJECT}" >/dev/null 2>&1; then
    gcloud kms keys create "${KEY}" --keyring="${KEYRING}" --location="${location}" \
      --project="${PROJECT}" --purpose=encryption >&2
  fi
  key_name="projects/${PROJECT}/locations/${location}/keyRings/${KEYRING}/cryptoKeys/${KEY}"
  # The service agent encrypts and decrypts objects on the bucket's behalf; callers never touch
  # the key. Idempotent: it adds the binding only if missing.
  gcloud storage service-agent --project="${PROJECT}" --authorize-cmek="${key_name}" >&2
  echo "${key_name}"
}

echo "==> State bucket"
if ! gcloud storage buckets describe "${BUCKET}" --project="${PROJECT}" >/dev/null 2>&1; then
  key_name="$(ensure_key "${REGION}")"
  gcloud storage buckets create "${BUCKET}" \
    --project="${PROJECT}" \
    --location="${REGION}" \
    --uniform-bucket-level-access \
    --public-access-prevention \
    --default-encryption-key="${key_name}"
  gcloud storage buckets update "${BUCKET}" --versioning
  # A new bucket's policy gives the project's basic roles access through convenience values, and
  # `projectViewer` gets roles/storage.legacyObjectReader: every project Viewer could read state,
  # and so every Langfuse secret. Owners and editors keep theirs; the plan identity has its own
  # grant (identities.tf).
  gcloud storage buckets remove-iam-policy-binding "${BUCKET}" \
    --member="projectViewer:${PROJECT}" --role=roles/storage.legacyObjectReader >/dev/null
  echo "    created, versioned, encrypted with ${key_name}; project viewers cannot read objects"
else
  echo "    exists — checking it"
  describe() { gcloud storage buckets describe "${BUCKET}" --format="value($1)"; }
  failures=0

  versioning="$(describe versioning_enabled)"
  pap="$(describe public_access_prevention)"
  ubla="$(describe uniform_bucket_level_access)"
  cmek="$(describe default_kms_key)"
  location="$(describe location | tr '[:upper:]' '[:lower:]')"

  if [[ "${versioning}" != "True" ]]; then
    if [[ "${HARDEN}" == 1 ]]; then
      gcloud storage buckets update "${BUCKET}" --versioning
      echo "    versioning: turned on (--harden)"
    else
      echo "    FAIL  versioning is off. A versioned bucket is the only way back from a corrupted state." >&2
      failures=1
    fi
  else
    echo "    ok    versioning"
  fi

  if [[ "${pap}" != "enforced" ]]; then
    if [[ "${HARDEN}" == 1 ]]; then
      gcloud storage buckets update "${BUCKET}" --public-access-prevention
      echo "    public access prevention: enforced (--harden)"
    else
      echo "    FAIL  public access prevention is '${pap:-unset}', not 'enforced'. State holds every Langfuse secret." >&2
      failures=1
    fi
  else
    echo "    ok    public access prevention"
  fi

  if [[ "${ubla}" != "True" ]]; then
    if [[ "${HARDEN}" == 1 ]]; then
      gcloud storage buckets update "${BUCKET}" --uniform-bucket-level-access
      echo "    uniform bucket-level access: on (--harden)"
    else
      echo "    WARN  uniform bucket-level access is off: per-object ACLs can grant read on state by accident." >&2
    fi
  else
    echo "    ok    uniform bucket-level access"
  fi

  if [[ -z "${cmek}" ]]; then
    if [[ "${HARDEN}" == 1 ]]; then
      key_name="$(ensure_key "${location}")"
      gcloud storage buckets update "${BUCKET}" --default-encryption-key="${key_name}"
      echo "    default encryption key: ${key_name} (--harden; objects written from now on)"
    else
      echo "    WARN  no CMEK default key: state is encrypted with a Google-managed key. --harden adds one." >&2
    fi
  else
    echo "    ok    CMEK default key ${cmek}"
  fi

  if [[ "${failures}" != 0 ]]; then
    cat >&2 <<EOF

Refusing to continue with ${BUCKET} as it is. Fix it yourself, or let this script do it:

  ./infra/bootstrap/state-bucket.sh ${ENVIRONMENT} --harden

--harden changes a bucket other components may share. Read what it does at the top of this file.
EOF
    exit 1
  fi
fi

echo
echo "==> Who can read state (bucket policy; project-level storage roles also apply)"
gcloud storage buckets get-iam-policy "${BUCKET}" --flatten="bindings[].members" \
  --format="table(bindings.role,bindings.members)" || true

cat <<EOF

Bootstrap complete. Everyone above, and everyone holding a project-level storage role, can read
this component's state — and so every password and key Langfuse runs with. Keep that list short.

Next, from the component's folder:

  just init ${ENVIRONMENT}
  just plan ${ENVIRONMENT}
  just up ${ENVIRONMENT}

EOF
