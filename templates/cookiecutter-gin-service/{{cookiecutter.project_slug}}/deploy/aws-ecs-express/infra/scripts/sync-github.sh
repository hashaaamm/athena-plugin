#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates cd.yml.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. GitHub's
# OIDC provider means CI holds no credential at all — a workflow exchanges GitHub's own token for a
# short-lived AWS session, and the role's trust policy says which workflow may. Every value below
# is a public identifier: an account id, a region, a role ARN, a repository URL, a subnet id. None
# of them grants anything alone.
#
# The only credentials in this system live in Secrets Manager, are read by the execution role, and
# never reach GitHub.
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

    pulumi -C "${PULUMI_DIR}" config set githubSubjectPrefix '${EXPECTED_PREFIX}' --stack "${STACK}"
    pulumi -C "${PULUMI_DIR}" up --refresh --stack "${STACK}"
EOF
  exit 1
fi
echo "  ok    CI roles trust ${TRUSTED_PREFIX:-${EXPECTED_PREFIX}}:…"

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/cd.yml and infra.yml exactly. infra/pulumi/contract_test.go asserts that it
# does, because the failure otherwise is a deploy job that is skipped forever and never says why.
#
# Every one of these exists the moment `pulumi up` has run. An empty value is a stack that has not
# been applied, so it stops the script rather than writing a blank into a pipeline.
REQUIRED=(
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

# Empty is the normal state for each of these: a project with no database has no migration task.
# An empty output DELETES the variable rather than setting it. Every workflow step that uses one is gated on it being
# non-empty, so a deleted variable is a step that quietly does not run — while a stale value left
# behind after the feature is switched off would send every deploy at a resource that no longer
# exists.
OPTIONAL=(
  "ecs_migrate_task_family:ECS_MIGRATE_TASK_FAMILY"
)

for entry in "${REQUIRED[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'pulumi up' run for stack '${STACK}'?" >&2
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

Done. AWS_DEPLOY_ROLE_ARN is now set, which un-gates .github/workflows/cd.yml, and
AWS_PREVIEW_ROLE_ARN, which un-gates the preview in infra.yml.

cd.yml runs in the GitHub environment 'production', and the deploy role trusts a token from that
environment and no other. Give the environment protection rules — required reviewers, or a branch
restriction to main — in the repository settings: they are what stands between a push and a deploy.

Two things this script cannot do, because their values come from outside the stack:

  * SENTRY_ORG and SENTRY_PROJECT, plus the SENTRY_AUTH_TOKEN repository *secret*, if you want a
    release marker on every deploy.
  * The value inside any Secrets Manager secret the stack created empty. Add one with:

      aws secretsmanager put-secret-value --region $(read_output aws_region) \\
        --secret-id <secret-name> --secret-string '<value>'

    then put the VersionId it prints into infra/pulumi/Pulumi.${STACK}.yaml (for the Sentry DSN,
    sentryDsnVersion) and run \`just infra-up ${STACK}\`. The service pins that version, so a new
    value is always a deploy, never a surprise.

EOF
