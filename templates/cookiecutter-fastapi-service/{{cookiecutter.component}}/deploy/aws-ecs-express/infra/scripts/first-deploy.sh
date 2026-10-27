#!/usr/bin/env bash
# The first deploy: build the real image from this checkout, push it, create the Express service on
# it{% if cookiecutter.use_postgres == "yes" %}, run the migrations{% endif %}, wait for the rollout and smoke-test the URL. Once per stack, after `just
# infra-up`{% if cookiecutter.use_postgres == "yes" %} and `just infra-secrets`{% endif %}:
#
#   ./infra/scripts/first-deploy.sh [stack] [pulumi up flags]   (just infra-first-deploy [stack] [--yes])
#
# Why it exists: there is no placeholder image. A stack that has not had its first deploy has a
# registry, roles, a cluster{% if cookiecutter.use_postgres == "yes" %} and a database{% endif %}, and no service, because Express Mode will not create one
# without an image and nothing real has been built yet. This builds it — the production target of
# backend/docker/Dockerfile.prod, for linux/amd64, tagged with the commit and with the build
# argument CD passes — and records it as `serviceImage`, so the service is created on the real
# build. From then on CD owns the image, and the deploy role stays exactly as narrow as it was.
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

for tool in aws docker git jq pulumi uv curl; do
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
stack '${STACK}' reports secrets_ready != true: a secret the service needs has no pinned version,
and a service created now would refuse to start. Run this first:

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

ACCOUNT="$(config_get accountId)"
REGION="$(config_get region)"
# The same guard as the bootstrap script and the provider: a shell on another account would push an
# image into somebody else's registry, or fail on one that is not there.
CALLER="$(aws sts get-caller-identity --query Account --output text)"
if [[ "${CALLER}" != "${ACCOUNT}" ]]; then
  echo "this shell is on account ${CALLER}; stack '${STACK}' belongs to ${ACCOUNT}" >&2
  exit 1
fi
export AWS_REGION="${REGION}" AWS_DEFAULT_REGION="${REGION}"

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
IMAGE="${IMAGE_REPO}:${SHA}"

echo "stack   = ${STACK}"
echo "account = ${ACCOUNT}"
echo "image   = ${IMAGE}"
echo

# --- 1. Build and push the production image -------------------------------------------------

# The repository refuses to move a tag, so a run that already pushed this commit reuses it.
if aws ecr describe-images --repository-name "${IMAGE_REPO#*/}" --image-ids "imageTag=${SHA}" >/dev/null 2>&1; then
  echo "==> ${IMAGE} is already in the registry"
else
  echo "==> Building and pushing ${IMAGE}"
  aws ecr get-login-password | docker login --username AWS --password-stdin "${IMAGE_REPO%%/*}"
  # linux/amd64, because that is what Fargate runs here, whatever this machine is. The target, the
  # build argument and the context are the ones CD uses, so the first image is the image CD would
  # have built from this commit.
  docker buildx build \
    --platform linux/amd64 \
    --file "${BACKEND_DIR}/docker/Dockerfile.prod" \
    --target production \
    --build-arg "GIT_SHA=${SHA}" \
    --tag "${IMAGE}" \
    --push \
    "${BACKEND_DIR}"
fi

# --- 2. Create the service on it ------------------------------------------------------------

echo
echo "==> Applying with serviceImage=${IMAGE}"
pulumi -C "${PULUMI_DIR}" config set --plaintext serviceImage "${IMAGE}" --stack "${STACK}"
(cd "${PULUMI_DIR}" && uv run pulumi up --stack "${STACK}" --refresh ${FLAGS[@]+"${FLAGS[@]}"})

CLUSTER="$(output ecs_cluster)"
SERVICE="$(output ecs_service)"
SERVICE_ARN="$(output ecs_service_arn)"
URL="$(output service_url)"
[[ -n "${SERVICE_ARN}" && -n "${URL}" ]] || { echo "the apply created no service" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
{%- if cookiecutter.use_postgres == "yes" %}

# --- 3. Migrate, exactly as CD does ---------------------------------------------------------
#
# The service's first tasks are already starting, and until this finishes they are live but not
# ready: `/health/ready` reports the database's role and tables missing. The rollout waits on
# liveness, so they are not replaced for it.

echo
echo "==> Migrating (the service's first tasks are live but not ready until this finishes)"
FAMILY="$(output ecs_migrate_task_family)"
SUBNETS="$(output ecs_task_subnets)"
SECURITY_GROUP="$(output ecs_task_security_group)"

aws ecs describe-task-definition --task-definition "${FAMILY}" \
  --query taskDefinition --output json > "${WORK}/current.json"
# Read-only fields describe returns and register refuses. Everything else is sent back exactly as
# the stack declared it — the same revision CD registers on every deploy.
jq --arg image "${IMAGE}" '
  del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities,
      .registeredAt, .registeredBy, .deregisteredAt)
  | .containerDefinitions[0].image = $image
' "${WORK}/current.json" > "${WORK}/revision.json"
TASK_DEFINITION="$(aws ecs register-task-definition --cli-input-json "file://${WORK}/revision.json" \
  --query taskDefinition.taskDefinitionArn --output text)"
echo "registered ${TASK_DEFINITION}"

TASK="$(aws ecs run-task --cluster "${CLUSTER}" --launch-type FARGATE \
  --task-definition "${TASK_DEFINITION}" \
  --network-configuration "awsvpcConfiguration={subnets=[${SUBNETS}],securityGroups=[${SECURITY_GROUP}],assignPublicIp=ENABLED}" \
  --query 'tasks[0].taskArn' --output text)"
if [[ -z "${TASK}" || "${TASK}" == "None" ]]; then
  echo "run-task started nothing — see the failures above" >&2
  exit 1
fi
echo "started ${TASK}"

aws ecs wait tasks-stopped --cluster "${CLUSTER}" --tasks "${TASK}"

aws ecs describe-tasks --cluster "${CLUSTER}" --tasks "${TASK}" --output json > "${WORK}/stopped.json"
EXIT_CODE="$(jq -r '.tasks[0].containers[0].exitCode // "none"' "${WORK}/stopped.json")"
if [[ "${EXIT_CODE}" != "0" ]]; then
  echo "migration exited ${EXIT_CODE}. $(jq -r '.tasks[0].stoppedReason // ""' "${WORK}/stopped.json")" >&2
  GROUP="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-group"]' "${WORK}/revision.json")"
  PREFIX="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-stream-prefix"]' "${WORK}/revision.json")"
  CONTAINER="$(jq -r '.containerDefinitions[0].name' "${WORK}/revision.json")"
  aws logs get-log-events --log-group-name "${GROUP}" \
    --log-stream-name "${PREFIX}/${CONTAINER}/${TASK##*/}" \
    --query 'events[].message' --output text | tr '\t' '\n' >&2 || true
  exit 1
fi
echo "migrated"
{%- endif %}

# --- {% if cookiecutter.use_postgres == "yes" %}4{% else %}3{% endif %}. Wait for the rollout, as CD does --------------------------------------------------
#
# On the deployment's own rollout state, not on the services-stable waiter: an Express deployment
# is a canary with a bake time that has outlasted that waiter's ten minutes, and the rollout state
# reads FAILED the moment ECS rolls back. There is no earlier deployment to tell this one from.

echo
echo "==> Waiting for ${SERVICE}'s first rollout (a canary with a bake time: often ten minutes)"
DEADLINE=$((SECONDS + 1800))
while :; do
  DEPLOYMENTS="$(aws ecs describe-services --cluster "${CLUSTER}" --services "${SERVICE}" \
    --query 'services[0].deployments[].[id, status, rolloutState]' --output text)"
  if awk '$3 == "FAILED"' <<<"${DEPLOYMENTS}" | grep -q .; then
    aws ecs describe-services --cluster "${CLUSTER}" --services "${SERVICE}" \
      --query 'services[0].events[:5].message' --output text | tr '\t' '\n' >&2
    echo "${SERVICE}'s first deployment failed" >&2
    exit 1
  fi
  if awk '$2 == "PRIMARY" && $3 == "COMPLETED"' <<<"${DEPLOYMENTS}" | grep -q .; then
    break
  fi
  if [[ "${SECONDS}" -ge "${DEADLINE}" ]]; then
    echo "${SERVICE}'s rollout did not finish in 30 minutes" >&2
    exit 1
  fi
  sleep 20
done
aws ecs describe-express-gateway-service --service-arn "${SERVICE_ARN}" --output json |
  jq -e --arg image "${IMAGE}" \
    '[.service.activeConfigurations[].primaryContainer.image] | index($image) != null' \
    >/dev/null || { echo "${SERVICE} is stable but not running ${IMAGE}" >&2; exit 1; }

# --- {% if cookiecutter.use_postgres == "yes" %}5{% else %}4{% endif %}. Smoke test -------------------------------------------------------------------
#
# Both probes, and both have to pass: unlike a later deploy, nothing this service depends on can
# still be on its way — the first deploy is what brought it up.

echo
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
