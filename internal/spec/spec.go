// Package spec rewrites OCI config.json and exec process files. Documents are edited as generic
// JSON so fields this package does not know survive the round trip.
package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	AnnotationWrapped = "v3compat.wrapped"
	AnnotationImage   = "io.kubernetes.cri.image-name"
	BundleMount       = "/v3compat"
	Launcher          = "/v3compat/bin/v3compat-run"
	CacheMount        = "/v3compat-cache"
)

type Options struct {
	BundlePath string   // host path of the v3compat bundle, mounted at /v3compat
	CacheDir   string   // host directory mounted read-only at /v3compat-cache; empty for none
	InitPaths  []string // init binaries that stay native and run the launcher as their child
	DROpts     string   // V3COMPAT_DR_OPTS for the container process; empty keeps the container's own
}

func prefix() []string { return []string{Launcher, "--"} }

// imageDigest matches a pinned reference. Only lowercase hex follows "@sha256:", so a match
// holds no path characters.
var imageDigest = regexp.MustCompile(`@sha256:([0-9a-f]{64})$`)

// ImageDigest returns the hex digest of an image reference that ends in @sha256:<64 hex>.
func ImageDigest(image string) (string, bool) {
	m := imageDigest.FindStringSubmatch(image)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// WrapArgs prefixes args with the launcher. It reports false if args are already wrapped
// or empty.
func WrapArgs(args []string, o Options) ([]string, bool) {
	if len(args) == 0 {
		return args, false
	}
	at := 0
	for _, init := range o.InitPaths {
		if args[0] != init {
			continue
		}
		for i, a := range args {
			if a == "--" {
				at = i + 1
				break
			}
		}
		break
	}
	if at >= len(args) || args[at] == Launcher {
		return args, false
	}
	out := append([]string{}, args[:at]...)
	out = append(out, prefix()...)
	return append(out, args[at:]...), true
}

type doc map[string]any

func load(path string) (doc, os.FileMode, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var d doc
	if err := dec.Decode(&d); err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	if d == nil {
		return nil, 0, fmt.Errorf("%s: not a JSON object", path)
	}
	return d, st.Mode().Perm(), nil
}

// save writes atomically: temp file in the same directory, fsync, rename.
func save(path string, d doc, mode os.FileMode) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".v3compat-runtime-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func rewriteArgs(m map[string]any, o Options) (bool, error) {
	raw, ok := m["args"].([]any)
	if !ok || len(raw) == 0 {
		return false, nil
	}
	args := make([]string, len(raw))
	for i, e := range raw {
		s, ok := e.(string)
		if !ok {
			return false, fmt.Errorf("process.args has a non-string entry")
		}
		args[i] = s
	}
	wrapped, changed := WrapArgs(args, o)
	if changed {
		out := make([]any, len(wrapped))
		for i, s := range wrapped {
			out[i] = s
		}
		m["args"] = out
	}
	return changed, nil
}

// Result reports what a rewrite did. Nothing is edited unless Wrapped.
type Result struct {
	Wrapped bool
	Removed []string // env names dropped from the process
}

// envDropped are removed from a wrapped container's env: V3COMPAT_NATIVE would let a pod opt itself
// out of translation on a translating node (ADR 0004 amendment 2026-11-22), V3COMPAT_AUDIT runs
// off-list forms natively.
var envDropped = []string{"V3COMPAT_NATIVE", "V3COMPAT_AUDIT"}

// rewriteProcess wraps the args of a runtime-spec Process map and, if they were wrapped, edits its env.
func rewriteProcess(m map[string]any, o Options) (Result, error) {
	changed, err := rewriteArgs(m, o)
	if err != nil || !changed {
		return Result{}, err
	}
	removed, err := removeEnv(m, envDropped...)
	if err != nil {
		return Result{}, err
	}
	if o.DROpts != "" {
		if err := setEnv(m, "V3COMPAT_DR_OPTS", o.DROpts); err != nil {
			return Result{}, err
		}
	}
	return Result{Wrapped: true, Removed: removed}, nil
}

func annotations(d doc) map[string]any {
	a, _ := d["annotations"].(map[string]any)
	return a
}

// IsSandbox reports whether config.json describes a Kubernetes pod sandbox (pause) container.
func IsSandbox(path string) (bool, error) {
	d, _, err := load(path)
	if err != nil {
		return false, err
	}
	a := annotations(d)
	for _, k := range []string{"io.kubernetes.cri.container-type", "io.kubernetes.cri-o.ContainerType"} {
		if a[k] == "sandbox" {
			return true, nil
		}
	}
	return false, nil
}

// IsWrapped reports whether config.json carries the marker set by RewriteConfig.
func IsWrapped(path string) (bool, error) {
	d, _, err := load(path)
	if err != nil {
		return false, err
	}
	return annotations(d)[AnnotationWrapped] == "true", nil
}

// RewriteConfig wraps the container of a bundle's config.json. It is idempotent.
func RewriteConfig(path string, o Options) (Result, error) {
	d, mode, err := load(path)
	if err != nil {
		return Result{}, err
	}
	p, ok := d["process"].(map[string]any)
	if !ok {
		return Result{}, fmt.Errorf("%s: no process", path)
	}
	res, err := rewriteProcess(p, o)
	if err != nil || !res.Wrapped {
		return Result{}, err
	}
	addMount(d, BundleMount, o.BundlePath)
	if o.CacheDir != "" {
		addMount(d, CacheMount, o.CacheDir)
	}
	a := annotations(d)
	if a == nil {
		a = map[string]any{}
		d["annotations"] = a
	}
	a[AnnotationWrapped] = "true"
	return res, save(path, d, mode)
}

// addMount adds a read-only bind of src at dest unless the spec already mounts something there.
// noexec is left off: DynamoRIO may map files from the cache.
func addMount(d doc, dest, src string) {
	mounts, _ := d["mounts"].([]any)
	for _, e := range mounts {
		if m, _ := e.(map[string]any); m["destination"] == dest {
			return
		}
	}
	b, _ := json.Marshal(specs.Mount{
		Destination: dest, Type: "bind", Source: src,
		Options: []string{"rbind", "ro", "nosuid", "nodev"},
	})
	var m any
	_ = json.Unmarshal(b, &m)
	d["mounts"] = append(mounts, m)
}

// ImageName returns the io.kubernetes.cri.image-name annotation of config.json, or "".
func ImageName(path string) (string, error) {
	d, _, err := load(path)
	if err != nil {
		return "", err
	}
	s, _ := annotations(d)[AnnotationImage].(string)
	return s, nil
}

// RewriteProcess wraps the args of an exec process file (a bare runtime-spec Process) and edits its env.
func RewriteProcess(path string, o Options) (Result, error) {
	d, mode, err := load(path)
	if err != nil {
		return Result{}, err
	}
	res, err := rewriteProcess(d, o)
	if err != nil || !res.Wrapped {
		return Result{}, err
	}
	return res, save(path, d, mode)
}
