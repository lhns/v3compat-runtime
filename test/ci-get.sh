#!/bin/sh
# Checks bundle/ci-get.sh against a local repo with an annotated and a lightweight tag and a stub get.sh.
#   test/ci-get.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
w=$(mktemp -d)
trap 'rm -rf "$w"' EXIT
fail=0
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

git init -q "$w/repo"
git -C "$w/repo" commit -q --allow-empty -m one
c1=$(git -C "$w/repo" rev-parse HEAD)
git -C "$w/repo" tag -a -m annotated v1.0.0
git -C "$w/repo" commit -q --allow-empty -m two
c2=$(git -C "$w/repo" rev-parse HEAD)
git -C "$w/repo" tag v2.0.0

printf '#!/bin/sh\necho "$@" > "%s/got"\n' "$w" > "$w/get.sh"
chmod +x "$w/get.sh"

# run NAME VERSION COMMIT WANT_STATUS WANT_ARGS
run() {
    printf 'V3COMPAT_COMMIT=%s\nV3COMPAT_VERSION=%s\nV3COMPAT_SHA256=x\n' "$3" "$2" > "$w/pin.env"
    rm -f "$w/got"; status=0
    V3COMPAT_PIN=$w/pin.env V3COMPAT_REPO=$w/repo V3COMPAT_GET=$w/get.sh "$here/../bundle/ci-get.sh" >"$w/out" 2>&1 || status=$?
    got=$(cat "$w/got" 2>/dev/null || true)
    if [ "$status" = "$4" ] && [ "$got" = "$5" ]; then echo "ok   $1"; else
        echo "FAIL $1: status $status (want $4), get.sh args '$got' (want '$5')"; sed 's/^/  out: /' "$w/out"; fail=1
    fi
}

run "no version: builds from source" "" "$c1" 0 "--source"
run "annotated tag matches (peeled)" 1.0.0 "$c1" 0 "--release"
run "lightweight tag matches" 2.0.0 "$c2" 0 "--release"
run "annotated tag, wrong commit" 1.0.0 "$c2" 1 ""
run "lightweight tag, wrong commit" 2.0.0 "$c1" 1 ""
run "missing tag" 3.0.0 "$c1" 1 ""

exit $fail
