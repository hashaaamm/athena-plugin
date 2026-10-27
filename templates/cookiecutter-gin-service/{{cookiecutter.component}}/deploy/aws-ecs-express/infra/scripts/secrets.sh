#!/usr/bin/env bash
# Generate the secret the stack creates empty, and record the version it was put as.
#
# Pulumi standards MUST-3: a secret value never appears in the program, its configuration or its
# state (ADR-0023 is how this repository meets it). So the stack creates the jwt-secret secret
# empty and mounts nothing until its VersionId is in stack configuration. This script is the
# out-of-band half: it generates a fresh HS256 signing key, puts it as a new version, and runs
# `pulumi config set jwtSecretVersion`. The next apply — `just infra-first-deploy`, or once setup
# is done the apply of the merged change — mounts the pinned version.
#
# The database needs nothing from here. The service logs in with an IAM token, and the migration
# task as the master user, whose password RDS itself generates, stores and rotates.
#
# The key is generated here, piped straight into Secrets Manager, and forgotten. It is never
# printed, never written to a file, and never passed as a command-line argument, where any process
# listing on this machine would show it.
#
# Idempotent: a version already configured is kept. --rotate replaces it, and signs every user out —
# every access and refresh token was signed with the old key.
#
#   ./infra/scripts/secrets.sh [stack] [--rotate]
#
set -euo pipefail

STACK="dev"
ROTATE=false
for arg in "$@"; do
  case "${arg}" in
    --rotate) ROTATE=true ;;
    -*) echo "unknown flag ${arg}; usage: secrets.sh [stack] [--rotate]" >&2; exit 2 ;;
    *) STACK="${arg}" ;;
  esac
done

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"

for tool in pulumi aws openssl; do
  command -v "${tool}" >/dev/null || { echo "${tool} is not installed" >&2; exit 1; }
done

config() {
  pulumi -C "${PULUMI_DIR}" config get "$1" --stack "${STACK}" 2>/dev/null || true
}
read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null || true
}

REGION="$(config region)"
SLUG="$(config slug)"
ENVIRONMENT="$(config environment)"
if [[ -z "${REGION}" || -z "${SLUG}" || -z "${ENVIRONMENT}" ]]; then
  echo "  MISSING  region, slug or environment in Pulumi.${STACK}.yaml — is '${STACK}' initialised?" >&2
  exit 1
fi
# The secret this fills is created by `up`; secrets_ready is exported from the first one.
if [[ -z "$(read_output secrets_ready)" ]]; then
  echo "  MISSING  secrets_ready — has 'just infra-up ${STACK}' run? It creates what this fills." >&2
  exit 1
fi

echo "stack  = ${STACK}"
echo "region = ${REGION}"
echo

# 48 random bytes, base64-encoded with the two characters that are not URL-safe swapped out: 64
# characters, well past the 32 the service requires of a signing key.
generate() {
  openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n'
}

# needs KEY: whether KEY's secret has to be generated. A configured version is kept unless
# --rotate, so running this twice changes nothing.
needs() {
  local current
  current="$(config "$1")"
  if [[ -n "${current}" && "${ROTATE}" != true ]]; then
    echo "  kept  $1 = ${current} (--rotate replaces it)"
    return 1
  fi
}

# put_version SECRET: puts stdin as a new version of the secret — file:///dev/stdin is the AWS
# CLI reading the value from the pipe — and prints only its VersionId.
put_version() {
  aws secretsmanager put-secret-value --region "${REGION}" --secret-id "$1" \
    --secret-string file:///dev/stdin --query VersionId --output text
}

# record_version KEY VERSION: the VersionId is not a secret; it is what the stack pins.
record_version() {
  if [[ -z "$2" || "$2" == "None" ]]; then
    echo "  FAILED  no VersionId came back for $1" >&2
    exit 1
  fi
  # --plaintext: a version is not a secret, and the Pulumi CLI refuses a value that looks like one
  # — a UUID-shaped VersionId among them — unless told. Encrypted, it would also be unreadable to
  # anyone checking which version the stack pins.
  pulumi -C "${PULUMI_DIR}" config set --plaintext "$1" "$2" --stack "${STACK}"
  echo "  set   $1 = $2"
}

if [[ "${ROTATE}" == true ]]; then
  cat <<EOF
  --rotate: a new signing key signs every user out. Tokens signed with the old one stop verifying
            as soon as the service runs with the new one, after the apply that pins it.

EOF
fi

if needs jwtSecretVersion; then
  # An assignment, so a failure anywhere in the pipe stops the script under `set -e`.
  version="$(generate | put_version "${SLUG}-jwt-secret-${ENVIRONMENT}")"
  record_version jwtSecretVersion "${version}"
fi

cat <<EOF

Done. The stack mounts what this recorded on its next apply:

  just infra-first-deploy ${STACK}     if the service does not exist yet; the first deploy applies it
  a pull request with Pulumi.${STACK}.yaml  if it does: {{ cookiecutter.component }}-infra.yml applies it on merge

then 'just infra-sync-github ${STACK}', which checks secrets_ready before it un-gates CD.

EOF
