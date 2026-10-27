#!/usr/bin/env bash
# Copy the stack's outputs into GitHub repository variables, which is what un-gates this component's
# CD workflow.
#
# Every variable is prefixed {{ cookiecutter.__component_var }}_. One repository holds several
# components, and each has its own deploy account, its own service and its own backend address; an
# unprefixed DEPLOY_SA would be overwritten by whichever component synced last.
#
# Note what this does *not* do: it sets no GitHub secrets. There are none to set here. Workload
# Identity Federation means CI holds no credential at all — it exchanges GitHub's own OIDC token
# for a short-lived Google one, and every value below is a public identifier: a project id, a
# region, a service-account email, a provider resource path, a URL. None of them grants anything
# alone.
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

# stack output -> repository variable. The right-hand column must match the `vars.` names in
# .github/workflows/{{ cookiecutter.component }}-cd.yml and {{ cookiecutter.component }}-infra.yml
# exactly. infra/pulumi/tests/contract.test.ts asserts that it does, because the failure otherwise
# is a deploy job that is skipped forever and never says why.
#
# Every one of these exists once `just infra-first-deploy` has run. An empty value stops the script
# rather than writing a blank into a pipeline.
REQUIRED=(
  "gcp_project:{{ cookiecutter.__component_var }}_GCP_PROJECT"
  "gcp_region:{{ cookiecutter.__component_var }}_GCP_REGION"
  "image_repo:{{ cookiecutter.__component_var }}_IMAGE_REPO"
  "wif_provider:{{ cookiecutter.__component_var }}_WIF_PROVIDER"
  "deploy_sa:{{ cookiecutter.__component_var }}_DEPLOY_SA"
  "preview_sa:{{ cookiecutter.__component_var }}_PREVIEW_SA"
  "apply_sa:{{ cookiecutter.__component_var }}_APPLY_SA"
  # The service, and its address, exist only once the first deploy has created it on a real image.
  "cloud_run_service:{{ cookiecutter.__component_var }}_CLOUD_RUN_SERVICE"
  "site_url:{{ cookiecutter.__component_var }}_SITE_URL"
  # Where the bundle calls the API, compiled into it: `/` — the site's own origin — while its nginx
  # proxies the API, the backend's own address otherwise. Derived by the stack from `apiUrl` and
  # `apiSameOrigin`, so CD builds with what the first deploy built with, and nobody types it into
  # GitHub by hand.
  "api_url:{{ cookiecutter.__component_var }}_API_URL"
)

for entry in "${REQUIRED[@]}"; do
  output="${entry%%:*}"
  variable="${entry##*:}"
  value="$(read_output "${output}" || true)"
  if [[ -z "${value}" ]]; then
    case "${output}" in
      cloud_run_service | site_url | api_url)
        cat >&2 <<EOF
  MISSING  ${output} — there is no service yet, so there is nothing for CD to update.

  Run the first deploy, which builds the real image and creates the service on it:

    just infra-first-deploy ${STACK}

  then run this again.
EOF
        ;;
      *)
        echo "  MISSING  ${output} — has 'pulumi up' run for stack '${STACK}'?" >&2
        ;;
    esac
    exit 1
  fi
  gh variable set "${variable}" --repo "${REPO}" --body "${value}"
  echo "  set   ${variable} = ${value}"
done

SITE_URL="$(read_output site_url)"
API_URL="$(read_output api_url)"

cat <<EOF

Done. {{ cookiecutter.__component_var }}_DEPLOY_SA is now set, which un-gates
.github/workflows/{{ cookiecutter.component }}-cd.yml; {{ cookiecutter.__component_var }}_PREVIEW_SA un-gates the preview in
{{ cookiecutter.component }}-infra.yml, and {{ cookiecutter.__component_var }}_APPLY_SA its apply. From the next push to main, CD owns every
deploy and {{ cookiecutter.component }}-infra.yml applies every infrastructure change that merges. The applies so far ran
from this machine because the apply account did not exist yet; one from a laptop now is the
exception Pulumi standards MUST-9 forbids.

Before that push, give two GitHub environments protection rules in the repository settings:

  * 'production' — the CD workflow's deploy job. Required reviewers, or a branch restriction to main.
  * 'infrastructure' — {{ cookiecutter.component }}-infra.yml's apply job. Only a job in it may impersonate the apply
    account, which can change anything in the project. Required reviewers, and deployment branches
    limited to main.

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
