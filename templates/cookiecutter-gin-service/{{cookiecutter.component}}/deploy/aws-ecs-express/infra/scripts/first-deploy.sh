#!/usr/bin/env bash
# The first deploy: build the real image on this machine, push it, and create the service on it.
#
# There is no placeholder image (ADR-0024). `just infra-up` creates everything the service needs —
# the registry, the roles, the network{% if cookiecutter.use_postgres == "yes" %}, the database{% endif %} — and not the service, because ECS will not
# create one without an image and the registry is empty until something pushes to it. CD cannot be
# that something: it updates a service and cannot create one. So the first image is built here, and
# after it CD owns every image, as it always has; the deploy role stays as narrow as it was.
#
# In order, each step the one CD runs on every later deploy wherever there is one:
#
#   1. refuse unless the stack has been applied, its secrets are recorded, the service does not
#      exist yet, and the component's files outside infra/ are committed — the image is tagged with the
#      commit, and a tag has to name what was built;
#   2. build the production image for linux/amd64 — the platform Fargate runs — with the build
#      arguments CD uses, and push it to the stack's ECR repository as <image_repo>:<git sha>;
#   3. record it as `serviceImage` and apply the stack, which creates the Express service{% if cookiecutter.use_postgres == "yes" %} and the
#      migration task definition{% endif %} on it. `pulumi up` asks for confirmation, as `just infra-up` does;
#      `just infra-first-deploy dev --yes` is the unattended form;
{%- if cookiecutter.use_postgres == "yes" %}
#   4. run the migrations exactly as CD does: register the image into the migration task family,
#      run the task, wait for it and read its exit code. The service's first tasks start alongside
#      and are live but not ready until it finishes — readiness checks the database, and the
#      service's database role is created by this migration;
{%- else %}
#   4. (no database, so no migrations);
{%- endif %}
#   5. follow the service's first rollout to COMPLETED, as CD does, and smoke-test /health/live
#      and /health/ready.
#
# Then `just infra-sync-github`, and push: CD takes it from there.
#
#   ./infra/scripts/first-deploy.sh [stack] [flags for pulumi up, e.g. --yes]
#
set -euo pipefail

STACK="${1:-dev}"
[[ $# -gt 0 ]] && shift

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"
COMPONENT_DIR="$(cd "${HERE}/../.." && pwd)"
BACKEND_DIR="${COMPONENT_DIR}/backend"

for tool in pulumi aws docker git curl jq; do
  command -v "${tool}" >/dev/null || { echo "${tool} is not installed" >&2; exit 1; }
done

read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null || true
}
config() {
  pulumi -C "${PULUMI_DIR}" config get "$1" --stack "${STACK}" 2>/dev/null || true
}

# --- 1. Refuse anything that is not a first deploy of a committed tree --------------------------

IMAGE_REPO="$(read_output image_repo)"
if [[ -z "${IMAGE_REPO}" ]]; then
  cat >&2 <<EOF
  NOT APPLIED  image_repo is empty for stack '${STACK}': there is no registry to push to yet.

    just infra-up ${STACK}
EOF
  exit 1
fi

if [[ -n "$(read_output service_url)" ]]; then
  cat >&2 <<EOF
  DEPLOYED  stack '${STACK}' already has its service. Every image after the first is CD's: run
            'just infra-sync-github ${STACK}' if you have not, and push to main.

            To redo a first deploy that went wrong, remove the service first —
            'pulumi -C ${PULUMI_DIR} config rm serviceImage --stack ${STACK}' and 'just infra-up ${STACK}'
            — and run this again.
EOF
  exit 1
fi

# The secrets the service cannot start without. secrets_ready is the stack's own answer, but it is
# an output, so it is only as new as the last apply — and `just infra-secrets` records versions in
# configuration without applying. So the configuration is read here, and the output is checked
# after this script's own apply.
REQUIRED_SECRET_VERSIONS=({% if cookiecutter.use_postgres == "yes" %}jwtSecretVersion{% endif %})
missing=()
for key in ${REQUIRED_SECRET_VERSIONS[@]+"${REQUIRED_SECRET_VERSIONS[@]}"}; do
  [[ -n "$(config "${key}")" ]] || missing+=("${key}")
done
if [[ -n "${missing[*]-}" ]]; then
  cat >&2 <<EOF
  NOT READY  ${missing[*]} not recorded for stack '${STACK}': the service would refuse to start.

    just infra-secrets ${STACK}
EOF
  exit 1
fi

# The tag is the commit, so the commit has to be what is built. Untracked files count: a go.sum
# nobody committed is the most likely one, and the image would build from it. infra/ is left out:
# `just infra-secrets` and this script record versions and the image in Pulumi.<stack>.yaml, and
# none of it is in the image.
DIRTY="$(git -C "${COMPONENT_DIR}" status --porcelain -- . ":(exclude)infra")"
if [[ -n "${DIRTY}" ]]; then
  echo "  UNCOMMITTED  the image is tagged with the commit, so commit these first:" >&2
  printf '%s\n' "${DIRTY}" | sed 's/^/               /' >&2
  exit 1
fi

SHA="$(git -C "${COMPONENT_DIR}" rev-parse HEAD)"
IMAGE="${IMAGE_REPO}:${SHA}"
REGION="$(read_output aws_region)"
REGISTRY="${IMAGE_REPO%%/*}"
REPOSITORY="${IMAGE_REPO#*/}"

echo "stack  = ${STACK}"
echo "image  = ${IMAGE}"
echo

# --- 2. Build and push --------------------------------------------------------------------------

echo "==> Logging in to ${REGISTRY}"
aws ecr get-login-password --region "${REGION}" |
  docker login --username AWS --password-stdin "${REGISTRY}" >/dev/null

# The repository's tags are immutable, so a commit already pushed — a re-run after a later step
# failed — is used as it is rather than refused.
if aws ecr describe-images --region "${REGION}" --repository-name "${REPOSITORY}" \
  --image-ids imageTag="${SHA}" >/dev/null 2>&1; then
  echo "    ${IMAGE} is already pushed"
else
  # The image CD builds: the production target of the same Dockerfile, for the platform Fargate
  # runs, with the commit as GIT_SHA. The Dockerfile cross-compiles in its builder stage, so this
  # is native speed on an Apple-silicon laptop too.
  echo "==> Building and pushing ${IMAGE}"
  docker buildx build \
    --platform linux/amd64 \
    --target production \
    --file "${BACKEND_DIR}/docker/Dockerfile.prod" \
    --build-arg "GIT_SHA=${SHA}" \
    --tag "${IMAGE}" \
    --push \
    "${BACKEND_DIR}"
fi

# --- 3. Create the service on it ----------------------------------------------------------------

echo "==> Recording serviceImage and applying stack '${STACK}'"
pulumi -C "${PULUMI_DIR}" config set --plaintext serviceImage "${IMAGE}" --stack "${STACK}"
# --refresh, as on every apply here: harmless now, and the habit is what keeps a later apply from
# rolling the service back to this image after CD has moved it on.
pulumi -C "${PULUMI_DIR}" up --stack "${STACK}" --refresh "$@"

if [[ "$(read_output secrets_ready)" != "true" ]]; then
  echo "  NOT READY  the stack applied but reports secrets_ready=false; run 'just infra-secrets ${STACK}'" >&2
  exit 1
fi

CLUSTER="$(read_output ecs_cluster)"
SERVICE="$(read_output ecs_service)"
SERVICE_ARN="$(read_output ecs_service_arn)"
URL="$(read_output service_url)"

# --- 4. Migrate, as CD does ---------------------------------------------------------------------

FAMILY="$(read_output ecs_migrate_task_family)"
if [[ -n "${FAMILY}" ]]; then
  echo "==> Migrating with ${FAMILY} (the service is live, and not ready until this finishes)"
  WORK="$(mktemp -d)"
  trap 'rm -rf "${WORK}"' EXIT

  # The latest ACTIVE revision of the family, the image swapped, registered as the next revision.
  aws ecs describe-task-definition --region "${REGION}" --task-definition "${FAMILY}" \
    --query taskDefinition --output json > "${WORK}/current.json"
  jq --arg image "${IMAGE}" '
    del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities,
        .registeredAt, .registeredBy, .deregisteredAt)
    | .containerDefinitions[0].image = $image
  ' "${WORK}/current.json" > "${WORK}/next.json"
  TASK_DEFINITION="$(aws ecs register-task-definition --region "${REGION}" \
    --cli-input-json "file://${WORK}/next.json" \
    --query taskDefinition.taskDefinitionArn --output text)"
  echo "    registered ${TASK_DEFINITION}"

  # Public IP on: the task subnets have no NAT. The security group is the app group, the one the
  # database admits.
  TASK="$(aws ecs run-task --region "${REGION}" \
    --cluster "${CLUSTER}" \
    --launch-type FARGATE \
    --task-definition "${TASK_DEFINITION}" \
    --network-configuration "awsvpcConfiguration={subnets=[$(read_output ecs_task_subnets)],securityGroups=[$(read_output ecs_task_security_group)],assignPublicIp=ENABLED}" \
    --query 'tasks[0].taskArn' --output text)"
  echo "    started ${TASK}"

  # run-task returns once the task is placed; the exit code is what says the schema is there.
  aws ecs wait tasks-stopped --region "${REGION}" --cluster "${CLUSTER}" --tasks "${TASK}"
  aws ecs describe-tasks --region "${REGION}" --cluster "${CLUSTER}" --tasks "${TASK}" \
    --query 'tasks[0]' --output json > "${WORK}/task.json"
  EXIT_CODE="$(jq -r '.containers[0].exitCode // "none"' "${WORK}/task.json")"
  if [[ "${EXIT_CODE}" != "0" ]]; then
    echo "  FAILED  migration exited with ${EXIT_CODE}: $(jq -r '.stoppedReason // ""' "${WORK}/task.json")" >&2
    GROUP="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-group"]' "${WORK}/next.json")"
    PREFIX="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-stream-prefix"]' "${WORK}/next.json")"
    CONTAINER="$(jq -r '.containerDefinitions[0].name' "${WORK}/next.json")"
    aws logs get-log-events --region "${REGION}" --log-group-name "${GROUP}" \
      --log-stream-name "${PREFIX}/${CONTAINER}/${TASK##*/}" \
      --query 'events[].message' --output text >&2 || true
    exit 1
  fi
  echo "    migrated"
fi

# --- 5. Wait for the rollout, as CD does, and smoke-test ----------------------------------------

# The rollout state of the service's deployment, not the services-stable waiter: an Express
# deployment is a canary with a bake time, and the waiter gives up at ten minutes. FAILED is read
# the moment ECS rolls back.
echo "==> Following ${SERVICE}'s first rollout"
DEADLINE=$((SECONDS + 1800))
while :; do
  # shellcheck disable=SC2016  # the backticks are a JMESPath literal, not command substitution
  DEPLOYMENTS="$(aws ecs describe-services --region "${REGION}" --cluster "${CLUSTER}" --services "${SERVICE}" \
    --query 'services[0].deployments[].[id, status, rolloutState]' --output text)"
  if awk '$3 == "FAILED"' <<<"${DEPLOYMENTS}" | grep -q .; then
    aws ecs describe-services --region "${REGION}" --cluster "${CLUSTER}" --services "${SERVICE}" \
      --query 'services[0].events[:5].message' --output text | tr '\t' '\n' >&2
    echo "  FAILED  ${SERVICE}'s first deployment failed" >&2
    exit 1
  fi
  if awk '$2 == "PRIMARY" && $3 == "COMPLETED"' <<<"${DEPLOYMENTS}" | grep -q .; then
    break
  fi
  if [[ "${SECONDS}" -ge "${DEADLINE}" ]]; then
    echo "  FAILED  ${SERVICE}'s rollout did not finish in 30 minutes" >&2
    exit 1
  fi
  sleep 20
done
aws ecs describe-express-gateway-service --region "${REGION}" --service-arn "${SERVICE_ARN}" --output json |
  jq -e --arg image "${IMAGE}" \
    '[.service.activeConfigurations[].primaryContainer.image] | index($image) != null' >/dev/null ||
  { echo "  FAILED  ${SERVICE} finished but is not running ${IMAGE}" >&2; exit 1; }

# Live, then ready. Both are required here, unlike in CD: the migrations have just run, so a
# service that is not ready now has something wrong with it rather than with a dependency. The
# endpoint's DNS can take a few minutes to answer the first time, hence the retries.
probe() {
  local path="$1" attempt
  for attempt in $(seq 1 30); do
    if curl -fsS -o /dev/null "${URL}${path}"; then
      echo "    ${path} answers"
      return 0
    fi
    [[ "${attempt}" -lt 30 ]] && sleep 10
  done
  echo "  FAILED  ${URL}${path} did not answer 200 in five minutes" >&2
  curl -sS "${URL}${path}" >&2 || true
  return 1
}
echo "==> Smoke-testing ${URL}"
probe /health/live
probe /health/ready

cat <<EOF

The service is up on its first image, at:

  ${URL}

Next:

  just infra-sync-github ${STACK}     copies the stack's outputs into repository variables, un-gating CD
  git push                            CD builds, migrates and deploys every image from here on

EOF
