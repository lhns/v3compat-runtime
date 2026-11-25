#!/bin/sh
# Runs the built installer image against a throwaway host root (a Docker volume) with --once.
#   test/installer.sh IMAGE
# Needs docker only; inspection also runs inside the image, so it checks the tools the image has.
set -eu

image=${1:?usage: test/installer.sh IMAGE}
vol=$(docker volume create)
cid=
cleanup() {
    [ -z "$cid" ] || docker rm -f "$cid" >/dev/null 2>&1 || true
    docker volume rm -f "$vol" >/dev/null 2>&1 || true
    rm -f "${out:-/nonexistent}"
}
trap cleanup EXIT
out=$(mktemp)
fail=0

# inst [ENV=VAL ...] -- ARGS... : run the image against the volume
inst() {
    set -- "$@" @@
    while [ "$1" != -- ]; do set -- "$@" -e "$1"; shift; done; shift
    set -- "$@" -v "$vol:/host" "$image"
    while [ "$1" != @@ ]; do set -- "$@" "$1"; shift; done; shift
    docker run --rm "$@"
}
# sh CMD: run a shell snippet in the image with the volume at /host
sh_in() { docker run --rm --entrypoint sh -v "$vol:/host" "$image" -c "$1"; }

# check NAME CMD...: ok when the command succeeds
check() {
    name=$1; shift
    if "$@" >"$out" 2>&1; then echo "ok   $name"; else echo "FAIL $name"; sed 's/^/  out: /' "$out"; fail=1; fi
}
# in_img NAME SNIPPET: ok when the snippet succeeds in the image
in_img() { check "$1" sh_in "$2"; }
fails() { ! "$@"; }

H=/host
CONF=$H/etc/v3compat-runtime/config.toml
DROPIN=$H/etc/k0s/containerd.d/50-v3compat-runtime.toml
DEST=$H/opt/v3compat
BIN=$H/usr/local/bin/v3compat-runtime

# Fresh install; DR_OPTS has a quote and a backslash.
check "fresh install exits 0" inst NATIVE_NODE=false 'DR_OPTS=-x "a b" \c' CACHE_ROOT=/var/cache/v3c -- --once
in_img "fresh: four files" "test -x $BIN && test -f $CONF && test -f $DROPIN && test -x $DEST/bin/v3compat-run"
in_img "fresh: native = false, once" "[ \"\$(grep -c '^native' $CONF)\" = 1 ] && grep -qx 'native = false' $CONF"
in_img "fresh: dr_opts escaped" "grep -Fqx 'dr_opts = \"-x \\\"a b\\\" \\\\c\"' $CONF"
in_img "fresh: cache_root" "grep -Fqx 'cache_root = \"/var/cache/v3c\"' $CONF"
in_img "fresh: v3 drop-in with host binary path and SystemdCgroup" "grep -Fqx 'version = 3' $DROPIN && grep -Fqx '    BinaryName = \"/usr/local/bin/v3compat-runtime\"' $DROPIN && grep -Fq 'io.containerd.cri.v1.runtime\".containerd.runtimes.v3compat]' $DROPIN && grep -Fqx '    SystemdCgroup = true' $DROPIN"
in_img "fresh: no temp files left" "[ -z \"\$(find $H/etc $H/usr $H/opt -name '*.new' -o -name '.config.*' -o -name '*.toml.*')\" ]"
in_img "fresh: bundle owned by root" "[ -z \"\$(find $DEST ! -user 0)\" ]"
in_img "fresh: bundle not group/other-writable" "[ -z \"\$(find $DEST -perm /022)\" ]"
in_img "fresh: CACHE_ROOT is root 0755" "[ \"\$(stat -c '%u %a' $H/var/cache/v3c)\" = '0 755' ]"
in_img "fresh: no .prev" "[ ! -e $DEST.prev ]"

# The real TOML must decode to the original string: check with the Go loader's parser is done in
# unit tests; here compare the raw line only.

# Same content again: bundle untouched.
ino=$(sh_in "stat -c %i $DEST")
check "second run exits 0" inst NATIVE_NODE=false 'DR_OPTS=-x "a b" \c' CACHE_ROOT=/var/cache/v3c -- --once
check "second run: same inode" test "$(sh_in "stat -c %i $DEST")" = "$ino"
in_img "second run: no .prev" "[ ! -e $DEST.prev ] && [ ! -e $DEST.new ]"
in_img "second run: config unchanged keys" "[ \"\$(grep -c '^dr_opts' $CONF)\" = 1 ] && [ \"\$(grep -c '^cache_root' $CONF)\" = 1 ]"

# native, other keys kept, cleared keys removed
sh_in "echo 'log_file = \"/x\"' >> $CONF"
check "native run exits 0" inst NATIVE_NODE=true -- --once
in_img "native: one native = true" "[ \"\$(grep -c '^native' $CONF)\" = 1 ] && grep -qx 'native = true' $CONF"
in_img "native: other keys kept" "grep -qx 'log_file = \"/x\"' $CONF"
in_img "native: empty env clears dr_opts and cache_root" "! grep -q '^dr_opts\|^cache_root' $CONF"
in_img "native: existing CACHE_ROOT dir kept" "test -d $H/var/cache/v3c"
check "CACHE_ROOT existing is not chmodded" inst CACHE_ROOT=/var/cache/v3c -- --once
sh_in "chmod 0700 $H/var/cache/v3c"
check "CACHE_ROOT existing 0700 left alone" inst CACHE_ROOT=/var/cache/v3c -- --once
in_img "cache dir still 0700" "[ \"\$(stat -c %a $H/var/cache/v3c)\" = 700 ]"

# Changed bundle: simulate by editing the installed copy.
sh_in "echo changed >> $DEST/share/doc/v3compat/NOTICE 2>/dev/null || echo changed >> $DEST/CHANGED; echo old > $DEST/OLDMARK"
ino=$(sh_in "stat -c %i $DEST")
check "changed bundle run exits 0" inst -- --once
in_img "changed: .prev holds the previous tree" "test -f $DEST.prev/OLDMARK && [ \"\$(stat -c %i $DEST.prev)\" = $ino ]"
in_img "changed: new tree installed" "test ! -e $DEST/OLDMARK && test -x $DEST/bin/v3compat-run"
in_img "changed: still root-owned, not g/o-writable" "[ -z \"\$(find $DEST ! -user 0)\" ] && [ -z \"\$(find $DEST -perm /022)\" ]"
sh_in "echo old2 > $DEST/OLD2MARK"
check "second change exits 0" inst -- --once
in_img "second change: .prev replaced" "test -f $DEST.prev/OLD2MARK && test ! -e $DEST.prev/OLDMARK"

# containerd config version 2: old syntax, no SystemdCgroup; switching back to 3 rewrites it.
check "CONTAINERD_CONFIG_VERSION=2 exits 0" inst CONTAINERD_CONFIG_VERSION=2 -- --once
in_img "v2 drop-in" "grep -Fqx 'version = 2' $DROPIN && grep -Fq 'io.containerd.grpc.v1.cri\".containerd.runtimes.v3compat]' $DROPIN && grep -Fqx '    BinaryName = \"/usr/local/bin/v3compat-runtime\"' $DROPIN && ! grep -q SystemdCgroup $DROPIN"
check "CONTAINERD_CONFIG_VERSION=3 exits 0" inst CONTAINERD_CONFIG_VERSION=3 -- --once
in_img "v3 drop-in again" "grep -Fqx 'version = 3' $DROPIN && grep -Fqx '    SystemdCgroup = true' $DROPIN"

# Bad input installs nothing.
docker run --rm -v "$vol:/host" "$image" --uninstall --once >/dev/null
sh_in "rm -rf $H/etc $H/usr $H/opt $H/var"
check "NATIVE_NODE=bogus fails" fails inst NATIVE_NODE=bogus -- --once
check "CONTAINERD_CONFIG_VERSION=4 fails" fails inst CONTAINERD_CONFIG_VERSION=4 -- --once
check "MODE=bogus fails" fails inst MODE=bogus -- --once
check "unknown argument fails" fails inst -- --bogus
check "newline in DR_OPTS fails" fails inst 'DR_OPTS=a
b' -- --once
in_img "bad input: nothing installed" "[ -z \"\$(ls -A $H)\" ]"

# Uninstall keeps the config.
check "reinstall exits 0" inst -- --once
sh_in "mkdir -p $DEST.prev $DEST.new"
check "MODE=uninstall exits 0" inst MODE=uninstall -- --once
in_img "uninstall: binary, bundle, .prev, .new, drop-in gone" "! test -e $BIN && ! test -e $DEST && ! test -e $DEST.prev && ! test -e $DEST.new && ! test -e $DROPIN"
in_img "uninstall: config kept" "test -f $CONF"
check "--uninstall alias exits 0" inst -- --uninstall --once

# Default run keeps running and stops on SIGTERM promptly.
cid=$(docker run -d -v "$vol:/host" "$image")
sleep 4
check "default run keeps running" test "$(docker inspect -f '{{.State.Running}}' "$cid")" = true
start=$(date +%s)
docker stop "$cid" >/dev/null
check "stops on SIGTERM within 5 s" test $(( $(date +%s) - start )) -lt 5
check "exited 0" test "$(docker inspect -f '{{.State.ExitCode}}' "$cid")" = 0

exit $fail
