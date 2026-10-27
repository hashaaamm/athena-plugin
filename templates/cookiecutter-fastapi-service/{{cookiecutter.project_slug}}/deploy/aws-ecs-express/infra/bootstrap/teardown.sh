#!/usr/bin/env bash
# Remove what `pulumi destroy` cannot: the state bucket, the KMS key and the GitHub OIDC provider
# that `state-bucket.sh` created. Run once, by a human, AFTER every stack in this backend has been
# destroyed.
#
#   ./infra/bootstrap/teardown.sh
#
# `pulumi destroy` removes every resource a stack created, and nothing else. It does not remove the
# bucket its own state lives in, the KMS key that encrypts it, or the account's OIDC provider,
# because the stack never created them — `state-bucket.sh` did, with the plain AWS CLI, before the
# stack existed. No Pulumi command will ever take them away. This script is the other end of that
# one.
#
# THE BUCKET MAY NOT BE YOURS. `state-bucket.sh` names it `pulumi-state-<account>-<region>` with no
# per-service component, so every service generated into one AWS account and region bootstraps
# onto the same bucket and the second one finds the first's state already there. One backend holds
# many Pulumi projects, a directory each, under `.pulumi/stacks/`. Deleting it takes all of them. So
# the first thing this script does is list that prefix and refuse when a directory it does not own
# is in it — Pulumi standards MUST-8, and the one check `pulumi` itself cannot make.
#
# THE ORDER IS NOT A PREFERENCE. The bucket holds the state that `pulumi destroy` reads. Delete it
# first and every resource the stack created still exists, still bills, and the only record of what
# those resources are has just been deleted — the console and `aws ... list` are all you have left.
# So this script also refuses to run until every stack in the backend is empty, and it checks rather
# than asking you.
#
# It then asks you to type the bucket name, and it refuses to run when nothing can answer, the way
# `pulumi up` refuses a non-interactive apply. There is deliberately no `--yes` and no `--force`:
# this is irreversible — S3 has no soft delete to fall back on — it happens at most once per account,
# and nothing about it needs to run from a pipeline.
#
# Safe to re-run: every step checks before it acts.
set -euo pipefail

ACCOUNT="${AWS_ACCOUNT_ID:-{{ cookiecutter.aws_account_id }}}"
REGION="${AWS_REGION:-{{ cookiecutter.aws_region }}}"
BUCKET="pulumi-state-${ACCOUNT}-${REGION}"
KEY_ALIAS="alias/pulumi-state"
OIDC_URL="https://token.actions.githubusercontent.com"
OIDC_ARN="arn:aws:iam::${ACCOUNT}:oidc-provider/${OIDC_URL#https://}"
# KMS waits at least 7 days and at most 30 before it deletes a key. The longest, because the only
# thing a shorter wait buys is less time to notice that something still needed it.
KEY_DELETION_DAYS=30

export AWS_REGION="${REGION}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"

# The Pulumi project this checkout is. Both things below are scoped by it — `pulumi stack ls` sees
# only this project, and the backend gives each project its own directory — so it is what "mine"
# means. Read from Pulumi.yaml rather than written twice: a name that can drift from the one the
# backend actually uses is a guard comparing against the wrong string.
PULUMI_PROJECT="$(sed -n 's/^name:[[:space:]]*//p' "${PULUMI_DIR}/Pulumi.yaml" | head -1 | tr -d "\"'")"
[[ -n "${PULUMI_PROJECT}" ]] || { echo "could not read 'name:' from ${PULUMI_DIR}/Pulumi.yaml"; exit 1; }

command -v aws >/dev/null || { echo "aws is not installed"; exit 1; }
command -v pulumi >/dev/null || { echo "pulumi is not installed"; exit 1; }
command -v python3 >/dev/null || { echo "python3 is not installed"; exit 1; }

echo "account = ${ACCOUNT}"
echo "region  = ${REGION}"
echo "bucket  = s3://${BUCKET}"
echo "project = ${PULUMI_PROJECT} (pulumi)"
echo

# The same wrong-account guard as state-bucket.sh, and it matters more here: this one deletes.
CALLER="$(aws sts get-caller-identity --query Account --output text)"
if [[ "${CALLER}" != "${ACCOUNT}" ]]; then
  echo "Refusing: these credentials are for account ${CALLER}, not ${ACCOUNT}." >&2
  exit 1
fi

bucket_exists() {
  aws s3api head-bucket --bucket "${BUCKET}" --expected-bucket-owner "${ACCOUNT}" >/dev/null 2>&1
}

# The key behind the alias, if it is still usable. A key already pending deletion is not this
# script's business a second time.
live_key() {
  local arn state
  arn="$(aws kms describe-key --key-id "${KEY_ALIAS}" --query KeyMetadata.Arn --output text 2>/dev/null)" || return 0
  state="$(aws kms describe-key --key-id "${arn}" --query KeyMetadata.KeyState --output text)"
  if [[ "${state}" == "Enabled" || "${state}" == "Disabled" ]]; then
    echo "${arn}"
  fi
}

oidc_exists() {
  aws iam get-open-id-connect-provider --open-id-connect-provider-arn "${OIDC_ARN}" >/dev/null 2>&1
}

KEY_ARN="$(live_key)"

if ! bucket_exists && [[ -z "${KEY_ARN}" ]] && ! oidc_exists; then
  echo "Nothing to do: the bucket is gone, no key is still live, and the OIDC provider is gone."
  exit 0
fi

echo "==> Checking that this backend holds nobody else's state"
if ! bucket_exists; then
  echo "    s3://${BUCKET} is already gone — there is no backend left to share"
else
  # Ownership is not the same question as resource count, which is why this check is separate from
  # the one below and comes first. Another project's stacks can hold nothing today and be applied
  # tomorrow; deleting the bucket out from under them strands the result either way.
  if PREFIXES="$(aws s3api list-objects-v2 --bucket "${BUCKET}" --prefix ".pulumi/stacks/" \
      --delimiter "/" --query "CommonPrefixes[].Prefix" --output text 2>&1)"; then
    PROJECTS="$(printf '%s\n' "${PREFIXES}" | tr '\t' '\n' |
      sed -n 's#^\.pulumi/stacks/\([^/][^/]*\)/$#\1#p')"
  else
    # A listing that cannot be read counts as shared, the same way an export that cannot be parsed
    # counts as non-empty below. A guard that fails open is not a guard.
    echo "    could not list s3://${BUCKET}/.pulumi/stacks/; refusing to guess" >&2
    printf '%s\n' "${PREFIXES}" | sed 's/^/    /' >&2
    exit 1
  fi

  OTHERS="$(printf '%s\n' "${PROJECTS}" | grep -vx -e '' -e "${PULUMI_PROJECT}" || true)"
  if [[ -n "${OTHERS}" ]]; then
    cat >&2 <<EOF

Refusing to delete s3://${BUCKET}.

It is not this project's alone. Alongside '${PULUMI_PROJECT}' it holds the state of:

$(printf '%s\n' "${OTHERS}" | sed 's/^/  /')

Deleting the bucket deletes their state too, and state is the only record of what a stack created.
Remove just this project's state and leave the backend standing:

  cd infra/pulumi && uv run pulumi stack rm <stack> --remove-backups

The bucket, the key and the OIDC provider stay. A state bucket costs pennies a month.

EOF
    exit 1
  fi
  if [[ -z "${PROJECTS}" ]]; then
    echo "    s3://${BUCKET} holds no stack state at all"
  else
    echo "    s3://${BUCKET} holds only '${PULUMI_PROJECT}'"
  fi
fi

echo "==> Checking that every stack in this backend is empty"
if ! bucket_exists; then
  # Nothing left to strand: the state is already gone, so there is no record this script could
  # destroy that a human does not already have to replace with the console.
  echo "    s3://${BUCKET} is already gone — no state left to protect"
else
  # Not "the stack you meant" — every stack in the backend. A `prod` stack nobody mentioned is
  # exactly what this check is for. `--all` is load-bearing: without it `stack ls` returns only
  # stacks whose project name matches this workspace, so another project's live stack is not
  # under-counted but absent, and the guard reports an empty backend and stands aside. With it the
  # names come back fully qualified — `organization/<project>/<stack>` — which `stack export`
  # accepts from this workspace.
  #
  # `pulumi:pulumi:Stack` is the root node of a deployment and `pulumi:providers:*` are the
  # provider instances; both survive a destroy and neither is a cloud resource.
  STACKS="$(pulumi -C "${PULUMI_DIR}" stack ls --all --json |
    python3 -c 'import json, sys; print("\n".join(s["name"] for s in json.load(sys.stdin)))')"

  if [[ -z "${STACKS}" ]]; then
    echo "    no stacks in s3://${BUCKET} — nothing can be stranded"
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

Refusing to delete s3://${BUCKET}.

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

  s3://${BUCKET}

Run it from a terminal. There is no flag that skips this.

EOF
  exit 1
fi

cat <<EOF

About to delete, permanently:

  the state bucket s3://${BUCKET}, and every object version in it
  the KMS key ${KEY_ALIAS} (${KEY_ARN:-already pending deletion}), after a ${KEY_DELETION_DAYS}-day wait
  the GitHub OIDC provider, unless an IAM role in this account still trusts it

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
# still works costs pennies; a key that is gone and a bucket still holding KMS-encrypted state is
# unreadable state you are still paying to store.
#
# A versioned bucket cannot be deleted while it holds any version or delete marker, and
# `aws s3 rb --force` removes only current versions. So: list every version and marker, delete them
# a page at a time, and only then the bucket.
if bucket_exists; then
  while :; do
    batch="$(aws s3api list-object-versions --bucket "${BUCKET}" --max-items 1000 --output json |
      python3 -c '
import json, sys

page = json.load(sys.stdin)
objects = [
    {"Key": v["Key"], "VersionId": v["VersionId"]}
    for v in (page.get("Versions") or []) + (page.get("DeleteMarkers") or [])
]
print(json.dumps({"Objects": objects, "Quiet": True}) if objects else "")')"
    [[ -n "${batch}" ]] || break
    aws s3api delete-objects --bucket "${BUCKET}" --delete "${batch}" >/dev/null
  done
  aws s3api delete-bucket --bucket "${BUCKET}"
  echo "    deleted"
else
  echo "    already gone"
fi

echo "==> KMS key"
# A KMS key is never deleted on request. It is scheduled, waits out the pending window, and only
# then is it gone — and until then `aws kms cancel-key-deletion` brings it back. The alias goes now,
# so a later `state-bucket.sh` creates a fresh key rather than finding one that is about to vanish.
if [[ -z "${KEY_ARN}" ]]; then
  echo "    nothing live to schedule"
else
  aws kms schedule-key-deletion --key-id "${KEY_ARN}" \
    --pending-window-in-days "${KEY_DELETION_DAYS}" >/dev/null
  aws kms delete-alias --alias-name "${KEY_ALIAS}" 2>/dev/null || true
  echo "    ${KEY_ARN} scheduled for deletion in ${KEY_DELETION_DAYS} days"
fi

echo "==> GitHub OIDC provider"
# Shared by every service in the account, and not only Pulumi ones. It goes only when no role's
# trust policy names it any more; otherwise deleting it locks every such pipeline out at once.
if ! oidc_exists; then
  echo "    already gone"
else
  TRUSTING="$(aws iam list-roles --output json | python3 -c '
import json, sys

arn = sys.argv[1]
for role in json.load(sys.stdin).get("Roles", []):
    if arn in json.dumps(role.get("AssumeRolePolicyDocument") or {}):
        print(role["RoleName"])' "${OIDC_ARN}")"
  if [[ -n "${TRUSTING}" ]]; then
    echo "    kept: still trusted by"
    printf '%s\n' "${TRUSTING}" | sed 's/^/      /'
  else
    aws iam delete-open-id-connect-provider --open-id-connect-provider-arn "${OIDC_ARN}"
    echo "    deleted"
  fi
fi

cat <<EOF

Done, as far as one command can go. One thing is still true and it is not a bug:

  * The KMS key still exists, pending deletion, for ${KEY_DELETION_DAYS} days. Nothing can use it in
    that time, and nothing needs to be done when it ends: KMS deletes it. Until then it can be
    brought back, and its alias recreated, with:

      aws kms cancel-key-deletion --key-id <key-arn> --region ${REGION}
      aws kms enable-key --key-id <key-arn> --region ${REGION}
      aws kms create-alias --alias-name ${KEY_ALIAS} --target-key-id <key-arn> --region ${REGION}

The bucket is gone outright. S3 has no soft delete, and its versions went with it.

If the AWS account existed only for this service, closing the account ends every charge at once.

EOF
