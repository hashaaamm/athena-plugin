#!/usr/bin/env bash
# Generate the secrets the stack creates empty, and record the version each one was added as.
#
# Pulumi standards MUST-3: a secret value never appears in the program, its configuration or its
# state (ADR-0023 is how this repository meets it). So the stack creates two empty Secret Manager
# containers and mounts neither until its version number is in stack configuration. This script is
# the out-of-band half:
#
#   jwt-secret          a fresh HS256 signing key, which the service mounts as JWT_SECRET
#   db-admin-password   a fresh password for Cloud SQL's built-in `postgres` login, set on the
#                       instance and stored for the migration job, which mounts it as
#                       DATABASE_PASSWORD. The service never sees it: it logs in with IAM.
#
# then `pulumi config set` jwtSecretVersion and dbAdminPasswordVersion. Run `just infra-up` after
# it, so the stack mounts the pinned versions.
#
# Each value is generated here, piped straight to where it is kept, and forgotten. It is never
# printed, never written to a file, and never passed as a command-line argument, where any process
# listing on this machine would show it.
#
# Idempotent: a secret whose version is already configured is kept. --rotate replaces every one.
# Rotating the signing key signs every user out — every access and refresh token was signed with
# the old key — and the migration job needs the `up` that follows before it can log in again.
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

# python3 reads the Cloud SQL operation's name out of a JSON response; curl is explained below.
for tool in pulumi gcloud openssl curl python3; do
  command -v "${tool}" >/dev/null || { echo "${tool} is not installed" >&2; exit 1; }
done

config() {
  pulumi -C "${PULUMI_DIR}" config get "$1" --stack "${STACK}" 2>/dev/null || true
}
read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null || true
}

PROJECT="$(config project)"
SLUG="$(config slug)"
ENVIRONMENT="$(config environment)"
if [[ -z "${PROJECT}" || -z "${SLUG}" || -z "${ENVIRONMENT}" ]]; then
  echo "  MISSING  project, slug or environment in Pulumi.${STACK}.yaml — is '${STACK}' initialised?" >&2
  exit 1
fi
# The containers this fills, and the instance whose login it sets, are created by `up`.
CONNECTION="$(read_output cloud_sql_instance)"
if [[ -z "${CONNECTION}" ]]; then
  echo "  MISSING  cloud_sql_instance — has 'just infra-up ${STACK}' run? It creates what this fills." >&2
  exit 1
fi
INSTANCE="${CONNECTION##*:}"

echo "stack   = ${STACK}"
echo "project = ${PROJECT}"
echo

# 48 random bytes, base64-encoded with the two characters that are not URL-safe swapped out: 64
# characters, well past the 32 the service requires of a signing key, and safe inside a DSN.
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

# add_version SECRET: adds stdin as a new version of the secret, and prints only its number.
add_version() {
  gcloud secrets versions add "$1" --project "${PROJECT}" --data-file=- --format='value(name)' | sed 's#.*/##'
}

# record_version KEY VERSION: the number is not a secret; it is what the stack pins.
record_version() {
  if [[ -z "$2" ]]; then
    echo "  FAILED  no version number came back for $1" >&2
    exit 1
  fi
  pulumi -C "${PULUMI_DIR}" config set "$1" "$2" --stack "${STACK}"
  echo "  set   $1 = $2"
}

# set_admin_password: sets stdin as the `postgres` login's password.
#
# `gcloud sql users set-password` takes the password only as --password, an argument every process
# listing shows, or from --prompt-for-password, which reads the terminal rather than stdin. So this
# calls the Cloud SQL Admin API's users.update — the endpoint that command calls — with the body on
# stdin and the access token in a header read from a pipe, then waits on the operation it returns.
set_admin_password() {
  local token response operation
  token="$(gcloud auth print-access-token)"
  # The response is an Operation, or an error — neither carries the password.
  if ! response="$(
    { printf '{"name":"postgres","password":"'; cat; printf '"}'; } |
      curl --silent --show-error --fail-with-body -X PUT \
        -H @<(printf 'Authorization: Bearer %s\nContent-Type: application/json\n' "${token}") \
        --data-binary @- \
        "https://sqladmin.googleapis.com/v1/projects/${PROJECT}/instances/${INSTANCE}/users?name=postgres"
  )"; then
    echo "  FAILED  setting the postgres login's password: ${response}" >&2
    exit 1
  fi
  operation="$(python3 -c 'import json, sys; print(json.load(sys.stdin)["name"])' <<<"${response}")"
  gcloud sql operations wait "${operation}" --project "${PROJECT}" --timeout=600 >/dev/null
}

if [[ "${ROTATE}" == true ]]; then
  cat <<EOF
  --rotate: every secret below gets a new value.
            A new signing key signs every user out: tokens signed with the old one stop verifying
            as soon as the service runs with the new one. The migration job cannot log in between
            the password change and the 'just infra-up ${STACK}' that pins the new version.

EOF
fi

if needs jwtSecretVersion; then
  # An assignment, so a failure anywhere in the pipe stops the script under `set -e`.
  version="$(generate | add_version "${SLUG}-jwt-secret-${ENVIRONMENT}")"
  record_version jwtSecretVersion "${version}"
fi

if needs dbAdminPasswordVersion; then
  # Stored first, then set on the login, then recorded. A failure part way leaves an unused
  # version — harmless — rather than a login whose password exists nowhere.
  password="$(generate)"
  version="$(printf '%s' "${password}" | add_version "${SLUG}-db-admin-password-${ENVIRONMENT}")"
  printf '%s' "${password}" | set_admin_password
  unset password
  echo "  set   the postgres login's password on ${INSTANCE}"
  record_version dbAdminPasswordVersion "${version}"
fi

cat <<EOF

Done. Now apply the stack, which mounts the pinned versions:

  just infra-up ${STACK}

then 'just infra-sync-github ${STACK}', which checks secrets_ready before it un-gates CD.

EOF
