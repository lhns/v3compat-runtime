#!/bin/sh
# Checks install/install.sh: fresh install, --native on an existing config, --no-k0s, --uninstall,
# and refusal without a bundle. The fake bundle is owned by a non-root user and has a
# group-writable file, so a bundle copied with its ownership fails the ownership check.
#   sudo test/install.sh
# Writes the real system paths (/usr/local/bin, /opt/v3compat, /etc/v3compat-runtime, /etc/k0s):
# run it on a disposable machine such as a CI runner.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
install_sh=$here/../install/install.sh
BIN=/usr/local/bin/v3compat-runtime
DEST=/opt/v3compat
CONF=/etc/v3compat-runtime/config.toml
DROPIN=/etc/k0s/containerd.d/50-v3compat-runtime.toml

w=$(mktemp -d); chmod 755 "$w"
trap 'rm -rf "$w"' EXIT
fail=0

# check NAME COMMAND... ; COMMAND runs in the shell, ok when it succeeds
check() {
    name=$1; shift
    if "$@" >"$w/out" 2>&1; then echo "ok   $name"; else
        echo "FAIL $name"; sed 's/^/  out: /' "$w/out"; fail=1
    fi
}

# No output from the command means ok (for find-style checks).
empty() { [ -z "$("$@")" ]; }
active_native() { grep -c '^[[:space:]]*native[[:space:]]*=' "$CONF" || true; }
one_native() { [ "$(active_native)" = 1 ]; }
has() { grep -Fq -- "$2" "$1"; }
fails() { ! "$@"; }

cp /bin/true "$w/binary"
mkdir -p "$w/bundle/bin" "$w/empty"
cp /bin/true "$w/bundle/bin/v3compat-run"
echo data > "$w/bundle/data"
chmod 664 "$w/bundle/data"
chown -R 65534:65534 "$w/bundle"

run() { "$install_sh" --binary "$w/binary" --bundle "$w/bundle" "$@"; }

rm -rf "$CONF" "$DROPIN" "$DEST" "$BIN"
check "fresh install exits 0" run
check "fresh: one native line" one_native
check "fresh: native = false" has "$CONF" "native = false"
check "fresh: binary executable" test -x "$BIN"
check "fresh: bundle launcher executable" test -x "$DEST/bin/v3compat-run"
check "fresh: bundle data kept" test -f "$DEST/data"
check "fresh: bundle owned by root" empty find "$DEST" ! -user 0
check "fresh: bundle not group/other-writable" empty find "$DEST" -perm /022
check "fresh: drop-in binary" has "$DROPIN" 'BinaryName = "/usr/local/bin/v3compat-runtime"'
check "fresh: drop-in handler" has "$DROPIN" 'runtimes.v3compat]'

echo 'cache_root = "/x"' >> "$CONF"
check "--native exits 0" run --native
check "--native: one native line" one_native
check "--native: native = true" has "$CONF" "native = true"
check "--native: other keys kept" has "$CONF" 'cache_root = "/x"'

rm -f "$DROPIN"
check "--no-k0s exits 0" run --no-k0s
check "--no-k0s: no drop-in" test ! -e "$DROPIN"

check "--uninstall exits 0" "$install_sh" --uninstall
check "uninstall: binary gone" test ! -e "$BIN"
check "uninstall: bundle gone" test ! -e "$DEST"
check "uninstall: drop-in gone" test ! -e "$DROPIN"
check "uninstall: config kept" test -f "$CONF"

check "no bundle fails" fails "$install_sh" --binary "$w/binary" --bundle "$w/empty"

exit $fail
