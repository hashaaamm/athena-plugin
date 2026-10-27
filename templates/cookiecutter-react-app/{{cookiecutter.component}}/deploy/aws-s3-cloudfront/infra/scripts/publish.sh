#!/usr/bin/env bash
# Publish a built bundle: upload it to the site bucket, invalidate the index, wait for that, prune
# the previous deploy's hashed files, and smoke-test the result.
#
# One script, run by both paths that publish: the CD workflow on every merge, and
# `just infra-first-deploy` once, from a laptop. Two copies of this sequence would be two sequences
# within a month, and the one that drifted would be the one nobody runs often.
#
#   BUCKET=… DISTRIBUTION=… SITE_URL=… [API_URL=…] ./infra/scripts/publish.sh <dist-dir>
#
# API_URL is what the bundle was built with, the stack's api_url export. `/` means the distribution
# serves the API on the site's own origin, and the smoke test then checks that route too.
#
# It acts with whatever AWS credentials the environment holds: the deploy role's session in CD,
# your own on a laptop. It builds nothing — the bundle in <dist-dir> is published as it is.
set -euo pipefail

DIST="${1:?usage: publish.sh <dist-dir>}"
: "${BUCKET:?BUCKET is not set: the site bucket, site_bucket in the stack outputs}"
: "${DISTRIBUTION:?DISTRIBUTION is not set: cloudfront_distribution_id in the stack outputs}"
: "${SITE_URL:?SITE_URL is not set: site_url in the stack outputs}"
API_URL="${API_URL:-}"

# In a workflow a failure is an annotation on the run; on a terminal it is a line on stderr.
fail() {
  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    echo "::error::$*" >&2
  else
    echo "  FAILED  $*" >&2
  fi
  exit 1
}

command -v aws >/dev/null || fail "aws is not installed"
command -v curl >/dev/null || fail "curl is not installed"
[[ -f "${DIST}/index.html" ]] || fail "${DIST}/index.html does not exist: build the bundle first"

# Two syncs, because the two halves of a Vite build want opposite caching and `aws s3 sync` sets one
# Cache-Control per invocation.
#
# assets/ first. Every file in it is named for a hash of its content, so a name never means two
# things: it is cached for a year and never revalidated. Uploaded before the index, and without
# --delete, so for the whole of this step the live index.html still finds every file it names — the
# previous deploy's and this one's are side by side.
#
# Then everything else — index.html above all — with no-cache, which a browser and CloudFront both
# revalidate on every request: the index is the map to the hashed files, and a cached copy pins a
# visitor to a deploy whose files are about to go. --delete here, with assets/ excluded, removes
# root files the build no longer produces and leaves the hashed files alone.
echo "==> Upload"
aws s3 sync "${DIST}/assets/" "s3://${BUCKET}/assets/" \
  --cache-control "public,max-age=31536000,immutable" --only-show-errors
aws s3 sync "${DIST}/" "s3://${BUCKET}/" \
  --exclude "assets/*" --cache-control "no-cache" --delete --only-show-errors
echo "    $(find "${DIST}" -type f | wc -l | tr -d ' ') files in s3://${BUCKET}"

# The edge may still hold the previous index for its minimum TTL. An invalidation clears it, and is
# not instant: it can take minutes. So the wait is a loop with a deadline rather than
# `aws cloudfront wait invalidation-completed`, whose fixed cap is shorter than a slow invalidation
# and turns a deploy that succeeded into a red run.
echo "==> Invalidate the index"
ID=$(aws cloudfront create-invalidation --distribution-id "${DISTRIBUTION}" \
  --paths "/index.html" "/" --query Invalidation.Id --output text)
echo "    invalidation ${ID}"
DEADLINE=$((SECONDS + 1200))
while :; do
  STATUS=$(aws cloudfront get-invalidation --distribution-id "${DISTRIBUTION}" --id "${ID}" \
    --query Invalidation.Status --output text)
  if [[ "${STATUS}" == "Completed" ]]; then break; fi
  if [[ "${SECONDS}" -ge "${DEADLINE}" ]]; then
    fail "invalidation ${ID} is still ${STATUS} after 20 minutes"
  fi
  sleep 15
done
echo "    completed"

# The previous deploy's hashed files go only now, once the index that named them is no longer
# served. Same command as the upload's first half plus --delete: nothing is re-uploaded, because
# every file in assets/ is already there.
echo "==> Prune the previous deploy's assets"
aws s3 sync "${DIST}/assets/" "s3://${BUCKET}/assets/" \
  --cache-control "public,max-age=31536000,immutable" --delete --only-show-errors

echo "==> Smoke test"
SERVED="$(mktemp)"
trap 'rm -f "${SERVED}"' EXIT

# The index: the distribution answers, over HTTPS, from the bucket. Downloaded to a file rather than
# piped into grep, which would close the pipe on its first match and kill curl with SIGPIPE.
code=$(curl -s -o "${SERVED}" -w '%{http_code}' "${SITE_URL}/")
[[ "${code}" == "200" ]] || fail "${SITE_URL}/ answered ${code}"

# And it is THIS build's index: it names the entry script this run just published.
ENTRY=$(grep -o 'assets/[^"]*\.js' "${DIST}/index.html" | head -1)
grep -q "${ENTRY}" "${SERVED}" || fail "${SITE_URL}/ does not serve ${ENTRY}"

# A route the bucket has no object for. It must still answer 200 with the index — that is the
# distribution's SPA fallback, and without it every hard refresh on a real route is an error page
# that only the person who bookmarked it ever sees.
code=$(curl -s -o /dev/null -w '%{http_code}' "${SITE_URL}/some/deep/route")
[[ "${code}" == "200" ]] || fail "${SITE_URL}/some/deep/route answered ${code}"

# The API on the site's own origin, when the bundle calls it there. JSON and not the index: a path
# that missed the /health/* behaviour is served the app's shell with a 200, which a status check
# alone would pass.
if [[ "${API_URL}" == "/" ]]; then
  answer=$(curl -s -o /dev/null -w '%{http_code} %{content_type}' "${SITE_URL}/health/live")
  [[ "${answer}" == "200 application/json"* ]] ||
    fail "${SITE_URL}/health/live answered '${answer}', not the API's 200 JSON: is the API up, and the stack applied?"
  echo "    the API answers on ${SITE_URL}/health/live"
fi
echo "    serving ${SITE_URL}"
