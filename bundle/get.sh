#!/bin/sh
# Fills bundle/out/v3compat with the contents of a v3compat release tarball (without its top
# directory); install/install.sh copies it to /opt/v3compat.
#   bundle/get.sh [--release]        download the release pinned in bundle/pin.env, verify its sha256
#   bundle/get.sh --tarball PATH     use a local tarball; prints its sha256 (for pin.env)
#   bundle/get.sh --source           build V3COMPAT_COMMIT from source; needs cmake, gcc, g++, git, patch,
#                                    zlib1g-dev. DynamoRIO builds into ~/.cache/v3compat (~10 min first time).
# v3compat and DynamoRIO may be redistributed; their notices are in share/doc/v3compat/.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=pin.env
. "$here/pin.env"
out=$here/out/v3compat
mode=release
tarball=

while [ $# -gt 0 ]; do
    case $1 in
        --release) mode=release; shift ;;
        --tarball) mode=tarball; tarball=${2:?--tarball needs a path}; shift 2 ;;
        --source) mode=source; shift ;;
        *) echo "usage: $0 [--release | --tarball PATH | --source]" >&2; exit 2 ;;
    esac
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case $mode in
    release)
        if [ -z "$V3COMPAT_VERSION" ] || [ -z "$V3COMPAT_SHA256" ]; then
            echo "no release pinned in bundle/pin.env yet; use --source or --tarball PATH" >&2
            exit 1
        fi
        tarball=$work/v3compat.tar.gz
        curl -fsSL -o "$tarball" \
            "https://github.com/lhns/v3compat/releases/download/v$V3COMPAT_VERSION/v3compat-$V3COMPAT_VERSION-linux-x86_64.tar.gz"
        echo "$V3COMPAT_SHA256  $tarball" | sha256sum -c -
        ;;
    tarball)
        sha256sum "$tarball"
        ;;
    source)
        git init -q "$work/src"
        git -C "$work/src" fetch -q --depth 1 https://github.com/lhns/v3compat "$V3COMPAT_COMMIT"
        git -C "$work/src" checkout -q FETCH_HEAD
        cmake -S "$work/src" -B "$work/build"
        cmake --build "$work/build" --target package-tarball -j "$(nproc)"
        tarball=$(ls "$work"/build/v3compat-*-linux-x86_64.tar.gz)
        ;;
esac

rm -rf "$out"
mkdir -p "$out"
tar -xzf "$tarball" -C "$out" --strip-components=1
chmod -R a+rX "$out"
[ -x "$out/bin/v3compat-run" ] || { echo "no bin/v3compat-run in the tarball" >&2; exit 1; }
echo "filled $out"
