#!/bin/sh
# CI: fills bundle/out/v3compat the way the pin says. With V3COMPAT_VERSION set in bundle/pin.env it
# downloads that release (bundle/get.sh, sha256-verified) after checking that the tag v<version> in
# lhns/v3compat points at V3COMPAT_COMMIT; without it, it builds V3COMPAT_COMMIT (get.sh --source).
# Test hooks: V3COMPAT_PIN (pin file), V3COMPAT_REPO (git URL or path for the tag lookup), V3COMPAT_GET (the get.sh to run).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=pin.env
. "${V3COMPAT_PIN:-$here/pin.env}"
repo=${V3COMPAT_REPO:-https://github.com/lhns/v3compat}
get=${V3COMPAT_GET:-$here/get.sh}

if [ -z "$V3COMPAT_VERSION" ]; then
    exec "$get" --source
fi

tag=refs/tags/v$V3COMPAT_VERSION
refs=$(git ls-remote "$repo" "$tag^{}" "$tag")
# An annotated tag lists the commit under the peeled name; a lightweight one under the plain name.
sha=$(printf '%s\n' "$refs" | awk -v t="$tag^{}" '$2 == t { print $1 }')
[ -n "$sha" ] || sha=$(printf '%s\n' "$refs" | awk -v t="$tag" '$2 == t { print $1 }')
[ -n "$sha" ] || { echo "tag v$V3COMPAT_VERSION not found in $repo" >&2; exit 1; }
if [ "$sha" != "$V3COMPAT_COMMIT" ]; then
    echo "tag v$V3COMPAT_VERSION is $sha, but bundle/pin.env pins V3COMPAT_COMMIT=$V3COMPAT_COMMIT" >&2
    exit 1
fi
exec "$get" --release
