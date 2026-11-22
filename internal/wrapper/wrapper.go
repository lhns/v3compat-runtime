// Package wrapper decides what to do with one runtime invocation. On a node that translates, a
// problem setting up translation refuses the container (ADR 0004, amendment 2026-11-22): run
// natively on a CPU without x86-64-v3 it crashes or computes wrong results. Every other
// invocation passes through unchanged.
package wrapper

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
// returns the path of the real runtime to exec with args unchanged. An error means the process
// must not start: the caller exits non-zero instead of exec'ing the runtime (see Refuse).
func Prepare(args []string, d Deps) (string, error) {
	d = d.withDefaults()
	cmd := argv.Parse(args)
	setsUp := cmd.Sub == "create" || cmd.Sub == "run" || cmd.Sub == "exec"
	cfg, err := config.Load(d.ConfigPath)
	if err != nil {
		if setsUp {
			// Without the config the node's native flag is unknown: not a native node.
			d.Logf(config.Defaults().LogFile, "error: %v: refusing %s", err, cmd.Sub)
			return "", fmt.Errorf("invalid config: %w", err)
		}
		d.Logf(config.Defaults().LogFile, "error: %v: passing through, nothing wrapped", err)
		return cfg.RealRuntime, nil
	}
	logf := func(format string, a ...any) { d.Logf(cfg.LogFile, format, a...) }
	switch cmd.Sub {
	case "create", "run":
		err = create(cmd, cfg, d, logf)
	case "exec":
		err = execCmd(cmd, cfg, d, logf)
	}
	if err != nil {
		logf("error: %v: refused, this node translates", err)
		return "", err
	}
	return cfg.RealRuntime, nil
}

// Refuse records why Prepare refused in the runtime's --log file, where containerd reads the
// last error for "OCI runtime create failed"; stderr alone may not reach the pod's events.
func Refuse(args []string, err error) {
	path, format := "", "text"
	g := argv.Parse(args).Global
	for i := 0; i < len(g); i++ {
		name, val, ok := strings.Cut(strings.TrimLeft(g[i], "-"), "=")
		if !ok && i+1 < len(g) && (name == "log" || name == "log-format") {
			i++
			val = g[i]
		}
		switch name {
		case "log":
			path = val
		case "log-format":
			format = val
		}
	}
	if path == "" {
		return
	}
	f, ferr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	msg := "v3compat-runtime: " + err.Error()
	now := time.Now().Format(time.RFC3339Nano)
	if format == "json" {
		b, _ := json.Marshal(map[string]string{"level": "error", "msg": msg, "time": now})
		fmt.Fprintf(f, "%s\n", b)
	} else {
		fmt.Fprintf(f, "time=%q level=error msg=%q\n", now, msg)
	}
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

func create(cmd argv.Cmd, cfg config.Config, d Deps, logf func(string, ...any)) error {
	id := cmd.ID()
	wrap, vetoed := translate(cfg, d)
	if !wrap {
		logf("info: %s: native=true and the host has x86-64-v3, running natively", id)
		return nil
	}
	if vetoed {
		logf("warn: %s: native=true but the CPU lacks x86-64-v3 (CPUID): translating", id)
	}
	bundle := cmd.Bundle
	if bundle == "" {
		var err error
		if bundle, err = d.Getwd(); err != nil {
			return fmt.Errorf("%s: no bundle: %w", id, err)
		}
	}
	cfgPath := filepath.Join(bundle, "config.json")
	if sb, err := spec.IsSandbox(cfgPath); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	} else if sb {
		return nil // pod sandbox (pause) is baseline x86-64
	}
	if _, err := os.Stat(filepath.Join(cfg.BundlePath, "bin", "v3compat-run")); err != nil {
		return fmt.Errorf("%s: v3compat bundle missing at %s: %w", id, cfg.BundlePath, err)
	}
	o := options(cfg)
	var image string
	o.CacheDir, image = cacheDir(cfg, cfgPath)
	changed, err := spec.RewriteConfig(cfgPath, o)
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", id, err)
	case changed && cfg.CacheRoot != "":
		logf("info: %s: wrapped with v3compat-run, bundle %s, image %q, cache %q (empty: none mounted)", id, cfg.BundlePath, image, o.CacheDir)
	case changed:
		logf("info: %s: wrapped with v3compat-run, bundle %s", id, cfg.BundlePath)
	}
	return nil
}

func execCmd(cmd argv.Cmd, cfg config.Config, d Deps, logf func(string, ...any)) error {
	id := cmd.ID()
	if id == "" {
		return nil // the runtime reports the usage error
	}
	if wrap, _ := translate(cfg, d); !wrap {
		return nil // nothing was wrapped on this host
	}
	// A wrapped container has the marker; asking the runtime for the bundle is the only way
	// to read it, so exec in an unwrapped container costs one `state` call.
	bundle, err := d.State(cfg.RealRuntime, cmd.Global, id)
	if err != nil {
		return fmt.Errorf("exec %s: cannot find bundle: %w", id, err)
	}
	wrapped, err := spec.IsWrapped(filepath.Join(bundle, "config.json"))
	if err != nil {
		return fmt.Errorf("exec %s: %w", id, err)
	}
	if !wrapped {
		return nil
	}
	if cmd.Process == "" {
		return fmt.Errorf("exec %s: no --process file (inline args) to wrap", id)
	}
	changed, err := spec.RewriteProcess(cmd.Process, options(cfg))
	switch {
	case err != nil:
		return fmt.Errorf("exec %s: %w", id, err)
	case changed:
		logf("info: exec %s: wrapped with v3compat-run", id)
	}
	return nil
}
