#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates
# .github/workflows/{{ cookiecutter.component }}-cd.yml. Every variable is prefixed {{ cookiecutter.__component_var }}_, the component's name, so two
# components in one repository never write each other's.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. GitHub's
# OIDC provider means CI holds no credential at all — a workflow exchanges GitHub's own token for a
# short-lived AWS session, and the role's trust policy says which workflow may. Every value below
# is a public identifier: an account id, a region, a role ARN, a repository URL, a subnet id. None
# of them grants anything alone.
#
# The only credentials in this system live in Secrets Manager, are read by the ECS execution roles,
# and never reach GitHub.
#
# Run after `just infra-first-deploy`, from anywhere:
#
#   ./infra/scripts/sync-github.sh [stack]
#
set -euo pipefail

STACK="${1:-dev}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"
REPO="${GITHUB_REPOSITORY:-{{ cookiecutter.github_repository }}}"

command -v pulumi >/dev/null || { echo "pulumi is not installed"; exit 1; }
command -v gh >/dev/null || { echo "gh is not installed"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh is not authenticated: run 'gh auth login'"; exit 1; }

echo "stack = ${STACK}"
echo "repo  = ${REPO}"
echo

read_output() {
  pulumi -C "${PULUMI_DIR}" stack output "$1" --stack "${STACK}" 2>/dev/null
}

# The CI roles trust one exact `sub` claim, and GitHub decides its shape per repository: the
# name-only `repo:<owner>/<repo>`, or the immutable `repo:<owner>@<id>/<repo>@<id>` it now issues for
# many. A mismatch is a pipeline that fails to log in on every run, with "Not authorized to perform
# sts:AssumeRoleWithWebIdentity" and no hint why — so it is checked here, before CD is un-gated.
EXPECTED_PREFIX="$(gh api "repos/${REPO}/actions/oidc/customization/sub" --jq '.sub_claim_prefix // empty' 2>/dev/null || true)"
EXPECTED_PREFIX="${EXPECTED_PREFIX:-repo:${REPO}}"
TRUSTED_PREFIX="$(read_output github_subject_prefix || true)"
if [[ -n "${TRUSTED_PREFIX}" && "${TRUSTED_PREFIX}" != "${EXPECTED_PREFIX}" ]]; then
  cat >&2 <<EOF
  MISMATCH  the CI roles trust tokens whose subject starts '${TRUSTED_PREFIX}',
            but GitHub issues '${EXPECTED_PREFIX}' for ${REPO}.

  Set it and apply, then run this script again:

    pulumi -C "${PULUMI_DIR}" config set --plaintext githubSubjectPrefix '${EXPECTED_PREFIX}' --stack "${STACK}"
    pulumi -C "${PULUMI_DIR}" up --refresh --stack "${STACK}"
EOF
  exit 1
fi
echo "  ok    CI roles trust ${TRUSTED_PREFIX:-${EXPECTED_PREFIX}}:…"

# Setting {{ cookiecutter.__component_var }}_AWS_DEPLOY_ROLE_ARN un-gates CD, and CD deploys an image that refuses to start without
# the secrets it reads. The stack mounts each one only once its VersionId is in stack
# configuration, and reports whether every one is as secrets_ready — so this stops here, before
# setting anything, rather than turning the first merge into a deployment that rolls back.
READY="$(read_output secrets_ready || true)"
if [[ "${READY}" != "true" ]]; then
  cat >&2 <<EOF
  NOT READY  secrets_ready is '${READY:-unset}' for stack '${STACK}': the service would deploy
             without a secret it cannot start without.

  Generate them, apply the stack so it mounts the pinned versions, then run this script again:

    just infra-secrets ${STACK}
    just infra-up ${STACK}
EOF
  exit 1
fi
echo "  ok    every required secret has a pinned version"

# CD updates the service's image; it cannot create the service. Before the first deploy there is
# no service — `just infra-up` creates everything else — so un-gating CD now would send its first
# run at a service that does not exist.
SERVICE_URL="$(read_output service_url || true)"
if [[ -z "${SERVICE_URL}" ]]; then
  cat >&2 <<EOF
  NO SERVICE  service_url is empty for stack '${STACK}': the service is created by the first deploy,
              which builds the real image on this machine and creates the service on it.

  Run it, then run this script again:

    just infra-first-deploy ${STACK}
EOF
  exit 1
fi
echo "  ok    the service exists: ${SERVICE_URL}"

# stack output -> repository variable, every name prefixed with the component's, so another
# component's sync never overwrites this one's. The right-hand column must match the `vars.` names in
# .github/workflows/{{ cookiecutter.component }}-cd.yml and {{ cookiecutter.component }}-infra.yml exactly. infra/pulumi/contract_test.go asserts that it
# does, because the failure otherwise is a deploy job that is skipped forever and never says why.
#
# Every one of these exists once the first deploy has run. An empty value is a stack that has not
# been applied, so it stops the script rather than writing a blank into a pipeline.
REQUIRED=(
  "aws_account_id:{{ cookiecutter.__component_var }}_AWS_ACCOUNT_ID"
  "aws_region:{{ cookiecutter.__component_var }}_AWS_REGION"
  "image_repo:{{ cookiecutter.__component_var }}_IMAGE_REPO"
  "deploy_role_arn:{{ cookiecutter.__component_var }}_AWS_DEPLOY_ROLE_ARN"
  "preview_role_arn:{{ cookiecutter.__component_var }}_AWS_PREVIEW_ROLE_ARN"
  "apply_role_arn:{{ cookiecutter.__component_var }}_AWS_APPLY_ROLE_ARN"
  "ecs_cluster:{{ cookiecutter.__component_var }}_ECS_CLUSTER"
  "ecs_service:{{ cookiecutter.__component_var }}_ECS_SERVICE"
  "ecs_service_arn:{{ cookiecutter.__component_var }}_ECS_SERVICE_ARN"
  "service_url:{{ cookiecutter.__component_var }}_SERVICE_URL"
  "ecs_task_subnets:{{ cookiecutter.__component_var }}_ECS_TASK_SUBNETS"
  "ecs_task_security_group:{{ cookiecutter.__component_var }}_ECS_TASK_SECURITY_GROUP"
)

# Empty is the normal state for each of these: a project with no database has no migration task.
# An empty output DELETES the variable rather than setting it. Every workflow step that uses one is gated on it being
# non-empty, so a deleted variable is a step that quietly does not run — while a stale value left
# behind after the feature is switched off would send every deploy at a resource that no longer
# exists.
OPTIONAL=(
  "ecs_migrate_task_family:{{ cookiecutter.__component_var }}_ECS_MIGRATE_TASK_FAMILY"
)

for entry in "${REQUIRED[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'just infra-first-deploy ${STACK}' run?" >&2
    exit 1
  fi
  gh variable set "${variable}" --repo "${REPO}" --body "${value}"
  echo "  set   ${variable} = ${value}"
done

for entry in "${OPTIONAL[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -n "${value}" ]]; then
    gh variable set "${variable}" --repo "${REPO}" --body "${value}"
    echo "  set   ${variable} = ${value}"
  elif gh variable get "${variable}" --repo "${REPO}" >/dev/null 2>&1; then
    gh variable delete "${variable}" --repo "${REPO}"
    echo "  unset ${variable} (the stack has none)"
  else
    echo "  skip  ${variable} (the stack has none)"
  fi
done

cat <<EOF

Done. {{ cookiecutter.__component_var }}_AWS_DEPLOY_ROLE_ARN is now set, which un-gates .github/workflows/{{ cookiecutter.component }}-cd.yml,
{{ cookiecutter.__component_var }}_AWS_PREVIEW_ROLE_ARN, which un-gates the preview in {{ cookiecutter.component }}-infra.yml, and {{ cookiecutter.__component_var }}_AWS_APPLY_ROLE_ARN, which
un-gates its apply. From the next push to main, CD builds and deploys every image, and every
infrastructure change that merges is applied by {{ cookiecutter.component }}-infra.yml. The applies so far ran from this
machine because the apply role did not exist yet; one from a laptop now is the exception Pulumi
standards MUST-9 forbids.

Before that push, give two GitHub environments protection rules in the repository settings. Each
is trusted by one role and no other, and its rules are what stands between a merge and a change:

  * 'production' — {{ cookiecutter.component }}-cd.yml's deploy job. Required reviewers, or a branch restriction to main.
  * 'infrastructure' — {{ cookiecutter.component }}-infra.yml's apply job, whose role can change anything in the account.
    Required reviewers, and deployment branches limited to main.

Two things this script cannot do, because their values come from outside the stack:

  * {{ cookiecutter.__component_var }}_SENTRY_ORG and {{ cookiecutter.__component_var }}_SENTRY_PROJECT, plus the {{ cookiecutter.__component_var }}_SENTRY_AUTH_TOKEN repository
    *secret*, if you want a release marker on every deploy.
  * The value of a secret that originates elsewhere, such as the Sentry DSN. Put it from stdin,
    so it is in no file and no shell history:

      aws secretsmanager put-secret-value --region $(read_output aws_region) \\
        --secret-id <secret-name> --secret-string file:///dev/stdin \\
        --query VersionId --output text

    then put the VersionId it prints into infra/pulumi/Pulumi.${STACK}.yaml (for the Sentry DSN,
    sentryDsnVersion) and merge that change: {{ cookiecutter.component }}-infra.yml applies it. The service pins that
    version, so a new value is always a deploy, never a surprise.

EOF
