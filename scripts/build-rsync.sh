#!/bin/bash
# scripts/build-rsync.sh: build GNU rsync universal2 from upstream source.
#
# Modes:
#   (none)          full build: arm64 + x86_64 + lipo -> internal/rsync/bin/rsync.universal2
#   --smoke         host architecture only, no lipo. CI per-commit signal.
#   --verify-only   download + SHA-check the tarball, no build.
#
# The upstream pin lives in scripts/rsync.version and is PARSED, never
# sourced. See that file's header for why.
#
# set -E is REQUIRED, not decorative: without it an ERR trap is not
# inherited by shell functions, and every failure path here is inside one.
set -eEuo pipefail
IFS=$'\n\t'

# PATH hygiene at entry, not per function, so a poisoned PATH entry cannot
# substitute clang, tar or shasum.
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"

SMOKE_MODE=0
VERIFY_ONLY=0
case "${1:-}" in
    --smoke)        SMOKE_MODE=1 ;;
    --verify-only)  VERIFY_ONLY=1 ;;
    "")             ;;
    *)              echo "FATAL: unknown flag '$1'" >&2; exit 1 ;;
esac

for tool in clang lipo shasum curl tar make grep cut file otool sysctl uname; do
    if ! command -v "${tool}" >/dev/null; then
        echo "FATAL: required tool '${tool}' not on PATH" >&2
        echo "  on macOS, install Xcode Command Line Tools: xcode-select --install" >&2
        exit 1
    fi
done

# Anchor to the repo root, not the caller's cwd, so running this from a
# subdirectory cannot scatter build/ and the payload outside the tree.
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK_DIR="${PROJECT_ROOT}/build"
CACHE_DIR="${WORK_DIR}/cache"
SRC_DIR="${WORK_DIR}/src"
OUTPUT_PATH="${PROJECT_ROOT}/internal/rsync/bin/rsync.universal2"
MIN_MACOS="13.0"
HOST_ARCH="$(uname -m)"

VERSION_FILE="${PROJECT_ROOT}/scripts/rsync.version"
[[ -f "${VERSION_FILE}" ]] || { echo "FATAL: ${VERSION_FILE} not found" >&2; exit 1; }

# Parse, never source. Anchored regexes accept ONLY a bare literal on the
# right-hand side; `cut -s` suppresses delimiter-free lines so a NUL-bearing
# "Binary file ... matches" message cannot be mistaken for a value.
RSYNC_VERSION="$(grep -aE '^RSYNC_VERSION=[A-Za-z0-9.-]+$' "${VERSION_FILE}" | cut -s -d= -f2 || true)"
RSYNC_TARBALL_SHA256="$(grep -aE '^RSYNC_TARBALL_SHA256=[a-f0-9]{64}$' "${VERSION_FILE}" | cut -s -d= -f2 || true)"

# Exactly-one-match guard: an anchored regex still admits a second smuggled
# assignment line, which would make these variables multi-line.
for key in RSYNC_VERSION RSYNC_TARBALL_SHA256; do
    count="$(grep -acE "^${key}=" "${VERSION_FILE}" || true)"
    [[ "${count}" == "1" ]] || { echo "FATAL: ${VERSION_FILE} has ${count} lines for ${key}; expected 1" >&2; exit 1; }
done
# Re-validate after capture, defending against anything the pipeline let through.
[[ "${RSYNC_VERSION}" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "FATAL: RSYNC_VERSION malformed" >&2; exit 1; }
[[ "${RSYNC_TARBALL_SHA256}" =~ ^[a-f0-9]{64}$ ]] || { echo "FATAL: RSYNC_TARBALL_SHA256 malformed" >&2; exit 1; }

PRIMARY_URL="https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror-rsync-${RSYNC_VERSION}/rsync-${RSYNC_VERSION}.tar.gz"
FALLBACK_URL="https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz"

on_error() {
    if compgen -G "${WORK_DIR}/*/config.log" >/dev/null 2>&1; then
        echo "FAILED. See ${WORK_DIR}/<arch>/config.log for build details." >&2
    elif [[ -d "${SRC_DIR}" ]]; then
        echo "FAILED during build setup. ${SRC_DIR} preserved for inspection." >&2
    else
        echo "FAILED before build started. See output above." >&2
    fi
}
trap 'on_error' ERR

download_and_verify_tarball() {
    mkdir -p "${CACHE_DIR}"
    local tarball="${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz"

    if [[ -f "${tarball}" ]] && \
       [[ "$(shasum -a 256 "${tarball}" | cut -d' ' -f1)" == "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "tarball cached + verified"
        return
    fi
    if [[ -f "${tarball}" ]]; then
        echo "WARNING: cached tarball failed SHA256 verification; re-downloading" >&2
        echo "  cached: $(shasum -a 256 "${tarball}" | cut -d' ' -f1)" >&2
        echo "  wanted: ${RSYNC_TARBALL_SHA256}" >&2
        mv "${tarball}" "${tarball}.rejected"
        echo "  rejected file preserved at ${tarball}.rejected" >&2
    fi

    echo "downloading from primary mirror..." >&2
    if ! curl -fSL --progress-bar -o "${tarball}.tmp" "${PRIMARY_URL}"; then
        echo "primary mirror unreachable; falling back to samba.org" >&2
        curl -fSL --progress-bar -o "${tarball}.tmp" "${FALLBACK_URL}"
    fi

    local got
    got="$(shasum -a 256 "${tarball}.tmp" | cut -d' ' -f1)"
    if [[ "${got}" != "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "FATAL: tarball SHA256 mismatch" >&2
        echo "  expected: ${RSYNC_TARBALL_SHA256}" >&2
        echo "  got:      ${got}" >&2
        echo "  (an HTML error page from a CDN hashes just as cleanly as a tarball;" >&2
        echo "   inspect ${tarball}.tmp before assuming a version drift)" >&2
        exit 1
    fi
    mv "${tarball}.tmp" "${tarball}"
    echo "tarball downloaded + verified"
}

extract_sources() {
    rm -rf "${SRC_DIR}"; mkdir -p "${SRC_DIR}"
    tar -xzf "${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz" -C "${SRC_DIR}" --strip-components=1
}

# build_arch does NOT pass --build. In autoconf --build names the machine you
# compile ON; setting build == host keeps cross_compiling=no, so configure
# EXECUTES its conftest binary and aborts with "cannot run C compiled
# programs" on any host that cannot run the target architecture (verified
# against rsync-3.4.1/configure.sh:1244-1250). Passing --host alone lets
# autoconf fall back to cross mode instead.
#
# The three ac_cv_* overrides plus --disable-iconv* suppress
# AC_SEARCH_LIBS(iconv_open, iconv), which otherwise runs BEFORE the
# AC_ARG_ENABLE and appends -liconv unconditionally. Without them the binary
# links libiconv.2 and libcharset.1 and fails the libSystem-only assertion.
# Verified Mon, 24 Aug 2026: LIBS= empty, otool reports libSystem alone, and
# the binary keeps ACLs, xattrs, inplace and append.
build_arch() {
    local arch="$1" build_dir="${WORK_DIR}/$1"
    rm -rf "${build_dir}"; mkdir -p "${build_dir}"
    (cd "${build_dir}" && \
     CC="clang -arch ${arch} -mmacosx-version-min=${MIN_MACOS}" \
     ac_cv_search_iconv_open=no \
     ac_cv_search_libiconv_open=no \
     ac_cv_func_iconv_open=no \
     "${SRC_DIR}/configure" \
        --disable-openssl --disable-zstd --disable-lz4 --disable-xxhash \
        --disable-iconv-open --disable-iconv \
        --host="${arch}-apple-darwin")
    (cd "${build_dir}" && make -j"$(sysctl -n hw.ncpu)")
}

# assert_linkage asserts the dependency set EQUALS {libSystem}, and that it is
# non-empty. An allowlist-style "reject anything not libSystem" grep passes
# vacuously when otool emits no dependency lines, and is blind to @rpath and
# to framework paths, which is exactly the shape used to smuggle a load-time
# dependency resolved on the victim's machine.
assert_linkage() {
    local binary="$1" arch="$2" deps count
    deps="$(otool -L -arch "${arch}" "${binary}" | tail -n +2 | sed 's/ (compatibility.*//; s/^[[:space:]]*//' | sort -u)"
    count="$(printf '%s\n' "${deps}" | grep -c . || true)"
    if [[ "${count}" -eq 0 ]]; then
        echo "FATAL: ${arch}: otool reported no dependencies; assertion would pass vacuously" >&2
        exit 1
    fi
    if [[ "${deps}" != "/usr/lib/libSystem.B.dylib" ]]; then
        echo "FATAL: ${arch} links something other than libSystem alone:" >&2
        printf '%s\n' "${deps}" >&2
        echo "  Do NOT resolve this by widening the allowlist; that turns the control into decoration." >&2
        exit 1
    fi
    echo "linkage OK (${arch}): libSystem only"
}

emit_audit() {
    local target="$1"; shift
    echo; echo "=== build complete ==="
    file "${target}"
    echo "SHA256: $(shasum -a 256 "${target}" | cut -d' ' -f1)"
    for a in "$@"; do assert_linkage "${target}" "${a}"; done
    "${target}" --version | head -1
}

main() {
    if [[ ${VERIFY_ONLY} -eq 1 ]]; then
        download_and_verify_tarball
        echo "verify-only mode: tarball SHA matches pin. No build performed."
        return
    fi
    download_and_verify_tarball
    extract_sources
    if [[ ${SMOKE_MODE} -eq 1 ]]; then
        # Build for the HOST arch so every CI matrix cell can execute and
        # assert against its own output, including the Intel cell.
        build_arch "${HOST_ARCH}"
        emit_audit "${WORK_DIR}/${HOST_ARCH}/rsync" "${HOST_ARCH}"
        return
    fi
    build_arch "arm64"
    build_arch "x86_64"
    mkdir -p "$(dirname "${OUTPUT_PATH}")"
    lipo -create -output "${OUTPUT_PATH}" "${WORK_DIR}/arm64/rsync" "${WORK_DIR}/x86_64/rsync"
    chmod 0755 "${OUTPUT_PATH}"
    emit_audit "${OUTPUT_PATH}" "arm64" "x86_64"
}

main "$@"
