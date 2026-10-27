#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates cd.yml.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. GitHub's
# OIDC provider means CI holds no credential at all — a workflow exchanges GitHub's own token for a
# short-lived AWS session, and the role's trust policy says which workflow may. Every value below
# is a public identifier: an account id, a region, a bucket name, a distribution id, a role ARN.
# None of them grants anything alone.
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
# .github/workflows/cd.yml and infra.yml exactly. infra/pulumi/tests/contract.test.ts asserts that
# it does, because the failure otherwise is a deploy job that is skipped forever and never says why.
#
# Every one of these exists the moment `pulumi up` has run. An empty value is a stack that has not
# been applied, so it stops the script rather than writing a blank into a pipeline.
REQUIRED=(
  "aws_account_id:AWS_ACCOUNT_ID"
  "aws_region:AWS_REGION"
  "site_bucket:SITE_BUCKET"
  "cloudfront_distribution_id:CLOUDFRONT_DISTRIBUTION_ID"
  "site_url:SITE_URL"
  "deploy_role_arn:AWS_DEPLOY_ROLE_ARN"
  "preview_role_arn:AWS_PREVIEW_ROLE_ARN"
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

SITE_URL="$(read_output site_url)"

cat <<EOF

Done. AWS_DEPLOY_ROLE_ARN is now set, which un-gates .github/workflows/cd.yml, and
AWS_PREVIEW_ROLE_ARN, which un-gates the preview in infra.yml.

cd.yml runs in the GitHub environment 'production', and the deploy role trusts a token from that
environment and no other. Give the environment protection rules — required reviewers, or a branch
restriction to main — in the repository settings: they are what stands between a push and a deploy.

Two things this script cannot do, because their values come from outside this stack:

  * API_URL — the backend's address, compiled into the bundle as VITE_API_URL. Set it by hand:

      gh variable set API_URL --repo ${REPO} --body 'https://<the backend's URL>'

    cd.yml refuses to build without it: a bundle built without one would call
    {{ cookiecutter.api_url }}, deploy cleanly, and fail every request.

  * The backend's side of the conversation. Its stack must list this site's origin,

      ${SITE_URL}

    in its corsOrigins configuration{% if cookiecutter.include_auth == "yes" %}, and set refreshTransport=cookie so a browser keeps the
    refresh token in an HttpOnly cookie{% endif %}. Then apply the backend's stack.

EOF

if ! gh variable get API_URL --repo "${REPO}" >/dev/null 2>&1; then
  echo "  API_URL is not set yet, so cd.yml will refuse to build until it is." >&2
fi
