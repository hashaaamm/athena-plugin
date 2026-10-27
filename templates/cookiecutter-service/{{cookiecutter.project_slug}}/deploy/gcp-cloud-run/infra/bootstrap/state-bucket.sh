#!/usr/bin/env bash
# Create the Pulumi state backend. Run once, by a human, before the first `pulumi up`.
#
# This is deliberately NOT Pulumi. A stack cannot create the bucket that holds its own state — the
# first `pulumi up` would need somewhere to write before that somewhere exists. Every IaC tool has
# this bootstrap hole; the honest response is a short, idempotent script rather than a stack that
# pretends to manage its own backend.
#
# Safe to re-run: every step checks before it creates.
#
#   ./infra/bootstrap/state-bucket.sh
#
# This is the first command in the whole setup that creates something outside the repository. It is
# cheap — a bucket and a KMS key are pennies a month — but it is the line where "reading" stops.
set -euo pipefail

PROJECT="${GCP_PROJECT:-{{ cookiecutter.gcp_project_id }}}"
REGION="${GCP_REGION:-{{ cookiecutter.gcp_region }}}"
BUCKET="gs://${PROJECT}-pulumi-state"
KEYRING="pulumi"
KEY="stack"

echo "project = ${PROJECT}"
echo "region  = ${REGION}"
echo "bucket  = ${BUCKET}"
echo

gcloud config set project "${PROJECT}" >/dev/null

echo "==> Enabling the APIs that must exist before Pulumi runs"
# Two groups. The first is what this script itself needs: a bucket and a KMS key.
#
# The second is the chicken-and-egg the stack cannot solve for itself. Enabling an API is a
# `serviceusage` call authorised through `cloudresourcemanager`, and the GCP provider resolves the
# project's default region and zone through `compute` before planning anything. All three are
# needed before infra/pulumi/components/apis.py can enable the rest.
gcloud services enable \
  storage.googleapis.com \
  cloudkms.googleapis.com \
  cloudresourcemanager.googleapis.com \
  serviceusage.googleapis.com \
  compute.googleapis.com \
  --project="${PROJECT}"

echo "==> State bucket"
if gcloud storage buckets describe "${BUCKET}" --project="${PROJECT}" >/dev/null 2>&1; then
  echo "    exists"
else
  # Uniform bucket-level access: per-object ACLs on a state bucket are a way to grant access by
  # accident. Public access prevention: state lists every resource name in the project.
  gcloud storage buckets create "${BUCKET}" \
    --project="${PROJECT}" \
    --location="${REGION}" \
    --uniform-bucket-level-access \
    --public-access-prevention
  echo "    created"
fi
# Versioning: state is the only record of what exists, and `pulumi up` overwrites it.
gcloud storage buckets update "${BUCKET}" --versioning

echo "==> KMS key for stack secrets"
if ! gcloud kms keyrings describe "${KEYRING}" --location="${REGION}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud kms keyrings create "${KEYRING}" --location="${REGION}" --project="${PROJECT}"
fi
if ! gcloud kms keys describe "${KEY}" --keyring="${KEYRING}" --location="${REGION}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud kms keys create "${KEY}" \
    --keyring="${KEYRING}" \
    --location="${REGION}" \
    --project="${PROJECT}" \
    --purpose=encryption
fi

SECRETS_PROVIDER="gcpkms://projects/${PROJECT}/locations/${REGION}/keyRings/${KEYRING}/cryptoKeys/${KEY}"

cat <<EOF

Bootstrap complete. Next, from infra/pulumi:

  pulumi login ${BUCKET}
  pulumi stack init dev --secrets-provider="${SECRETS_PROVIDER}"
  uv lock
  uv run pytest -q
  uv run pulumi preview --stack dev --refresh --diff

The KMS key encrypts stack *configuration* at rest. It is not where application secrets live —
those are Secret Manager containers the stack creates and a human populates, because secret values
must never enter infrastructure code or state.

EOF
