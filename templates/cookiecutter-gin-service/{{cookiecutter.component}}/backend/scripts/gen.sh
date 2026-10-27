#!/usr/bin/env bash
# Regenerate everything generated in backend/, or with --check, fail if any of it is stale.
#
#   bash scripts/gen.sh           # `just gen`
#   bash scripts/gen.sh --check   # `just gen-check`, and CI's drift gate
#
# One script for both, so the check runs exactly the generation it checks. Run it in the dev
# container (the recipes do) or anywhere with Go, sqlc and swag at the versions pinned in
# docker/Dockerfile.dev and .github/workflows/{{ cookiecutter.component }}-ci.yml.
#
# What is generated, and from what:
#   internal/db/          sqlc, from queries/ against migrations/ (the schema)
#   docs/openapi*.json    swag reads the handler annotations into Swagger 2.0, then cmd/openapi
#                         converts that to OpenAPI 3 — the document /openapi.json serves.
set -euo pipefail

cd "$(dirname "$0")/.."

check=false
if [[ "${1:-}" == "--check" ]]; then
    check=true
fi

SWAG="${SWAG:-swag}"
SQLC="${SQLC:-sqlc}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# swag: Swagger 2.0, optionally filtered by tag. The transport-specific handlers carry a marker tag
# (transport-body, transport-cookie), and each run leaves out the other transport's operations;
# cmd/openapi strips the markers again. -o to a scratch directory, because swag's own output is an
# intermediate; the committed artefact is the OpenAPI 3 document built from it.
swagger() {
    local out="$1" tags="$2"
    "$SWAG" init \
        --quiet \
        --generalInfo cmd/api/main.go \
        --dir ./ \
        ${tags:+--tags "$tags"} \
        --outputTypes json \
        --output "$out" >/dev/null
}

openapi() {
    go run ./cmd/openapi -in "$1/swagger.json" -out "$2"
}
{% if cookiecutter.use_postgres == "yes" %}
swagger "$work/body" "!transport-cookie"
swagger "$work/cookie" "!transport-body"
openapi "$work/body" "$work/openapi.json"
openapi "$work/cookie" "$work/openapi-cookie.json"
documents=(openapi.json openapi-cookie.json)

if $check; then
    # sqlc diff compares what it would generate with what is committed, and exits 1 on any change.
    "$SQLC" diff
else
    "$SQLC" generate
fi
{%- else %}
swagger "$work/body" ""
openapi "$work/body" "$work/openapi.json"
documents=(openapi.json)
{%- endif %}

stale=false
for document in "${documents[@]}"; do
    if $check; then
        if ! diff -u "docs/$document" "$work/$document"; then
            stale=true
        fi
    else
        cp "$work/$document" "docs/$document"
    fi
done

if $stale; then
    echo "Generated files are out of date. Run \`just gen\` and commit the result." >&2
    exit 1
fi
