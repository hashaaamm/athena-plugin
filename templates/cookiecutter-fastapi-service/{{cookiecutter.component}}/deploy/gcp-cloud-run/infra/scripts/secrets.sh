#!/usr/bin/env bash
# Generate the stack's secrets, store each as a new Secret Manager version, and pin that version in
# stack configuration. Run it once after the first `just infra-up` and before `just
# infra-first-deploy`, which applies with what it pinned and creates the service and the migration
# job mounting it:
#
#   ./infra/scripts/secrets.sh [stack] [--rotate]      (just infra-secrets [stack] [--rotate])
#
# Why a script and not the stack: a secret *value* never appears in a Pulumi program, in a
# `Pulumi.<stack>.yaml` or in state (Pulumi standards MUST-3, ADR-0023). The stack creates the empty
# containers; this fills them, and what the stack learns is a version number, which is not a secret.
#
# Two secrets, each 48 random bytes as URL-safe base64 (64 characters, well past the 32 the backend
# requires of a signing key, and safe inside a DSN):
#
#   jwt-secret          the key the service signs access and refresh tokens with -> jwtSecretVersion
#   db-admin-password   the `postgres` login the migration job uses; also set on the Cloud SQL
#                       instance                                             -> dbAdminPasswordVersion
#
# A value is never printed, never an argument to another process, and never written to a file: it
# lives in a shell variable and reaches each command on stdin, through `printf`, which is a shell
# builtin and so never appears in the process list.
#
# Idempotent. A secret whose version is already pinned is kept; `--rotate` makes a new version of
# every secret and pins that instead. Rotating `jwt-secret` signs every user out — every access and
# refresh token was signed with the old key — so do it when you mean that.
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

# curl and python3 are for the `postgres` password — see `set_postgres_password`.
for tool in gcloud pulumi openssl curl python3; do
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

PROJECT="$(config_get project)"
SLUG="$(config_get slug)"
ENVIRONMENT="$(config_get environment)"
if [[ -z "${PROJECT}" || -z "${SLUG}" || -z "${ENVIRONMENT}" ]]; then
  echo "stack '${STACK}' has no project, slug or environment configured — has it been initialised?" >&2
  exit 1
fi

# The instance the `postgres` password is set on, from the stack's own export: `project:region:name`.
CONNECTION="$(pulumi -C "${PULUMI_DIR}" stack output cloud_sql_instance --stack "${STACK}" 2>/dev/null || true)"
if [[ -z "${CONNECTION}" ]]; then
  echo "stack '${STACK}' exports no cloud_sql_instance — run 'just infra-up ${STACK}' first" >&2
  exit 1
fi
INSTANCE="${CONNECTION##*:}"

echo "stack    = ${STACK}"
echo "project  = ${PROJECT}"
echo "instance = ${INSTANCE}"
echo

# A secret's id, exactly as the stack names it: `StackConfig.name(slug, name)` in config.py. A test
# in infra/pulumi/tests asserts the two agree.
secret_id() {
  echo "${SLUG}-$1-${ENVIRONMENT}"
}

# 48 random bytes, base64 with the URL-safe alphabet and no padding.
generate() {
  openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n'
}

# Reads the value on stdin; prints the new version's number, never the value.
add_version() {
  local name
  name="$(gcloud secrets versions add "$(secret_id "$1")" --project "${PROJECT}" \
    --data-file=- --format='value(name)')"
  echo "${name##*/}"
}

# Reads the password on stdin and sets it on the instance's `postgres` user, through the Cloud SQL
# Admin API rather than `gcloud sql users set-password`, which takes the password as an argument or
# prompts for it on the terminal. The access token travels as a header read from a file descriptor,
# not as an argument either.
set_postgres_password() {
  local password response operation
  password="$(cat)"
  response="$(
    printf '{"name":"postgres","password":"%s"}' "${password}" |
      curl --fail --silent --show-error -X PUT \
        -H @<(printf 'Authorization: Bearer %s\n' "$(gcloud auth print-access-token)") \
        -H 'Content-Type: application/json' \
        --data-binary @- \
        "https://sqladmin.googleapis.com/v1/projects/${PROJECT}/instances/${INSTANCE}/users?name=postgres"
  )"
  # An operation, which carries no password; wait for it, so the version pinned below is one the
  # instance already accepts.
  operation="$(python3 -c 'import json, sys; print(json.load(sys.stdin)["name"])' <<<"${response}")"
  gcloud sql operations wait "${operation}" --project "${PROJECT}" --timeout=600 --quiet >/dev/null
}

# fill <secret> <config key> [also-run]: generate, store, optionally apply, then pin.
fill() {
  local name="$1" key="$2" apply="${3:-}" pinned value version
  pinned="$(config_get "${key}")"
  if [[ -n "${pinned}" && "${ROTATE}" != true ]]; then
    echo "  kept     ${name} at version ${pinned} (--rotate makes a new one)"
    return
  fi
  value="$(generate)"
  # Stored first, then applied, then pinned. A failure part-way leaves the pinned version — the one
  # the deployment uses — untouched and still matching whatever the instance has.
  version="$(printf '%s' "${value}" | add_version "${name}")"
  if [[ -n "${apply}" ]]; then
    printf '%s' "${value}" | "${apply}"
  fi
  value=""
  if [[ -z "${version}" ]]; then
    echo "  FAILED   no version number came back for ${name}" >&2
    exit 1
  fi
  config_set "${key}" "${version}"
  echo "  pinned   ${name} at version ${version} (${key})"
}

if [[ "${ROTATE}" == true ]]; then
  cat <<EOF
  --rotate: every secret below gets a new value.
            A new signing key signs every user out: every token was signed with the old one.
            The migration job cannot log in between the password change and the
            apply that pins the new version.

EOF
fi

fill jwt-secret jwtSecretVersion
fill db-admin-password dbAdminPasswordVersion set_postgres_password

# What comes next depends on whether the first deploy has happened: before it there is no service
# to reference anything, and the first deploy's own apply is the one that pins these.
SERVICE_URL="$(pulumi -C "${PULUMI_DIR}" stack output service_url --stack "${STACK}" 2>/dev/null || true)"
if [[ -z "${SERVICE_URL}" ]]; then
  cat <<EOF

Done. Nothing above is a secret: the values went straight into Secret Manager and the instance.
Next, the first deploy: it builds the real image, applies with these versions pinned, migrates and
smoke-tests the service.

  just infra-first-deploy ${STACK}
EOF
else
  cat <<EOF

Done. Nothing above is a secret: the values went straight into Secret Manager and the instance.
Commit infra/pulumi/Pulumi.${STACK}.yaml and merge it: {{ cookiecutter.component }}-infra.yml applies it, and the
service and the migration job mount the pinned versions.
EOF
fi
