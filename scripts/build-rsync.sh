#!/bin/bash
# Task 12a: build GNU rsync 3.4.1 universal2 from source.
#
# This is a STUB placeholder for Task 12a. The full implementation will:
#   1. Clone https://github.com/RsyncProject/rsync at tag v3.4.1 into a
#      work directory under ./build/src.
#   2. Configure + make for darwin/amd64 (Intel) into ./build/amd64/rsync.
#   3. Configure + make for darwin/arm64 (Apple Silicon) into ./build/arm64/rsync.
#   4. lipo -create -output ../internal/rsync/bin/rsync \
#        ./build/amd64/rsync ./build/arm64/rsync
#      to produce the universal2 binary that internal/rsync embeds.
#   5. Compute SHA256 of the lipo'd binary and emit it on stdout for
#      audit / release-note inclusion. The Go-side EmbeddedSHA256() will
#      recompute the same value at runtime; no constant needs updating.
#   6. Remove the old internal/rsync/bin/rsync.placeholder (which exists
#      only so Task 12's extraction logic is testable without this script).
#
# Prereqs (auto-install via brew per project policy):
#   autoconf automake libtool pkg-config
# Dependencies pulled from brew or built from source:
#   popt openssl@3 zlib zstd lz4 xxhash
#
# Until Task 12a lands, internal/rsync embeds a placeholder shell script;
# the Go code's extraction + SHA256 verification + chmod 0500 + chflags
# uchg logic is exercised against that placeholder by the package tests.
#
# Invariant #38 (embedded rsync source pin tracking) is satisfied by:
#   - the pinned upstream tag/SHA recorded in this script when implemented
#   - the SHA256 of the lipo output, computable at runtime via
#     EmbeddedSHA256() in internal/rsync.

set -euo pipefail

echo "scripts/build-rsync.sh is a Task 12a stub; not yet implemented" >&2
echo "Until Task 12a lands, internal/rsync embeds bin/rsync.placeholder." >&2
exit 1
