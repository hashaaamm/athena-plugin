#!/usr/bin/env bash
# Run Terraform in infra/terraform with the arguments given.
#
# The installed `terraform` when it is 1.16 or newer — the version terraform.tf requires — and
# otherwise the pinned image, so nobody has to install Terraform to work on this component. The
# image runs as you, sees only this component's folder, and reads your gcloud Application Default
# Credentials read-only, which is what the Google, Kubernetes and Helm providers authenticate with.
#
#   ./infra/scripts/tf.sh plan -var-file=environments/dev.tfvars
#
set -euo pipefail

TF_IMAGE="hashicorp/terraform:1.16.5"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPONENT_DIR="$(cd "${HERE}/../.." && pwd)"
TF_DIR="${COMPONENT_DIR}/infra/terraform"

native_is_recent() {
  command -v terraform >/dev/null 2>&1 || return 1
  local version
  version="$(terraform version 2>/dev/null | sed -n 's/^Terraform v\([0-9]*\.[0-9]*\).*/\1/p' | head -1)"
  [[ -n "${version}" ]] || return 1
  local major="${version%%.*}" minor="${version#*.}"
  (( major > 1 || (major == 1 && minor >= 16) ))
}

if native_is_recent && [[ "${TF_USE_DOCKER:-}" != "1" ]]; then
  cd "${TF_DIR}"
  exec terraform "$@"
fi

command -v docker >/dev/null 2>&1 || {
  echo "Neither terraform >= 1.16 nor docker is installed. Install one of them." >&2
  exit 1
}

# A unique container name: Docker on this machine may be shared with other work, and two runs must
# never collide on a name. --rm removes the container when Terraform exits.
args=(
  run --rm -i
  --name "tf-$(basename "${COMPONENT_DIR}")-$$"
  --user "$(id -u):$(id -g)"
  -e HOME=/tmp/home
  -e TF_IN_AUTOMATION
  -e TF_LOG
  -e TF_PLUGIN_CACHE_DIR
  -v "${COMPONENT_DIR}:/work"
  -w /work/infra/terraform
)
[[ -t 0 && -t 1 ]] && args+=(-t)

GCLOUD_CONFIG="${CLOUDSDK_CONFIG:-${HOME}/.config/gcloud}"
if [[ -d "${GCLOUD_CONFIG}" ]]; then
  args+=(-v "${GCLOUD_CONFIG}:/tmp/home/.config/gcloud:ro")
fi
if [[ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -f "${GOOGLE_APPLICATION_CREDENTIALS}" ]]; then
  args+=(-v "${GOOGLE_APPLICATION_CREDENTIALS}:/tmp/adc.json:ro" -e GOOGLE_APPLICATION_CREDENTIALS=/tmp/adc.json)
fi

exec docker "${args[@]}" "${TF_IMAGE}" "$@"
