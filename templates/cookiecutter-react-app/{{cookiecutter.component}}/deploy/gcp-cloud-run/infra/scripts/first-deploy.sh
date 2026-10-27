#!/usr/bin/env bash
# The first deploy: build the real image on this machine, push it, and create the Cloud Run service
# on it — before CD exists.
#
#   just infra-first-deploy [stack] [pulumi up flags, e.g. --yes]
#
# `just infra-up` created everything but the service, because there was nothing real to run. This
# builds the production image exactly as CD does — docker/Dockerfile.prod, target `production`, for
# linux/amd64, with the stack's api_url export as the VITE_API_URL build argument — tags it with the
# commit, pushes it to the stack's registry, sets `serviceImage` to it and applies; the same apply
# gives the service API_UPSTREAM_HOST when the site serves the API. Then it smoke-tests the service
# the way CD does. Afterwards `just infra-sync-github` un-gates CD, and every later image is
# CD's: the service ignores changes to `serviceImage`.
#
# It builds what is committed. A working tree with uncommitted changes under the application —
# anything outside infra/, whose Pulumi.<stack>.yaml this sequence edits — is refused, because the
# image is tagged with a commit it would not be a build of.
#
# `pulumi up` asks for confirmation, as `just infra-up` does, and refuses when nothing can answer.
# Flags after the stack go to `pulumi up`: `just infra-first-deploy dev --yes` is the unattended form.
set -euo pipefail

STACK="${1:-dev}"
shift || true
FLAGS=("$@")

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"
APP_DIR="$(cd "${HERE}/../.." && pwd)"

for tool in pulumi gcloud docker git curl; do
  command -v "${tool}" >/dev/null || { echo "${tool} is not installed. infra/README.md lists what runs on the host." >&2; exit 1; }
done
docker buildx version >/dev/null 2>&1 || { echo "docker buildx is not available" >&2; exit 1; }

read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null
}

echo "stack = ${STACK}"
echo "app   = ${APP_DIR}"
echo

echo "==> Checking the stack has been applied"
IMAGE_REPO="$(read_output image_repo || true)"
if [[ -z "${IMAGE_REPO}" ]]; then
  echo "    stack '${STACK}' has no image_repo output: run 'just infra-up ${STACK}' first" >&2
  exit 1
fi
echo "    ${IMAGE_REPO}"

# What the bundle calls is the stack's api_url export, the value CD builds with too: `/` — the page's
# own origin — while the site's nginx proxies the API under /api/ and /health/, the backend's own
# address when the browser calls it directly. The export rather than the configuration, so the image
# is built with what the stack derives from it; checked against the configuration, so an apiUrl
# changed since the last apply is applied before an image is built for it.
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
    stack and the image agree on where the API is, and run this again:

      just infra-up ${STACK}
EOF
  exit 1
fi
if [[ "${API_URL}" == "/" ]]; then
  echo "    the bundle will call its own origin; nginx proxies /api/ and /health/ to ${UPSTREAM}"
else
  echo "    the bundle will call ${API_URL}"
fi

echo "==> Checking the application is committed"
DIRTY="$(git -C "${APP_DIR}" status --porcelain -- . ':(exclude)infra')"
if [[ -n "${DIRTY}" ]]; then
  echo "    refusing: uncommitted changes in the application would ship under a commit they are not in:" >&2
  printf '%s\n' "${DIRTY}" | sed 's/^/      /' >&2
  exit 1
fi
COMMIT="$(git -C "${APP_DIR}" rev-parse HEAD)"

# The repository and the image name are the ones CD pushes to: <image_repo>/<service>:<commit>. The
# service does not exist yet, so its name is read off the registry repository, which the stack names
# identically — tests/stack.test.ts holds the two together.
IMAGE="${IMAGE_REPO}/${IMAGE_REPO##*/}:${COMMIT}"
echo "    ${IMAGE}"

echo "==> Build and push"
gcloud auth configure-docker "${IMAGE_REPO%%/*}" --quiet
# linux/amd64, because that is what Cloud Run runs. The Dockerfile's build stage runs on this
# machine's own platform, so Node is native here; only the nginx stage is amd64, and it only copies.
docker buildx build \
  --platform linux/amd64 \
  -f "${APP_DIR}/docker/Dockerfile.prod" \
  --target production \
  --build-arg "VITE_API_URL=${API_URL}" \
  -t "${IMAGE}" \
  --push \
  "${APP_DIR}"

echo "==> Create the service on it"
pulumi -C "${PULUMI_DIR}" config set --plaintext serviceImage "${IMAGE}" --stack "${STACK}"
pulumi -C "${PULUMI_DIR}" up --stack "${STACK}" --refresh ${FLAGS[@]+"${FLAGS[@]}"}

# `pulumi up` returns once Cloud Run reports the new revision ready, so the service is serving
# when this runs. The same requests CD's smoke test makes.
echo "==> Smoke test"
SITE_URL="$(read_output site_url || true)"
[[ -n "${SITE_URL}" ]] || { echo "    the stack has no site_url output after the apply" >&2; exit 1; }
for path in / /some/deep/route; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "${SITE_URL}${path}")
  [[ "${code}" == "200" ]] || { echo "    FAILED  ${SITE_URL}${path} answered ${code}" >&2; exit 1; }
  echo "    ${SITE_URL}${path} 200"
done
# The API through the site's nginx, when the bundle calls it there. JSON and not the index: a path
# the proxy missed is the app's shell with a 200, which a status check alone would pass.
if [[ "${API_URL}" == "/" ]]; then
  answer=$(curl -s -o /dev/null -w '%{http_code} %{content_type}' "${SITE_URL}/health/live")
  [[ "${answer}" == "200 application/json"* ]] || {
    echo "    FAILED  ${SITE_URL}/health/live answered '${answer}', not the API's 200 JSON: is the API up?" >&2
    exit 1
  }
  echo "    ${SITE_URL}/health/live 200, from the API"
fi

cat <<EOF

Live: ${SITE_URL}

Next, so CD owns every deploy from here:

  just infra-sync-github ${STACK}
  git add infra/pulumi/Pulumi.${STACK}.yaml && git commit -m "The service runs its first image"
  git push
EOF
