#!/usr/bin/env bash
# The first deploy: build the real image from this checkout, push it, create the Cloud Run service
# on it{% if cookiecutter.use_postgres == "yes" %}, run the migration job{% endif %} and smoke-test the URL. Once per stack, after `just infra-up`{% if cookiecutter.use_postgres == "yes" %} and
# `just infra-secrets`{% endif %}:
#
#   ./infra/scripts/first-deploy.sh [stack] [pulumi up flags]   (just infra-first-deploy [stack] [--yes])
#
# Why it exists: there is no placeholder image. A stack that has not had its first deploy has a
# registry, service accounts{% if cookiecutter.use_postgres == "yes" %}, a database{% endif %} and Workload Identity, and no service, because Cloud Run
# will not create one without an image and nothing real has been built yet. This builds it — the
# production target of backend/docker/Dockerfile.prod, for linux/amd64, at the path and tag CD
# pushes to and with the build argument CD passes — and records it as `serviceImage`, so the
# service is created on the real build. From then on CD owns the image, and the CI account stays
# exactly as narrow as it was.
#
# The apply prompts, as `just infra-up` does, and refuses to run unattended without `--yes`. The
# flags after the stack name reach `pulumi up` unchanged: `just infra-first-deploy dev --yes`.
set -euo pipefail

STACK="dev"
if [[ $# -gt 0 && "$1" != -* ]]; then
  STACK="$1"
  shift
fi
FLAGS=("$@")

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"
COMPONENT_DIR="$(cd "${HERE}/../.." && pwd)"
BACKEND_DIR="${COMPONENT_DIR}/backend"

for tool in gcloud docker git pulumi uv curl; do
  command -v "${tool}" >/dev/null || { echo "${tool} is not installed" >&2; exit 1; }
done

output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null || true
}

config_get() {
  pulumi -C "${PULUMI_DIR}" config get "$1" --stack "${STACK}" 2>/dev/null || true
}

# --- Refuse anything that is not a first deploy, before building anything ---------------------

IMAGE_REPO="$(output image_repo)"
if [[ -z "${IMAGE_REPO}" ]]; then
  echo "stack '${STACK}' exports no image_repo: it has not been applied. Run 'just infra-up ${STACK}' first." >&2
  exit 1
fi
if [[ "$(output secrets_ready)" != "true" ]]; then
  cat >&2 <<EOF
stack '${STACK}' reports secrets_ready != true: a secret the service or its migration needs has
no pinned version, and a service created now would refuse to start. Run this first:

  just infra-secrets ${STACK}
EOF
  exit 1
fi
if [[ -n "$(output service_url)" ]]; then
  cat >&2 <<EOF
stack '${STACK}' already has a service, at $(output service_url).
The first deploy has happened; CD owns the image from here. Un-gate it and push:

  just infra-sync-github ${STACK}
EOF
  exit 1
fi

PROJECT="$(config_get project)"
REGION="$(config_get region)"
SLUG="$(config_get slug)"
ENVIRONMENT="$(config_get environment)"
# The service's name, exactly as the stack names it — `StackConfig.name(slug)` in config.py — and
# so the image path CD pushes to: `<image_repo>/<service>:<sha>`. A test in infra/pulumi/tests
# asserts the two agree.
SERVICE="${SLUG}-${ENVIRONMENT}"

# The image is named for a commit, so it has to be one: an uncommitted change would ship under a
# SHA that does not contain it, and nothing could ever rebuild what is running. infra/ is the
# exception — `stack init` and `config set` change Pulumi.<stack>.yaml as a matter of course, and
# nothing under infra/ is in the image.
if [[ -n "$(git -C "${COMPONENT_DIR}" status --porcelain -- . ':(exclude)infra')" ]]; then
  echo "uncommitted changes under ${COMPONENT_DIR}; commit them first — the image is tagged with the commit:" >&2
  git -C "${COMPONENT_DIR}" status --short -- . ':(exclude)infra' >&2
  exit 1
fi
[[ -f "${BACKEND_DIR}/uv.lock" ]] || {
  echo "backend/uv.lock is missing: run '(cd backend && uv lock)', commit it, and run this again" >&2
  exit 1
}
SHA="$(git -C "${COMPONENT_DIR}" rev-parse HEAD)"
IMAGE="${IMAGE_REPO}/${SERVICE}:${SHA}"

echo "stack   = ${STACK}"
echo "project = ${PROJECT}"
echo "image   = ${IMAGE}"
echo

# --- 1. Build and push the production image -------------------------------------------------

echo "==> Building and pushing ${IMAGE}"
gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet
# linux/amd64, because that is what Cloud Run runs, whatever this machine is. The target, the build
# argument and the context are the ones CD uses, so the first image is the image CD would have
# built from this commit.
docker buildx build \
  --platform linux/amd64 \
  --file "${BACKEND_DIR}/docker/Dockerfile.prod" \
  --target production \
  --build-arg "GIT_SHA=${SHA}" \
  --tag "${IMAGE}" \
  --push \
  "${BACKEND_DIR}"

# --- 2. Create the service on it ------------------------------------------------------------
#
# The apply waits for the service's first revision to be ready, which on Cloud Run means its
# startup probe — liveness — passed.

echo
echo "==> Applying with serviceImage=${IMAGE}"
pulumi -C "${PULUMI_DIR}" config set --plaintext serviceImage "${IMAGE}" --stack "${STACK}"
(cd "${PULUMI_DIR}" && uv run pulumi up --stack "${STACK}" --refresh ${FLAGS[@]+"${FLAGS[@]}"})

URL="$(output service_url)"
[[ -n "${URL}" ]] || { echo "the apply created no service" >&2; exit 1; }
{%- if cookiecutter.use_postgres == "yes" %}

# --- 3. Migrate, exactly as CD does ---------------------------------------------------------
#
# The job was created on this image, so there is nothing to update first. The service's first
# revision is already serving, and until this finishes it is live but not ready: `/health/ready`
# reports the tables missing.

echo
echo "==> Migrating (the service is live but not ready until this finishes)"
JOB="$(output cloud_run_migrate_job)"
gcloud run jobs execute "${JOB}" --region "${REGION}" --project "${PROJECT}" --wait
echo "migrated"
{%- endif %}

# --- {% if cookiecutter.use_postgres == "yes" %}4{% else %}3{% endif %}. Smoke test -------------------------------------------------------------------
#
# Both probes, and both have to pass: unlike a later deploy, nothing this service depends on can
# still be on its way — the first deploy is what brought it up.

echo
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
curl -fsS "${URL}/health/live" >/dev/null
echo "live:  ${URL}/health/live"
code="$(curl -s -o "${WORK}/ready.json" -w '%{http_code}' "${URL}/health/ready")"
if [[ "${code}" != "200" ]]; then
  cat "${WORK}/ready.json" >&2
  echo "${URL}/health/ready answered ${code}" >&2
  exit 1
fi
echo "ready: ${URL}/health/ready"

cat <<EOF

The service is live at ${URL}, running ${IMAGE}.

Next, hand the image to CD — copy the stack's outputs into the repository variables that un-gate
it, then push:

  just infra-sync-github ${STACK}
EOF
