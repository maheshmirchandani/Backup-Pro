#!/usr/bin/env bash
#
# mkfixtures.sh: materialize the pathological fixture tree into $1.
#
# Usage: mkfixtures.sh <dest-dir>
#
# Why a script (not checked-in fixture files):
#   - NFC vs NFD twin filenames: git on macOS HFS+ checkout silently
#     normalizes one form to the other, defeating the twin pair.
#   - 0x07 (BEL) / 0x1B (ESC) in filenames: a Windows checkout refuses
#     them; many editors / archivers mangle them.
#   - Sparse files: git stores fully expanded blobs; sparseness is lost.
#   - Immutable files (chflags uchg): filesystem state, not file content.
#
# Each member documented in MANIFEST.txt sibling file.
#
# This script is idempotent: a second invocation against the same dest
# overwrites everything. Callers that need a clean tree should remove
# the dest first.

set -euo pipefail

if [ "$#" -ne 1 ]; then
    echo "usage: $0 <dest-dir>" >&2
    exit 2
fi
dest="$1"
mkdir -p "$dest"

# --- ASCII control bytes in filenames -------------------------------------
# The shell's $'\x07' / $'\x1b' literal works on bash 4+ and zsh; touch
# accepts the resulting bytes on APFS / HFS+. We avoid quoting tricks
# that some shells flatten.
touch "$dest/$(printf 'bell\x07char.txt')"
touch "$dest/$(printf 'esc\x1bchar.txt')"

# --- NFC vs NFD twin pair -------------------------------------------------
# Both filenames look like "naive.txt" with a diaeresis on the i. NFC uses
# a single U+00EF code point; NFD uses U+0069 + U+0308 (combining).
# On APFS the two paths are distinct entries; on HFS+ they would collapse
# (HFS+ stores filenames pre-normalized to NFD). Our test fixture targets
# APFS so we exercise both.
printf 'nfc form\n' > "$dest/$(printf 'na\xc3\xafve-nfc.txt')"
printf 'nfd form\n' > "$dest/$(printf 'nai\xcc\x88ve-nfd.txt')"

# --- Long deeply-nested path ---------------------------------------------
# Total relative path length lands above 200 bytes after the segment
# concatenation; tests AC paths that exercise path-handling buffers.
deep="$dest/deeply/nested/path/that/exceeds/usual/limits/for/most/filesystems/and/operators"
mkdir -p "$deep"
printf 'deep file content\n' > "$deep/file.txt"

# --- Sparse file ----------------------------------------------------------
# 1 MiB logical size, ~0 bytes physical; dd with seek + count=0 creates
# the hole without writing the body. macOS APFS supports sparse files.
sparse="$dest/sparse.bin"
dd if=/dev/zero of="$sparse" bs=1 count=0 seek=1m 2>/dev/null
# Add a few bytes at the end so the file isn't entirely sparse (some
# tools treat fully-zero files specially); the middle is still a hole.
printf 'tail\n' >> "$sparse"

# --- Immutable file -------------------------------------------------------
# Created here; the test that consumes this fixture will run
# `chflags uchg` against it before the backup run (this script can't
# leave the bit set or the test framework couldn't clean up after itself
# on a TempDir host). Filename signals intent.
printf 'immutable target\n' > "$dest/immutable-target.txt"

# --- A regular ordinary file to keep the tree's "happy" path present -----
printf 'plain content\n' > "$dest/plain.txt"

echo "materialized pathological fixture into $dest" >&2
