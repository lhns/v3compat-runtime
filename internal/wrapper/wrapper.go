// Package wrapper decides what to do with one runtime invocation. It never fails: on any
// problem it logs and leaves the invocation unchanged, so the real runtime sees the original.
package wrapper

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lhns/v3compat-runtime/internal/argv"
	"github.com/lhns/v3compat-runtime/internal/config"
	"github.com/lhns/v3compat-runtime/internal/spec"
)

type Deps struct {
	ConfigPath string
	// HostV3 reports whether CPUID shows every x86-64-v3 feature. It only vetoes native=true.
	HostV3 func() bool
	// Logf must write neither to stdout nor stderr.
	Logf func(logFile, format string, a ...any)
	// State returns the bundle directory of a container (runtime `state`).
	State func(real string, global []string, id string) (string, error)
	Getwd func() (string, error)
}

// Defaults fills unset dependencies with the real ones (Logf and HostV3 stay caller's job).
func (d Deps) withDefaults() Deps {
	if d.State == nil {
		d.State = runtimeState
	}
	if d.Getwd == nil {
		d.Getwd = os.Getwd
	}
	if d.Logf == nil {
		d.Logf = func(string, string, ...any) {}
	}
	if d.HostV3 == nil {
		d.HostV3 = func() bool { return false }
	}
	return d
}

func runtimeState(real string, global []string, id string) (string, error) {
	out, err := exec.Command(real, append(append([]string{}, global...), "state", id)...).Output()
	if err != nil {
		return "", err
	}
	var s struct {
		Bundle string `json:"bundle"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return "", err
	}
	return s.Bundle, nil
}

// Prepare applies the v3compat rewrite to the files the invocation refers to, if it needs one, and
// returns the path of the real runtime to exec with args unchanged.
func Prepare(args []string, d Deps) string {
	d = d.withDefaults()
	cfg, err := config.Load(d.ConfigPath)
	if err != nil {
		d.Logf(config.Defaults().LogFile, "error: %v: passing through, nothing wrapped", err)
		return cfg.RealRuntime
	}
	logf := func(format string, a ...any) { d.Logf(cfg.LogFile, format, a...) }
	cmd := argv.Parse(args)
	switch cmd.Sub {
	case "create", "run":
		create(cmd, cfg, d, logf)
	case "exec":
		execCmd(cmd, cfg, d, logf)
	}
	return cfg.RealRuntime
}

func options(cfg config.Config) spec.Options {
	return spec.Options{BundlePath: cfg.BundlePath, InitPaths: cfg.InitPaths}
}

// translate decides whether containers run under the launcher. Native needs the node's opt-in
// (native = true) and CPUID agreement: native on a CPU without v3 silently miscomputes lzcnt/tzcnt.
// vetoed reports that native was asked for but CPUID refused.
func translate(cfg config.Config, d Deps) (wrap, vetoed bool) {
	if !cfg.Native {
		return true, false
	}
	if d.HostV3() {
		return false, false
	}
	return true, true
}

// cacheDir returns the host directory of the image's translation cache, or "" with the image name.
func cacheDir(cfg config.Config, cfgPath string) (dir, image string) {
	if cfg.CacheRoot == "" {
		return "", ""
	}
	image, err := spec.ImageName(cfgPath)
	if err != nil {
		return "", ""
	}
	hex, ok := spec.ImageDigest(image)
	if !ok {
		return "", image
	}
	dir = filepath.Join(cfg.CacheRoot, "sha256-"+hex)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		return "", image
	}
	return dir, image
}

func create(cmd argv.Cmd, cfg config.Config, d Deps, logf func(string, ...any)) {
	id := cmd.ID()
	wrap, vetoed := translate(cfg, d)
	if !wrap {
		logf("info: %s: native=true and the host has x86-64-v3, running natively", id)
		return
	}
	if vetoed {
		logf("warn: %s: native=true but the CPU lacks x86-64-v3 (CPUID): translating", id)
	}
	bundle := cmd.Bundle
	if bundle == "" {
		var err error
		if bundle, err = d.Getwd(); err != nil {
			logf("error: %s: no bundle: %v: passing through", id, err)
			return
		}
	}
	cfgPath := filepath.Join(bundle, "config.json")
	if sb, err := spec.IsSandbox(cfgPath); err != nil {
		logf("error: %s: %v: passing through", id, err)
		return
	} else if sb {
		return // pod sandbox (pause) is baseline x86-64
	}
	if _, err := os.Stat(filepath.Join(cfg.BundlePath, "bin", "v3compat-run")); err != nil {
		logf("error: %s: v3compat bundle missing at %s: %v: passing through, the container will probably fail", id, cfg.BundlePath, err)
		return
	}
	o := options(cfg)
	var image string
	o.CacheDir, image = cacheDir(cfg, cfgPath)
	changed, err := spec.RewriteConfig(cfgPath, o)
	switch {
	case err != nil:
		logf("error: %s: %v: passing through", id, err)
	case changed && cfg.CacheRoot != "":
		logf("info: %s: wrapped with v3compat-run, bundle %s, image %q, cache %q (empty: none mounted)", id, cfg.BundlePath, image, o.CacheDir)
	case changed:
		logf("info: %s: wrapped with v3compat-run, bundle %s", id, cfg.BundlePath)
	}
}

func execCmd(cmd argv.Cmd, cfg config.Config, d Deps, logf func(string, ...any)) {
	id := cmd.ID()
	if id == "" {
		return
	}
	if wrap, _ := translate(cfg, d); !wrap {
		return // nothing was wrapped on this host
	}
	// A wrapped container has the marker; asking the runtime for the bundle is the only way
	// to read it, so exec in an unwrapped container costs one `state` call.
	bundle, err := d.State(cfg.RealRuntime, cmd.Global, id)
	if err != nil {
		logf("error: exec %s: cannot find bundle: %v: passing through", id, err)
		return
	}
	wrapped, err := spec.IsWrapped(filepath.Join(bundle, "config.json"))
	if err != nil {
		logf("error: exec %s: %v: passing through", id, err)
		return
	}
	if !wrapped {
		return
	}
	if cmd.Process == "" {
		logf("warn: exec %s: no --process file (inline args): passing through unwrapped", id)
		return
	}
	changed, err := spec.RewriteProcess(cmd.Process, options(cfg))
	switch {
	case err != nil:
		logf("error: exec %s: %v: passing through", id, err)
	case changed:
		logf("info: exec %s: wrapped with v3compat-run", id)
	}
}
