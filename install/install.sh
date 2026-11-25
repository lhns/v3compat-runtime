#!/bin/sh
# Installs the v3compat-runtime wrapper, the v3compat bundle and the k0s containerd handler `v3compat`.
#   install/install.sh [--native] [--dr-opts STR] [--cache-root PATH] [--binary PATH] [--bundle DIR]
#                      [--no-k0s] [--root DIR]
#                      [--containerd-config 2|3]
#   install/install.sh --uninstall [--root DIR]
# --native sets `native = true` in the config: containers run unwrapped on a CPU that reports
# x86-64-v3. Use it only on nodes labelled v3 (ADR 0004). Without it `native = false` is written.
# --dr-opts and --cache-root set `dr_opts` and `cache_root`; an empty value clears the key. A key
# that is not given is left alone. An existing config keeps its other keys; only these lines are rewritten.
# --cache-root creates PATH (root-owned, 0755) if it does not exist.
# --containerd-config picks the drop-in's containerd config version: 3 (default; adds SystemdCgroup = true)
# or 2 (the old syntax, no SystemdCgroup).
# --root DIR prefixes every path written (a host mounted at DIR); paths written INTO files stay host paths.
# Re-running is idempotent: files are replaced by rename, and an identical binary, drop-in or
# bundle is left alone (containers bind-mount /opt/v3compat). A changed bundle keeps the previous
# tree as /opt/v3compat.prev until the next change; renaming leaves it valid for running containers.
# Does not touch an old sde-runtime install. Does not restart anything; restart k0s (or containerd)
# afterwards to load the handler.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
binary=$here/../v3compat-runtime
bundle=$here/../bundle/out/v3compat
native=false
k0s=1
cfgver=3
uninstall=0
root=
dr_opts= set_dr=0
cache_root= set_cache=0

usage() {
    echo "usage: $0 [--native] [--dr-opts STR] [--cache-root PATH] [--binary PATH] [--bundle DIR] [--no-k0s] [--root DIR] [--containerd-config 2|3] | --uninstall [--root DIR]" >&2
    exit 2
}

while [ $# -gt 0 ]; do
    case $1 in --binary|--bundle|--dr-opts|--cache-root|--root|--containerd-config) [ $# -ge 2 ] || usage ;; esac
    case $1 in
        --binary) binary=$2; shift 2 ;;
        --bundle) bundle=$2; shift 2 ;;
        --native) native=true; shift ;;
        --dr-opts) dr_opts=$2; set_dr=1; shift 2 ;;
        --cache-root) cache_root=$2; set_cache=1; shift 2 ;;
        --root) root=$2; shift 2 ;;
        --containerd-config) cfgver=$2; shift 2 ;;
        --no-k0s) k0s=0; shift ;;
        --uninstall) uninstall=1; shift ;;
        *) usage ;;
    esac
done
root=${root%/}
case $cfgver in 2|3) ;; *) echo "--containerd-config must be 2 or 3" >&2; exit 2 ;; esac

BIN=$root/usr/local/bin/v3compat-runtime
BUNDLE_DEST=$root/opt/v3compat
CONF_DIR=$root/etc/v3compat-runtime
DROPIN=$root/etc/k0s/containerd.d/50-v3compat-runtime.toml
# What the files say, as the host sees it.
HOST_BIN=/usr/local/bin/v3compat-runtime

# TOML basic strings: no newline or other control characters (tab is fine).
check_str() { # NAME VALUE
    case $2 in *'
'*) echo "$1: newline not allowed" >&2; exit 2 ;; esac
    [ -z "$(printf %s "$2" | LC_ALL=C tr -d '\040-\176\200-\377\011')" ] || { echo "$1: control character not allowed" >&2; exit 2; }
}
# Escapes \ and " by hand: busybox sed mishandles the usual expression.
toml_str() {
    rest=$1 out=
    while [ -n "$rest" ]; do
        tail=${rest#?} c=${rest%"$tail"}
        case $c in \\|'"') out=$out\\$c ;; *) out=$out$c ;; esac
        rest=$tail
    done
    printf '"%s"' "$out"
}
check_str --dr-opts "$dr_opts"
check_str --cache-root "$cache_root"

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }

if [ "$uninstall" = 1 ]; then
    rm -f "$DROPIN" "$BIN"
    rm -rf "$BUNDLE_DEST" "$BUNDLE_DEST.prev" "$BUNDLE_DEST.new"
    # Keep the config: it may have been edited. rmdir removes the directory only if it is empty.
    rmdir "$CONF_DIR" 2>/dev/null || true
    echo "removed; restart k0s/containerd to drop the handler. Pods using the RuntimeClass fail until it is deleted."
    exit 0
fi

[ -x "$binary" ] || { echo "no binary at $binary (go build -o v3compat-runtime ./cmd/v3compat-runtime)" >&2; exit 1; }
[ -x "$bundle/bin/v3compat-run" ] || { echo "no v3compat bundle at $bundle (run bundle/get.sh)" >&2; exit 1; }

# Writes the temp file $1 over $2, or drops it if the content is already there. Mode of $1 is kept.
replace() {
    if [ -f "$2" ] && cmp -s "$1" "$2"; then rm -f "$1"; else mv "$1" "$2"; fi
}

# Digest of a tree's names, modes, owners, link targets and file contents.
fingerprint() {
    (cd "$1" && {
        find . -exec stat -c '%n %a %u %g %F %N' {} +
        find . -type f -exec sha256sum {} +
    } | LC_ALL=C sort | sha256sum)
}

mkdir -p "$(dirname "$BIN")"
install -m 0755 "$binary" "$BIN.new"
replace "$BIN.new" "$BIN"

mkdir -p "$(dirname "$BUNDLE_DEST")"
rm -rf "$BUNDLE_DEST.new"
cp -a "$bundle" "$BUNDLE_DEST.new"
# Every translated container execs this: root-owned, not writable by the user who built the bundle.
chown -R 0:0 "$BUNDLE_DEST.new"
chmod -R go-w,a+rX "$BUNDLE_DEST.new"
# Compare after the chown, so a user-owned source still matches the root-owned install.
if [ -d "$BUNDLE_DEST" ] && [ "$(fingerprint "$BUNDLE_DEST.new")" = "$(fingerprint "$BUNDLE_DEST")" ]; then
    rm -rf "$BUNDLE_DEST.new"
    echo "bundle unchanged"
else
    if [ -e "$BUNDLE_DEST" ]; then
        rm -rf "$BUNDLE_DEST.prev"
        mv "$BUNDLE_DEST" "$BUNDLE_DEST.prev"
    fi
    mv "$BUNDLE_DEST.new" "$BUNDLE_DEST"
fi

if [ "$set_cache" = 1 ] && [ -n "$cache_root" ] && [ ! -e "$root$cache_root" ]; then
    (umask 022; mkdir -p "$root$cache_root")
fi

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
# Set the given keys in a new or existing config: drop any active line for each and append ours
# (top-level keys, so position does not matter as long as no table precedes them; the shipped
# config has none).
keys=native
[ "$set_dr" = 1 ] && keys="$keys|dr_opts"
[ "$set_cache" = 1 ] && keys="$keys|cache_root"
tmp=$(mktemp "$CONF_DIR/.config.XXXXXX")
grep -Ev "^[[:space:]]*($keys)[[:space:]]*=" "$conf" > "$tmp" || true
echo "native = $native" >> "$tmp"
[ -n "$dr_opts" ] && echo "dr_opts = $(toml_str "$dr_opts")" >> "$tmp"
[ -n "$cache_root" ] && echo "cache_root = $(toml_str "$cache_root")" >> "$tmp"
chmod --reference="$conf" "$tmp" 2>/dev/null || chmod 0644 "$tmp"
replace "$tmp" "$conf"

if [ "$k0s" = 1 ]; then
    mkdir -p "$(dirname "$DROPIN")"
    # Not *.toml while temporary: k0s reads every .toml in the directory.
    tmp=$(mktemp "$DROPIN.XXXXXX")
    if [ "$cfgver" = 3 ]; then
        cat > "$tmp" <<TOML
version = 3
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.v3compat]
  runtime_type = "io.containerd.runc.v2"
  [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.v3compat.options]
    BinaryName = "$HOST_BIN"
    SystemdCgroup = true
TOML
    else
        cat > "$tmp" <<TOML
version = 2
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.v3compat]
  runtime_type = "io.containerd.runc.v2"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.v3compat.options]
    BinaryName = "$HOST_BIN"
TOML
    fi
    chmod 0644 "$tmp"
    replace "$tmp" "$DROPIN"
fi
echo "installed; restart k0s/containerd to load the handler."
