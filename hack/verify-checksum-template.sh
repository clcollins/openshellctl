#!/usr/bin/env bash
# verify-checksum-template.sh — confirm that .goreleaser.yaml's checksum
# name_template renders to the same shape that selfupdate.ChecksumAssetName
# produces for the same tag. Run as: hack/verify-checksum-template.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SAMPLE_TAG="v0.1.5"
SAMPLE_VER="${SAMPLE_TAG#v}"   # strip leading v (goreleaser .Version has no v)

# Extract the checksum name_template from .goreleaser.yaml.
# The checksum block comes before the release block; grep for the line inside
# it by using awk to read from "^checksum:" until the next top-level key.
TEMPLATE=$(awk '/^checksum:/{in_checksum=1} in_checksum && /name_template:/{print; exit}' \
    "${REPO_ROOT}/.goreleaser.yaml" \
  | sed 's/.*name_template:[[:space:]]*//' | tr -d '"')

if [ -z "$TEMPLATE" ]; then
    printf 'FAIL: could not find checksum name_template in .goreleaser.yaml\n'
    exit 1
fi

# Render the template with the sample values.
RENDERED=$(printf '%s' "$TEMPLATE" \
  | sed "s/{{ .ProjectName }}/openshellctl/g; s/{{ .Version }}/${SAMPLE_VER}/g")

# selfupdate.ChecksumAssetName strips the leading v and formats:
#   openshellctl_<version>_checksums.txt
EXPECTED="openshellctl_${SAMPLE_VER}_checksums.txt"

if [ "$RENDERED" != "$EXPECTED" ]; then
    printf 'FAIL: goreleaser checksum template mismatch\n'
    printf '  template : %s\n' "$TEMPLATE"
    printf '  rendered : %s\n' "$RENDERED"
    printf '  expected : %s\n' "$EXPECTED"
    exit 1
fi

printf 'OK: .goreleaser.yaml checksum name_template matches selfupdate.ChecksumAssetName (%s)\n' "$RENDERED"
