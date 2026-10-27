#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates cd.yml.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. CI holds no
# AWS credential at all — it exchanges GitHub's own OIDC token for a short-lived session on a role
# that trusts exactly this repository, and every value below is a public identifier: an account id,
# a region, a role ARN, a cluster and a service name. None of them grants anything alone.
#
# The only credentials in this system live in Secrets Manager, are read by the ECS execution role
# when a task starts, and never reach GitHub.
#
# Run after `pulumi up`, from anywhere:
#
#   ./infra/scripts/sync-github.sh [stack]
#
set -euo pipefail

STACK="${1:-dev}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PULUMI_DIR="${HERE}/../pulumi"
REPO="${GITHUB_REPOSITORY:-{{ cookiecutter.github_repository }}}"

command -v gh >/dev/null || { echo "gh is not installed"; exit 1; }
command -v pulumi >/dev/null || { echo "pulumi is not installed"; exit 1; }
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

    pulumi -C "${PULUMI_DIR}" config set githubSubjectPrefix '${EXPECTED_PREFIX}' --stack "${STACK}"
    just infra-up ${STACK}
EOF
  exit 1
fi
echo "  ok    CI roles trust ${TRUSTED_PREFIX:-${EXPECTED_PREFIX}}:…"

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/ exactly; a unit test asserts that it does, because the failure otherwise is a
# deploy job that is skipped forever and never says why.
#
# Required: every stack exports these with a value, so an empty one means the stack has not been
# applied, and the script stops rather than half-configuring the pipeline.
declare -a REQUIRED=(
  "aws_account_id:AWS_ACCOUNT_ID"
  "aws_region:AWS_REGION"
  "image_repo:IMAGE_REPO"
  "deploy_role_arn:AWS_DEPLOY_ROLE_ARN"
  "preview_role_arn:AWS_PREVIEW_ROLE_ARN"
  "ecs_cluster:ECS_CLUSTER"
  "ecs_service:ECS_SERVICE"
  "ecs_service_arn:ECS_SERVICE_ARN"
  "service_url:SERVICE_URL"
  "ecs_task_subnets:ECS_TASK_SUBNETS"
  "ecs_task_security_group:ECS_TASK_SECURITY_GROUP"
)

# Optional: exported empty when the thing does not exist — no database means no migration task. An
# empty one is *deleted* rather than skipped or set empty: a stale value left behind after the thing
# was removed would send every deploy at something that no longer exists, and the workflows gate on
# these being non-empty.
declare -a OPTIONAL=(
  "ecs_migrate_task_family:ECS_MIGRATE_TASK_FAMILY"
)

# Exported and deliberately never synced. No workflow acts as the application, so the runtime role
# has no business in CI's configuration, and the database identifier is for a human at a terminal.
# Listed so the contract test can tell "deliberately not synced" from "forgotten".
# shellcheck disable=SC2034
declare -a NOT_SYNCED=(runtime_role_arn rds_instance github_subject_prefix)

for entry in "${REQUIRED[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'pulumi up' run for stack '${STACK}'?" >&2
    exit 1
  fi
  gh variable set "${variable}" --repo "${REPO}" --body "${value}"
  echo "  set ${variable} = ${value}"
done

for entry in "${OPTIONAL[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -n "${value}" ]]; then
    gh variable set "${variable}" --repo "${REPO}" --body "${value}"
    echo "  set ${variable} = ${value}"
  else
    # Not an error when it was never set: deleting a variable that does not exist is the steady
    # state of a project without that component.
    gh variable delete "${variable}" --repo "${REPO}" >/dev/null 2>&1 || true
    echo "  deleted ${variable} (${output} is empty)"
  fi
done

cat <<EOF

Done. AWS_DEPLOY_ROLE_ARN is now set, which un-gates .github/workflows/cd.yml, and
AWS_PREVIEW_ROLE_ARN un-gates the preview in infra.yml.

Two things this script cannot do, because their values come from outside the stack:

  * SENTRY_ORG and SENTRY_PROJECT, plus the SENTRY_AUTH_TOKEN repository *secret*, if you want a
    release marker on every deploy.
  * The value inside any Secrets Manager secret the stack created empty. Add one, then pin the
    version it prints and apply, so the service is redeployed reading exactly that version:

      aws secretsmanager put-secret-value --secret-id <secret-name> --secret-string '<value>' \\
        --region \$(pulumi -C infra/pulumi stack output aws_region --stack ${STACK}) \\
        --query VersionId --output text
      # put the printed VersionId in infra/pulumi/Pulumi.${STACK}.yaml (e.g. sentryDsnVersion)
      just infra-up ${STACK}

EOF
