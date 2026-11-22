#!/bin/sh
# Installs the v3compat-runtime wrapper, the v3compat bundle and the k0s containerd handler `v3compat`.
#   install/install.sh [--native] [--binary PATH] [--bundle DIR] [--no-k0s]
#   install/install.sh --uninstall
# --native sets `native = true` in the config: containers run unwrapped on a CPU that reports
# x86-64-v3. Use it only on nodes labelled v3 (ADR 0004). Without it `native = false` is written.
# An existing config keeps its other keys; only the `native` line is rewritten.
# Does not touch an old sde-runtime install. Does not restart anything; restart k0s (or containerd)
# afterwards to load the handler.
set -eu

BIN=/usr/local/bin/v3compat-runtime
BUNDLE_DEST=/opt/v3compat
CONF_DIR=/etc/v3compat-runtime
DROPIN=/etc/k0s/containerd.d/50-v3compat-runtime.toml

here=$(cd "$(dirname "$0")" && pwd)
binary=$here/../v3compat-runtime
bundle=$here/../bundle/out/v3compat
native=false
k0s=1
uninstall=0

while [ $# -gt 0 ]; do
    case $1 in
        --binary) binary=$2; shift 2 ;;
        --bundle) bundle=$2; shift 2 ;;
        --native) native=true; shift ;;
        --no-k0s) k0s=0; shift ;;
        --uninstall) uninstall=1; shift ;;
        *) echo "usage: $0 [--native] [--binary PATH] [--bundle DIR] [--no-k0s] | --uninstall" >&2; exit 2 ;;
    esac
done

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }

if [ "$uninstall" = 1 ]; then
    rm -f "$DROPIN" "$BIN"
    rm -rf "$BUNDLE_DEST"
    # Keep the config: it may have been edited. rmdir removes the directory only if it is empty.
    rmdir "$CONF_DIR" 2>/dev/null || true
    echo "removed; restart k0s/containerd to drop the handler. Pods using the RuntimeClass fail until it is deleted."
    exit 0
fi

[ -x "$binary" ] || { echo "no binary at $binary (go build -o v3compat-runtime ./cmd/v3compat-runtime)" >&2; exit 1; }
[ -x "$bundle/bin/v3compat-run" ] || { echo "no v3compat bundle at $bundle (run bundle/get.sh)" >&2; exit 1; }

install -m 0755 "$binary" "$BIN.new"
mv "$BIN.new" "$BIN"
rm -rf "$BUNDLE_DEST.new"
cp -a "$bundle" "$BUNDLE_DEST.new"
rm -rf "$BUNDLE_DEST"
mv "$BUNDLE_DEST.new" "$BUNDLE_DEST"

mkdir -p "$CONF_DIR"
conf=$CONF_DIR/config.toml
if [ ! -e "$conf" ]; then
    cat > "$conf" <<'CONF'
# All keys are optional; defaults shown, except native, which the installer sets.
# real_runtime = "/var/lib/k0s/bin/runc"
# bundle_path  = "/opt/v3compat"
# log_file     = "/var/log/v3compat-runtime.log"
# cache_root   = ""
CONF
fi
# Set `native` in a new or existing config: drop any active `native =` line and append ours
# (top-level keys, so position does not matter as long as no table precedes it; the shipped
# config has none).
tmp=$(mktemp "$CONF_DIR/.config.XXXXXX")
grep -v '^[[:space:]]*native[[:space:]]*=' "$conf" > "$tmp" || true
echo "native = $native" >> "$tmp"
chmod --reference="$conf" "$tmp" 2>/dev/null || chmod 0644 "$tmp"
mv "$tmp" "$conf"

if [ "$k0s" = 1 ]; then
    mkdir -p "$(dirname "$DROPIN")"
    cat > "$DROPIN" <<'TOML'
version = 2
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.v3compat]
  runtime_type = "io.containerd.runc.v2"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.v3compat.options]
    BinaryName = "/usr/local/bin/v3compat-runtime"
TOML
fi
echo "installed; restart k0s/containerd to load the handler."
