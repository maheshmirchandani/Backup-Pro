#!/usr/bin/env bash
#
# regen-manifest.sh: regenerate the SHA256-of-tree line in each fixture's
# MANIFEST.txt.
#
# Usage: regen-manifest.sh
#
# Run from the repo root (or anywhere; the script computes paths relative
# to its own location). Re-run when fixture files change so the recorded
# tree hash matches.
#
# SHA256-of-tree recipe (must match the helper in test/e2e/helpers.go so
# tests can re-verify against the committed manifest):
#
#   1. List every regular file under the fixture dir, relative paths.
#   2. Sort the list lexicographically (LC_ALL=C; raw byte order).
#   3. For each path: write the bytes of the relative path, a single
#      newline byte, the file contents, and a single newline byte, to
#      stdout. Pipe stdout into sha256.
#   4. Hex-encode the result.
#
# The "+ newline after each chunk" framing prevents the
# concat-collision attack where files A="x" + B="y" and A="xy" + B=""
# would otherwise share a hash.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

compute_tree_sha() {
    local dir="$1"
    # Strip the leading "./" that find emits so the relative-path bytes
    # match the Go-side helper, which produces "a.txt" not "./a.txt".
    (cd "$dir" && LC_ALL=C find . -type f ! -name MANIFEST.txt -print0 \
        | LC_ALL=C sort -z \
        | while IFS= read -r -d '' rel; do
            rel="${rel#./}"
            printf '%s\n' "$rel"
            cat "$rel"
            printf '\n'
        done) | shasum -a 256 | awk '{print $1}'
}

for fix in tiny realistic; do
    dir="$here/$fix"
    [ -d "$dir" ] || continue
    sha=$(compute_tree_sha "$dir")
    echo "$fix: $sha"
done

# Pathological fixture is materialized by mkfixtures.sh at runtime; we
# compute its hash against a freshly-materialized tree so the MANIFEST
# value matches what tests will see.
ptmp=$(mktemp -d)
trap 'rm -rf "$ptmp"' EXIT
"$here/pathological/mkfixtures.sh" "$ptmp" >/dev/null 2>&1
sha=$(compute_tree_sha "$ptmp")
echo "pathological: $sha"
