#!/usr/bin/env bash
# Remove what `pulumi destroy` cannot: the state bucket and the KMS key that `state-bucket.sh`
# created. Run once, by a human, AFTER every stack in this backend has been destroyed.
#
#   ./infra/bootstrap/teardown.sh
#
# `pulumi destroy` removes every resource a stack created, and nothing else. It does not remove the
# bucket its own state lives in, or the KMS key that encrypts the secrets in that state, because
# the stack never created them — `state-bucket.sh` did, with plain gcloud, before the stack
# existed. No Pulumi command will ever take them away. This script is the other end of that one.
#
# THE ORDER IS NOT A PREFERENCE. The bucket holds the state that `pulumi destroy` reads. Delete it
# first and every resource the stack created still exists, still bills, and the only record of what
# those resources are has just been deleted — the console and `gcloud ... list` are all you have
# left. So this script refuses to run until every stack in the backend is empty, and it checks
# rather than asking you.
#
# It then asks you to type the bucket name, and it refuses to run when nothing can answer, the way
# `pulumi up` refuses a non-interactive apply. There is deliberately no `--yes` and no `--force`:
# this is irreversible, it happens at most once per project, and nothing about it needs to run from
# a pipeline.
#
# Safe to re-run: every step checks before it acts, which matters here because the KMS half cannot
# finish in one sitting. See the block this prints at the end.
set -euo pipefail

PROJECT="${GCP_PROJECT:-{{ cookiecutter.gcp_project_id }}}"
REGION="${GCP_REGION:-{{ cookiecutter.gcp_region }}}"
BUCKET="gs://${PROJECT}-pulumi-state"
KEYRING="pulumi"
KEY="stack"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"

command -v gcloud >/dev/null || { echo "gcloud is not installed"; exit 1; }
command -v pulumi >/dev/null || { echo "pulumi is not installed"; exit 1; }
command -v python3 >/dev/null || { echo "python3 is not installed"; exit 1; }

echo "project = ${PROJECT}"
echo "region  = ${REGION}"
echo "bucket  = ${BUCKET}"
echo

bucket_exists() {
  gcloud storage buckets describe "${BUCKET}" --project="${PROJECT}" >/dev/null 2>&1
}

# Key versions that are still costing something and still usable. A version already scheduled for
# destruction, or already destroyed, is not this script's business a second time.
live_key_versions() {
  gcloud kms keys versions list \
    --key="${KEY}" --keyring="${KEYRING}" --location="${REGION}" --project="${PROJECT}" \
    --filter="state=ENABLED OR state=DISABLED" \
    --format="value(name.basename())" 2>/dev/null || true
}

VERSIONS="$(live_key_versions)"

if ! bucket_exists && [[ -z "${VERSIONS}" ]]; then
  echo "Nothing to do: the bucket is gone and no key version is still live."
  echo "If the key ring is still listed, the block at the end of a previous run says why."
  exit 0
fi

echo "==> Checking that every stack in this backend is empty"
if ! bucket_exists; then
  # Nothing left to strand: the state is already gone, so there is no record this script could
  # destroy that a human does not already have to replace with the console.
  echo "    ${BUCKET} is already gone — no state left to protect"
else
  # Not "the stack you meant" — every stack. One bucket holds the state of every environment
  # pointed at this project, so a `prod` stack nobody mentioned is exactly what this check is for.
  # `pulumi:pulumi:Stack` is the root node of a deployment and `pulumi:providers:*` are the
  # provider instances; both survive a destroy and neither is a cloud resource.
  STACKS="$(pulumi -C "${PULUMI_DIR}" stack ls --json |
    python3 -c 'import json, sys; print("\n".join(s["name"] for s in json.load(sys.stdin)))')"

  if [[ -z "${STACKS}" ]]; then
    echo "    no stacks in ${BUCKET} — nothing can be stranded"
  else
    remaining=0
    while read -r stack; do
      [[ -n "${stack}" ]] || continue
      count="$(pulumi -C "${PULUMI_DIR}" stack export --stack "${stack}" | python3 -c '
import json, sys


def count(document):
    # A stack that has never been applied has no resources key at all; that is zero, not an error.
    deployment = document.get("deployment") or {}
    resources = deployment.get("resources") or []
    if not isinstance(resources, list):
        return "unreadable"
    return sum(
        1
        for r in resources
        if isinstance(r, dict)
        and r.get("type") != "pulumi:pulumi:Stack"
        and not str(r.get("type", "")).startswith("pulumi:providers:")
    )


try:
    print(count(json.load(sys.stdin)))
except Exception:
    print("unreadable")')"
      # A guard that fails open is not a guard. Anything other than a number — an export that came
      # back empty, a shape this does not understand — counts as "do not touch the bucket".
      if [[ ! "${count}" =~ ^[0-9]+$ ]]; then
        echo "    ${stack}: could not read its state; refusing to guess" >&2
        remaining=1
      elif [[ "${count}" -gt 0 ]]; then
        echo "    ${stack}: ${count} resource(s) still exist" >&2
        remaining=1
      else
        echo "    ${stack}: empty"
      fi
    done <<<"${STACKS}"

    if [[ "${remaining}" -ne 0 ]]; then
      cat >&2 <<EOF

Refusing to delete ${BUCKET}.

The state in that bucket is the only record of what the stacks above created. Delete it while any
of them is unaccounted for and those resources keep running with nothing left that names them.
Destroy what is still there, then run this again:

  just infra-destroy <stack>

EOF
      exit 1
    fi
  fi
fi

if [[ ! -t 0 ]]; then
  cat >&2 <<EOF

Refusing to run with nothing there to confirm. This deletes the state bucket and every version
in it, which cannot be undone:

  ${BUCKET}

Run it from a terminal. There is no flag that skips this.

EOF
  exit 1
fi

cat <<EOF

About to delete, permanently:

  the state bucket ${BUCKET}, and every object version in it
  every live version of key '${KEY}' in key ring '${KEYRING}' (${REGION})

Type the bucket name to confirm.
EOF
read -r -p "  ${BUCKET}: " reply
if [[ "${reply}" != "${BUCKET}" ]]; then
  echo "    did not match — nothing was deleted"
  exit 1
fi

echo
echo "==> State bucket"
# The bucket goes before the key. If this stops half way, a bucket that is gone and a key that
# still works costs pennies; a key that is gone and a bucket still holding KMS-encrypted stack
# configuration is unreadable state you are still paying to store.
#
# `--recursive` on a bucket URL deletes the objects and then the bucket, and implies
# `--all-versions` — which this bucket needs, because `state-bucket.sh` turned versioning on.
if bucket_exists; then
  gcloud storage rm --recursive "${BUCKET}" --project="${PROJECT}"
  echo "    deleted"
else
  echo "    already gone"
fi

echo "==> KMS key versions"
# A KMS key version is never deleted on request. `destroy` moves it to "scheduled for destruction"
# and it stays there for the key's destroy-scheduled duration — 30 days by default, fixed when the
# key was created and not changeable afterwards. Only then is it DESTROYED, and only then can the
# version, the key and the key ring be deleted.
#
# `--quiet` suppresses gcloud's own prompt because the confirmation already happened above, and it
# was stricter than gcloud's: nothing reaches this line without a human typing the bucket name.
if [[ -z "${VERSIONS}" ]]; then
  echo "    nothing live to schedule"
else
  while read -r version; do
    [[ -n "${version}" ]] || continue
    gcloud kms keys versions destroy "${version}" \
      --key="${KEY}" --keyring="${KEYRING}" --location="${REGION}" --project="${PROJECT}" \
      --quiet
    echo "    version ${version} scheduled for destruction"
  done <<<"${VERSIONS}"
fi

cat <<EOF

Done, as far as one command can go. Two things are still true and neither is a bug:

  * The bucket is soft-deleted, not gone. Cloud Storage enables soft delete on every bucket that
    supports it, with a default retention of 7 days, and soft-deleted data keeps accruing storage
    charges until that expires. A Pulumi state bucket is small; this is a line of pennies, not a
    bill, and it clears itself.

  * The key ring, the key and its versions still exist. A scheduled version is still billed until
    it reaches DESTROYED. Once the destroy-scheduled duration has passed — 30 days unless this key
    was created with a different one — finish it off, in this order:

      gcloud kms keys versions list --key=${KEY} --keyring=${KEYRING} \\
        --location=${REGION} --project=${PROJECT}
      gcloud kms keys versions delete <version> --key=${KEY} --keyring=${KEYRING} \\
        --location=${REGION} --project=${PROJECT}
      gcloud kms keys delete ${KEY} --keyring=${KEYRING} \\
        --location=${REGION} --project=${PROJECT}
      gcloud kms keyrings delete ${KEYRING} --location=${REGION} --project=${PROJECT}

    Until then a restore is still possible: 'gcloud kms keys versions restore' cancels the
    destruction of a version that has not passed its date yet.

If the project existed only for this service, deleting the project is the one command that ends
every charge at once, these included:

  gcloud projects delete ${PROJECT}

EOF
