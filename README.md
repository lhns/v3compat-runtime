# v3compat-runtime

Runs container images built for x86-64-v3 (AVX2, BMI2, FMA, LZCNT, MOVBE) on CPUs without it, through
an OCI runtime wrapper for containerd + runc (Kubernetes). The translation is
[lhns/v3compat](https://github.com/lhns/v3compat): a DynamoRIO client behind a static launcher,
`v3compat-run`. Its docs hold the details this README does not repeat.

**Translation is a slow fallback, not a fast path.** Every process in a wrapped container starts under
DynamoRIO, and `kubectl exec` and exec probes too: several seconds on Ivy Bridge for duckdb. Prefer HTTP or TCP
probes and a generous startup probe. Use it to let a pod run on an old node at all.

## How it works

Pods opt in with a RuntimeClass whose handler is `v3compat`; other pods never see the wrapper. For each
container created through that handler, `v3compat-runtime`:

1. **Translates by default**, on every CPU. It passes the container through natively only when the node
   was installed with `install.sh --native` (`native = true`, written for nodes labelled as v3, ADR 0004)
   **and** CPUID confirms x86-64-v3. If `native = true` on a CPU without v3, it translates and logs a
   warning: translation is correct on any CPU, native on an old one silently miscomputes lzcnt/tzcnt;
2. edits the bundle's `config.json`: bind-mounts the bundle read-only at `/v3compat`, prefixes
   `process.args` with `/v3compat/bin/v3compat-run --`, and sets the annotation `v3compat.wrapped=true`.
   An init process (`/sbin/docker-init`, `/run/podman-init`, `/dev/init`) stays native and runs the
   launcher as its child. Pod sandbox (pause) containers are not wrapped;
3. gives `exec` into a wrapped container (`kubectl exec`, exec probes) the same prefix in its process file;
4. `execve`s the real runtime with the original arguments. Every other subcommand does only that.
   DynamoRIO follows `exec` into child processes.

**Fail-closed on translating nodes** (v3compat ADR 0004, amendment 2026-11-22). If `create`, `run` or `exec`
cannot set up translation (invalid config, missing bundle, unparsable spec, no process file to rewrite), the
wrapper exits 1 without starting the real runtime: run natively there, a v3 program dies with SIGILL or, for
lzcnt/tzcnt, silently computes bsr/bsf. The reason goes to stderr and to the runtime's `--log` file, where
containerd reads it for "OCI runtime create failed" in the pod's events. An invalid config refuses on every node,
as the `native` flag cannot be read. A native node (`native = true` and CPUID x86-64-v3) never reads the bundle or
spec, so nothing there can fail. Every other subcommand (`state`, `start`, `kill`, `delete`, ...) passes through
unchanged, so existing containers can always be managed. Logs go to syslog (`v3compat-runtime`) and
`/var/log/v3compat-runtime.log`; stdout is never written.

### Translation cache mount

If `cache_root` is set and the image reference (annotation `io.kubernetes.cri.image-name`) ends in
`@sha256:<64 hex>`, and `<cache_root>/sha256-<hex>` is a directory, that directory is bind-mounted
read-only at `/v3compat-cache` (executable: DynamoRIO may map entries). Otherwise nothing is mounted;
`cache_root` itself never is. No environment variable is set: the launcher's cache option is later work.

## Exit statuses

Of the wrapped process, from `v3compat-run` (details: v3compat `docs/plan.md`, "Launcher and exec follow").
Application statuses and signals pass through.

| status | meaning for a pod |
| --- | --- |
| 40 | DynamoRIO cannot handle a signal the program got |
| 86 | the program hit an instruction form that is not on the audited list; it was refused, not guessed |
| 87 | the launcher refused the program: setuid/setgid/file capabilities where privileges would change, unreadable, or 32-bit |
| 89 | a vfork child was killed holding a DynamoRIO lock; the process ends |
| 125 | launcher failure |
| 126, 127 | not executable, not found, as in a shell |

An exec refused deeper in the process tree fails with EPERM or ENOEXEC and a message on stderr instead.

## Limits

- The image needs no glibc: the launcher and DynamoRIO need nothing from it (v3compat measured static,
  static-PIE and musl programs). The bundle's own self-containment is checked by v3compat's
  `release.yml`; this README claims no more.
- Every process pays DynamoRIO's startup, exec probes included.
- `/proc/self/maps` lists DynamoRIO, and `DYNAMORIO_*` variables are visible to the program.
- Setuid, setgid and file-capability programs and 32-bit children are refused (87, or an errno below the top process).
- A vfork child killed while holding a DynamoRIO lock ends the process with 89.
- Not supported: CPUs without AVX, automatic detection of images that need v3 (opt in by RuntimeClass).
- Only the runc command line is tested; the argument handling is runtime-neutral.

## Build and install

v3compat and DynamoRIO may be redistributed; their notices are in the bundle's `share/doc/v3compat/`.

**No v3compat release exists yet**: the owner cuts the first once the open crashes are fixed. Until then build the
bundle with `bundle/get.sh --source` (the commit pinned in `bundle/pin.env`, the one CI tests).

    bundle/get.sh                    # the release pinned in bundle/pin.env; refuses while none is pinned
    bundle/get.sh --source           # build the pinned commit: cmake, gcc, g++, git, patch, zlib1g-dev; ~10 min first time
    bundle/get.sh --tarball PATH     # a local release tarball; prints its sha256
    go build -o v3compat-runtime ./cmd/v3compat-runtime
    sudo install/install.sh          # binary, bundle, config, k0s handler drop-in; native = false
    sudo install/install.sh --native # for nodes labelled x86-64-v3: native = true
    sudo install/install.sh --uninstall

`install.sh` always sets `native` in `/etc/v3compat-runtime/config.toml`: written explicitly in a new
file; in an existing file only that line is rewritten (other keys stay; the config has no tables).

Install the handler on every node that may run opted-in pods: a RuntimeClass whose handler is missing fails
the pod.

### Installer image (DaemonSet)

`ghcr.io/lhns/v3compat-runtime-installer` installs onto the node it runs on, kata-deploy style: run it as a
DaemonSet with the host root mounted at `HOST_ROOT` (read-write). Tags: `main` (pushes to main) and `vX.Y.Z`
(the git tag). It runs as root, installs, then sleeps (so the pod stays `Running`; `--once` exits instead).
It does **not** restart k0s or containerd; the handler loads at the next restart (a reboot rollout). New GHCR
packages start private: make the package public, or give the cluster a pull secret.

| env | default | meaning |
| --- | --- | --- |
| `HOST_ROOT` | `/host` | where the host root is mounted |
| `NATIVE_NODE` | `false` | `true` or `false` (else: error, nothing installed); `native` in the config |
| `DR_OPTS` | empty | config `dr_opts`; empty clears it |
| `CACHE_ROOT` | empty | config `cache_root`, a host path; empty clears it. Created root-owned 0755 if missing (an existing directory is left alone) |
| `CONTAINERD_CONFIG_VERSION` | `3` | `2` or `3`, the drop-in's containerd config version (see "k0s / containerd") |
| `MODE` | `install` | `install` or `uninstall` (else: error); argument `--uninstall` is an alias |

Files, under `HOST_ROOT`: `/usr/local/bin/v3compat-runtime`, `/opt/v3compat`, `/etc/v3compat-runtime/config.toml`,
`/etc/k0s/containerd.d/50-v3compat-runtime.toml`. Paths written into them are host paths. Uninstall removes all
but the config.

It runs the same `install/install.sh`, which also takes `--root DIR` (prefix for every path written),
`--dr-opts STR`, `--cache-root PATH` and `--containerd-config 2|3` (an empty string clears the key; values are
written as escaped TOML strings, newlines refused). Unknown config keys are ignored by the wrapper, so
`dr_opts` is harmless before the wrapper reads it.

Re-running is idempotent and every file is replaced by rename. An identical bundle is left alone: containers
bind-mount `/opt/v3compat`, and the pod restarts on every node reboot. A changed bundle is copied to
`/opt/v3compat.new`, the old tree is renamed to `/opt/v3compat.prev` (replacing an older `.prev`) and the new
one renamed into place; a rename keeps the old tree valid for containers that already mounted it. So one
previous version survives until the next change. A reboot rollout is the supported way to upgrade.

**Replacing an old SDE install.** The installer does not touch it. Remove it by hand: run the old
version's `install.sh --uninstall`, or delete `/usr/local/bin/sde-runtime`, `/opt/sde-bundle`,
`/etc/sde-runtime` and `/etc/k0s/containerd.d/50-sde-runtime.toml`, and the `sde` RuntimeClass.

## k0s / containerd

`install/install.sh` writes `/etc/k0s/containerd.d/50-v3compat-runtime.toml`. By default it is containerd 2 / config
`version = 3` syntax with the systemd cgroup driver (`--containerd-config 3`):

```toml
version = 3
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.v3compat]
  runtime_type = "io.containerd.runc.v2"
  [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.v3compat.options]
    BinaryName = "/usr/local/bin/v3compat-runtime"
    SystemdCgroup = true
```

`--containerd-config 2` writes the config `version = 2` syntax instead (plugin `io.containerd.grpc.v1.cri`, no
`SystemdCgroup`). Use it when the node's containerd config is version 2.
Restart k0s or containerd afterwards. Then:

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: v3compat
handler: v3compat
---
# in the pod spec
spec:
  runtimeClassName: v3compat
```

## Configuration

`/etc/v3compat-runtime/config.toml`, all keys optional (defaults shown). A missing file means defaults; an
invalid one refuses opted-in containers (above).

```toml
real_runtime = "/var/lib/k0s/bin/runc"   # the runtime to exec
bundle_path  = "/opt/v3compat"           # host path, mounted read-only at /v3compat
init_paths   = ["/sbin/docker-init", "/run/podman-init", "/dev/init"]
log_file     = "/var/log/v3compat-runtime.log"
native       = false                     # true: run unwrapped if CPUID shows x86-64-v3 (install.sh --native)
cache_root   = ""                        # translation cache directories, one per image digest; empty: off
```

## Development

    go vet ./... && go test ./...
    GOOS=linux GOARCH=amd64 go build ./cmd/v3compat-runtime

CI also runs `test/e2e.sh`: the wrapper in front of runc with a bundle built from the pinned v3compat commit
(`bundle/pin.env`), covering translated and native runs, exec, refusal without a bundle, status 87 and the cache mount. The runner has AVX2, where
the client emulates anyway; Ivy Bridge coverage comes from v3compat's lab, not from here.

## License

The wrapper is licensed under [Apache-2.0](LICENSE). v3compat, DynamoRIO and avxemu keep their own licenses
(in the bundle's `share/doc/v3compat/`).
