#!/usr/bin/env bash
set -euo pipefail

artifacts=${1:?Pass the directory containing the tested OCI archives}
: "${TAGS:?Set the tags to publish}"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

layout="ocidir://$work/image"
for arch in amd64 arm64; do
    ref="$layout:$arch"
    regctl image import "$ref" "$artifacts/docktinel-$arch.tar"
    platform=$(regctl image inspect "$ref" --platform "linux/$arch" --format '{{.OS}}/{{.Architecture}}')
    if [[ "$platform" != "linux/$arch" ]]; then
        echo "Expected linux/$arch, got $platform" >&2
        exit 1
    fi
    regctl manifest get "$ref" --format raw-body > "$work/$arch.json"
    if ! jq -e '.manifests | type == "array"' "$work/$arch.json" >/dev/null; then
        regctl index create "$layout:index-$arch" --ref "$ref"
        regctl manifest get "$layout:index-$arch" --format raw-body > "$work/$arch.json"
    fi
done

# Keep the tested image descriptors and their provenance and SBOM attestations.
jq -es '
    def platforms:
        [.manifests[].platform | select(.os != "unknown") | "\(.os)/\(.architecture)"];
    if map(platforms) == [["linux/amd64"], ["linux/arm64"]] then
        {schemaVersion: 2, mediaType: "application/vnd.oci.image.index.v1+json",
         manifests: [.[].manifests[]]}
    else error("Expected one linux/amd64 image and one linux/arm64 image in their respective archives") end
' "$work/amd64.json" "$work/arm64.json" | regctl manifest put "$layout:release"

digest=$(regctl image digest "$layout:release" --require-list)
while IFS= read -r tag; do
    [[ -n "$tag" ]] || continue
    regctl image copy "$layout:release" "$tag"
    published=$(regctl image digest "$tag" --require-list)
    if [[ "$published" != "$digest" ]]; then
        echo "$tag: expected $digest, got $published" >&2
        exit 1
    fi
    printf '%s %s\n' "$tag" "$digest"
done <<< "$TAGS"
echo "digest=$digest" >> "$GITHUB_OUTPUT"
