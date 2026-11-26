#!/bin/sh
# Entrypoint of the installer image (a DaemonSet): installs or uninstalls into the host
# filesystem mounted at $HOST_ROOT, then sleeps so the pod stays Running. Env:
#   HOST_ROOT    host root mount (default /host)
#   NATIVE_NODE  true|false (default false): install.sh --native
#   DR_OPTS      config key dr_opts (empty clears it)
#   CACHE_ROOT   config key cache_root, a host path (empty clears it); created if missing
#   CONTAINERD_CONFIG_VERSION  2|3 (default 3): install.sh --containerd-config
#   MODE         install (default) | uninstall
# Arguments: --uninstall (= MODE=uninstall), --once (exit instead of sleeping, for tests).
# Does not restart k0s/containerd.
set -eu

app=/app
mode=${MODE:-install}
once=0
for a in "$@"; do
    case $a in
        --uninstall) mode=uninstall ;;
        --once) once=1 ;;
        *) echo "unknown argument: $a" >&2; exit 2 ;;
    esac
done

case ${NATIVE_NODE:-false} in
    true|false) ;;
    *) echo "NATIVE_NODE must be true or false, got '$NATIVE_NODE'" >&2; exit 2 ;;
esac
case ${CONTAINERD_CONFIG_VERSION:-3} in
    2|3) ;;
    *) echo "CONTAINERD_CONFIG_VERSION must be 2 or 3, got '$CONTAINERD_CONFIG_VERSION'" >&2; exit 2 ;;
esac
case $mode in
    install|uninstall) ;;
    *) echo "MODE must be install or uninstall, got '$mode'" >&2; exit 2 ;;
esac

root=${HOST_ROOT:-/host}
if [ "$mode" = uninstall ]; then
    "$app/install/install.sh" --root "$root" --uninstall
else
    native=
    [ "${NATIVE_NODE:-false}" = true ] && native=--native
    # $native is empty or one word.
    # shellcheck disable=SC2086
    "$app/install/install.sh" --root "$root" $native \
        --containerd-config "${CONTAINERD_CONFIG_VERSION:-3}" \
        --dr-opts "${DR_OPTS:-}" --cache-root "${CACHE_ROOT:-}"
fi

[ "$once" = 0 ] || exit 0
# sleep as PID 1 ignores SIGTERM; trap so the pod stops at once.
trap 'exit 0' TERM INT
while :; do sleep 3600 & wait $!; done
