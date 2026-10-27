#!/usr/bin/env bash
# The first deploy: build the real bundle on this machine and publish it, before CD exists.
#
#   just infra-first-deploy [stack] [--yes]
#
# The stack has created the bucket and the distribution, and the bucket is empty: CloudFront answers
# every path with a 403 from S3, because the index.html every route is served does not exist yet.
# This builds the bundle exactly as CD does — `pnpm install --frozen-lockfile`, then `pnpm build`
# with the stack's api_url export as VITE_API_URL — and publishes it with infra/scripts/publish.sh,
# the script CD runs: the same two syncs, the same invalidation, the same smoke test. Afterwards
# `just infra-sync-github` un-gates CD, and every later deploy is CD's.
#
# It publishes what is committed. A working tree with uncommitted changes under the application —
# anything outside infra/, whose Pulumi.<stack>.yaml this sequence edits — is refused, because the
# site would then serve a build of no commit, and the first CD run would silently replace it.
#
# It asks before it uploads, and refuses when nothing can answer, the way `pulumi up` does.
# `--yes` is the unattended form.
set -euo pipefail

STACK="${1:-dev}"
shift || true
YES=0
for flag in "$@"; do
  case "${flag}" in
    --yes | -y) YES=1 ;;
    *) echo "unknown flag ${flag}: the only one is --yes" >&2; exit 1 ;;
  esac
done

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"
APP_DIR="$(cd "${HERE}/../.." && pwd)"

for tool in pulumi aws pnpm git curl; do
  command -v "${tool}" >/dev/null || { echo "${tool} is not installed. infra/README.md lists what runs on the host." >&2; exit 1; }
done

read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null
}

echo "stack = ${STACK}"
echo "app   = ${APP_DIR}"
echo

echo "==> Checking the stack has been applied"
BUCKET="$(read_output site_bucket || true)"
DISTRIBUTION="$(read_output cloudfront_distribution_id || true)"
SITE_URL="$(read_output site_url || true)"
ACCOUNT="$(read_output aws_account_id || true)"
REGION="$(read_output aws_region || true)"
if [[ -z "${BUCKET}" || -z "${DISTRIBUTION}" || -z "${SITE_URL}" ]]; then
  echo "    stack '${STACK}' has no site_bucket output: run 'just infra-up ${STACK}' first" >&2
  exit 1
fi
echo "    s3://${BUCKET} behind ${SITE_URL}"

# What the bundle calls is the stack's api_url export, the value CD builds with too: `/` — the page's
# own origin — while the distribution serves the API under /api/* and /health/*, the backend's own
# address when the browser calls it directly. The export rather than the configuration, because the
# route it depends on exists only once applied; checked against the configuration, so an apiUrl
# changed since the last apply is applied before a bundle is built for it.
CONFIGURED="$(pulumi -C "${PULUMI_DIR}" config get apiUrl --stack "${STACK}" 2>/dev/null || true)"
API_URL="$(read_output api_url || true)"
UPSTREAM="$(read_output api_upstream || true)"
if [[ -z "${CONFIGURED}" ]]; then
  cat >&2 <<EOF
    apiUrl is not set, so this build would compile the local development address into the bundle.
    Set the backend's address on the stack, apply it, and run this again:

      pulumi -C infra/pulumi config set --plaintext apiUrl 'https://<the backend URL>' --stack ${STACK}
      just infra-up ${STACK}
EOF
  exit 1
fi
if [[ -z "${API_URL}" || "${UPSTREAM}" != "${CONFIGURED}" ]]; then
  cat >&2 <<EOF
    apiUrl is ${CONFIGURED}, and the stack was last applied with '${UPSTREAM}'. Apply it, so the
    distribution and the bundle agree on where the API is, and run this again:

      just infra-up ${STACK}
EOF
  exit 1
fi
if [[ "${API_URL}" == "/" ]]; then
  echo "    the bundle will call its own origin; ${SITE_URL}/api/* and /health/* reach ${UPSTREAM}"
else
  echo "    the bundle will call ${API_URL}"
fi

echo "==> Checking these credentials are for account ${ACCOUNT}"
export AWS_REGION="${REGION}" AWS_DEFAULT_REGION="${REGION}"
CALLER="$(aws sts get-caller-identity --query Account --output text)"
if [[ "${CALLER}" != "${ACCOUNT}" ]]; then
  echo "    refusing: these credentials are for account ${CALLER}, not ${ACCOUNT}" >&2
  exit 1
fi

echo "==> Checking the application is committed"
DIRTY="$(git -C "${APP_DIR}" status --porcelain -- . ':(exclude)infra')"
if [[ -n "${DIRTY}" ]]; then
  echo "    refusing: uncommitted changes in the application would be published as no commit:" >&2
  printf '%s\n' "${DIRTY}" | sed 's/^/      /' >&2
  exit 1
fi
COMMIT="$(git -C "${APP_DIR}" rev-parse --short HEAD)"
echo "    ${COMMIT}"

if [[ "${YES}" -ne 1 ]]; then
  if [[ ! -t 0 ]]; then
    echo "Refusing to publish with nothing there to confirm. Run it from a terminal, or pass --yes." >&2
    exit 1
  fi
  read -r -p "Build ${COMMIT} and publish it to ${SITE_URL}? [y/N] " reply
  [[ "${reply}" == "y" || "${reply}" == "Y" ]] || { echo "    nothing was published"; exit 1; }
fi

# The same two commands as CD's install and build steps. VITE_API_URL in the environment wins over
# any .env file: Vite never overwrites a variable that is already set.
echo "==> Build"
pnpm --dir "${APP_DIR}" install --frozen-lockfile
VITE_API_URL="${API_URL}" pnpm --dir "${APP_DIR}" build

echo "==> Publish"
BUCKET="${BUCKET}" DISTRIBUTION="${DISTRIBUTION}" SITE_URL="${SITE_URL}" API_URL="${API_URL}" \
  "${HERE}/publish.sh" "${APP_DIR}/dist"

cat <<EOF

Live: ${SITE_URL}

Next, so CD owns every deploy from here:

  just infra-sync-github ${STACK}
  git push

Commit infra/pulumi/Pulumi.${STACK}.yaml first if apiUrl or githubSubjectPrefix changed it.
EOF
