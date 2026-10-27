#!/usr/bin/env bash
# Generate the stack's secrets, store each as a new Secrets Manager version, and pin that version in
# stack configuration. Run it once after the first `just infra-up` and before `just
# infra-first-deploy`, which applies with what it pinned and creates the service referencing it:
#
#   ./infra/scripts/secrets.sh [stack] [--rotate]      (just infra-secrets [stack] [--rotate])
#
# Why a script and not the stack: a secret *value* never appears in a Pulumi program, in a
# `Pulumi.<stack>.yaml` or in state (Pulumi standards MUST-3, ADR-0023). The stack creates the empty
# secret; this fills it, and what the stack learns is a `VersionId`, which is not a secret.
#
# One secret, 48 random bytes as URL-safe base64 (64 characters, well past the 32 the backend
# requires of a signing key):
#
#   jwt-secret   the key the service signs access and refresh tokens with -> jwtSecretVersion
#
# The database needs nothing from here. The service logs in with IAM authentication and has no
# password at all; the migration task logs in as the master user, whose password RDS generates,
# stores and rotates in a secret of its own (`manage_master_user_password`).
#
# A value is never printed, never an argument to another process, and never written to a file: it
# lives in a shell variable and reaches the AWS CLI on stdin, through `printf`, which is a shell
# builtin and so never appears in the process list.
#
# Idempotent. A secret whose version is already pinned is kept; `--rotate` makes a new version and
# pins that instead. Rotating `jwt-secret` signs every user out — every access and refresh token was
# signed with the old key — so do it when you mean that.
set -euo pipefail

STACK="dev"
ROTATE=false
for arg in "$@"; do
  case "${arg}" in
    --rotate) ROTATE=true ;;
    -*) echo "unknown flag: ${arg}" >&2; exit 2 ;;
    *) STACK="${arg}" ;;
  esac
done

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"

for tool in aws pulumi openssl; do
  command -v "${tool}" >/dev/null || { echo "${tool} is not installed" >&2; exit 1; }
done

config_get() {
  pulumi -C "${PULUMI_DIR}" config get "$1" --stack "${STACK}" 2>/dev/null || true
}

config_set() {
  # --plaintext: a version id is not a secret, and the Pulumi CLI refuses a UUID-shaped value
  # without being told which it is. Encrypted, it would also be hidden from the program that has to
  # pin exactly that version.
  pulumi -C "${PULUMI_DIR}" config set --plaintext "$1" "$2" --stack "${STACK}" >/dev/null
}

ACCOUNT="$(config_get accountId)"
REGION="$(config_get region)"
SLUG="$(config_get slug)"
ENVIRONMENT="$(config_get environment)"
if [[ -z "${ACCOUNT}" || -z "${REGION}" || -z "${SLUG}" || -z "${ENVIRONMENT}" ]]; then
  echo "stack '${STACK}' has no accountId, region, slug or environment — has it been initialised?" >&2
  exit 1
fi

# The same guard as the bootstrap script and the provider: a shell on another account would write a
# signing key into somebody else's Secrets Manager, or fail on a secret that is not there.
CALLER="$(aws sts get-caller-identity --query Account --output text)"
if [[ "${CALLER}" != "${ACCOUNT}" ]]; then
  echo "this shell is on account ${CALLER}; stack '${STACK}' belongs to ${ACCOUNT}" >&2
  exit 1
fi

echo "stack   = ${STACK}"
echo "account = ${ACCOUNT}"
echo "region  = ${REGION}"
echo

# A secret's name, exactly as the stack names it: `StackConfig.name(slug, name)` in config.py. A
# test in infra/pulumi/tests asserts the two agree.
secret_id() {
  echo "${SLUG}-$1-${ENVIRONMENT}"
}

# 48 random bytes, base64 with the URL-safe alphabet and no padding.
generate() {
  openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n'
}

# Reads the value on stdin; prints the new version's VersionId, never the value.
add_version() {
  aws secretsmanager put-secret-value --secret-id "$(secret_id "$1")" --region "${REGION}" \
    --secret-string file:///dev/stdin --query VersionId --output text
}

# fill <secret> <config key>: generate, store, then pin.
fill() {
  local name="$1" key="$2" pinned value version
  pinned="$(config_get "${key}")"
  if [[ -n "${pinned}" && "${ROTATE}" != true ]]; then
    echo "  kept     ${name} at version ${pinned} (--rotate makes a new one)"
    return
  fi
  value="$(generate)"
  version="$(printf '%s' "${value}" | add_version "${name}")"
  value=""
  if [[ -z "${version}" || "${version}" == "None" ]]; then
    echo "  FAILED   no VersionId came back for ${name}" >&2
    exit 1
  fi
  config_set "${key}" "${version}"
  echo "  pinned   ${name} at version ${version} (${key})"
}

if [[ "${ROTATE}" == true ]]; then
  echo "  --rotate: a new signing key signs every user out — every token was signed with the old one."
  echo
fi

fill jwt-secret jwtSecretVersion

# What comes next depends on whether the first deploy has happened: before it there is no service
# to reference anything, and the first deploy's own apply is the one that pins these.
SERVICE_URL="$(pulumi -C "${PULUMI_DIR}" stack output service_url --stack "${STACK}" 2>/dev/null || true)"
if [[ -z "${SERVICE_URL}" ]]; then
  cat <<EOF

Done. Nothing above is a secret: the value went straight into Secrets Manager.
Next, the first deploy: it builds the real image, applies with these versions pinned, migrates and
smoke-tests the service.

  just infra-first-deploy ${STACK}
EOF
else
  cat <<EOF

Done. Nothing above is a secret: the value went straight into Secrets Manager.
Commit infra/pulumi/Pulumi.${STACK}.yaml and merge it: {{ cookiecutter.component }}-infra.yml applies it, and the
service references the pinned version.
EOF
fi
