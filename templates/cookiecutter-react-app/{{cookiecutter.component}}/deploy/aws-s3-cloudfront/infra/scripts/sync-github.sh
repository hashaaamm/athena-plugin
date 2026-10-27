#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates this component's
# CD workflow.
#
# Every variable is prefixed {{ cookiecutter.__component_var }}_. One repository holds several
# components, and each has its own deploy role, its own bucket and its own backend address; an
# unprefixed AWS_DEPLOY_ROLE_ARN would be overwritten by whichever component synced last.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. GitHub's
# OIDC provider means CI holds no credential at all — a workflow exchanges GitHub's own token for a
# short-lived AWS session, and the role's trust policy says which workflow may. Every value below
# is a public identifier: an account id, a region, a bucket name, a distribution id, a role ARN, a
# URL. None of them grants anything alone.
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

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/{{ cookiecutter.component }}-cd.yml and {{ cookiecutter.component }}-infra.yml
# exactly. infra/pulumi/tests/contract.test.ts asserts that it does, because the failure otherwise
# is a deploy job that is skipped forever and never says why.
#
# Every one of these exists once `pulumi up` has run with `apiUrl` set. An empty value stops the
# script rather than writing a blank into a pipeline.
REQUIRED=(
  "aws_account_id:{{ cookiecutter.__component_var }}_AWS_ACCOUNT_ID"
  "aws_region:{{ cookiecutter.__component_var }}_AWS_REGION"
  "site_bucket:{{ cookiecutter.__component_var }}_SITE_BUCKET"
  "cloudfront_distribution_id:{{ cookiecutter.__component_var }}_CLOUDFRONT_DISTRIBUTION_ID"
  "site_url:{{ cookiecutter.__component_var }}_SITE_URL"
  "deploy_role_arn:{{ cookiecutter.__component_var }}_AWS_DEPLOY_ROLE_ARN"
  "preview_role_arn:{{ cookiecutter.__component_var }}_AWS_PREVIEW_ROLE_ARN"
  "apply_role_arn:{{ cookiecutter.__component_var }}_AWS_APPLY_ROLE_ARN"
  # Where the bundle calls the API, compiled into it: `/` — the site's own origin — while the
  # distribution serves the API, the backend's own address otherwise. Derived by the stack from
  # `apiUrl` and `apiSameOrigin`, so CD builds with what the first deploy built with, and nobody
  # types it into GitHub by hand.
  "api_url:{{ cookiecutter.__component_var }}_API_URL"
)

for entry in "${REQUIRED[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -z "${value}" && "${output}" == "api_url" ]]; then
    cat >&2 <<EOF
  MISSING  api_url — the stack has no apiUrl, so CD would build a bundle that calls nothing.

    pulumi -C "${PULUMI_DIR}" config set --plaintext apiUrl 'https://<the backend URL>' --stack "${STACK}"
    just infra-up ${STACK}
    just infra-first-deploy ${STACK}

  Then run this again.
EOF
    exit 1
  fi
  if [[ -z "${value}" ]]; then
    echo "  MISSING  ${output} — has 'pulumi up' run for stack '${STACK}'?" >&2
    exit 1
  fi
  gh variable set "${variable}" --repo "${REPO}" --body "${value}"
  echo "  set   ${variable} = ${value}"
done

SITE_URL="$(read_output site_url)"
API_URL="$(read_output api_url)"

cat <<EOF

Done. {{ cookiecutter.__component_var }}_AWS_DEPLOY_ROLE_ARN is now set, which un-gates
.github/workflows/{{ cookiecutter.component }}-cd.yml; {{ cookiecutter.__component_var }}_AWS_PREVIEW_ROLE_ARN un-gates the preview in
{{ cookiecutter.component }}-infra.yml, and {{ cookiecutter.__component_var }}_AWS_APPLY_ROLE_ARN its apply. From the next push to main, CD owns
every deploy and {{ cookiecutter.component }}-infra.yml applies every infrastructure change that merges. The applies so far
ran from this machine because the apply role did not exist yet; one from a laptop now is the
exception Pulumi standards MUST-9 forbids.

Before that push, give two GitHub environments protection rules in the repository settings. Each
is trusted by one role and no other, and its rules are what stands between a merge and a change:

  * 'production' — the CD workflow's deploy job. Required reviewers, or a branch restriction to main.
  * 'infrastructure' — {{ cookiecutter.component }}-infra.yml's apply job, whose role can change anything in the account.
    Required reviewers, and deployment branches limited to main.

EOF

if [[ "${API_URL}" == "/" ]]; then
  cat <<EOF
The browser calls the API on this site's own origin — ${SITE_URL}/api/… — so the backend needs
no CORS for it.
{%- if cookiecutter.include_auth == "yes" %} One thing this script cannot do, because it belongs to the backend's stack:
its refreshTransport must be cookie, and its corsOrigins must still list this site's origin,

    ${SITE_URL}

The cookie routes' CSRF check accepts only the origins on that list, and a browser sends Origin on
every POST, same-origin ones included. Then apply the backend's stack.
{%- endif %}

EOF
else
  cat <<EOF
One thing this script cannot do, because it belongs to the backend's stack: its corsOrigins must
list this site's origin,

    ${SITE_URL}
{% if cookiecutter.include_auth == "yes" %}
and its refreshTransport must be cookie, so a browser keeps the refresh token in an HttpOnly cookie.
Called directly, the API is another site, and a reload signs the user out unless both are on a
domain you own.
{%- endif %}
Then apply the backend's stack.

EOF
fi
