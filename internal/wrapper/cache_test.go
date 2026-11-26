package wrapper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trusted stands in for the root-ownership check, which a non-root test run cannot satisfy.
func (e *env) trusted(p string) error {
	if e.trust != nil {
		return e.trust(p)
	}
	if st, err := os.Lstat(p); err != nil || !st.IsDir() {
		return fmt.Errorf("%s: not a directory", p)
	}
	return nil
}

func digestImage(hex string) string { return "r.io/x:1@sha256:" + strings.Repeat(hex, 64) }

func (e *env) cachedConfig(process string) {
	cfg := `{"process":` + process + `,"annotations":{"io.kubernetes.cri.image-name":"` + digestImage("a") + `"}}`
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte(cfg), 0o644))
}

func TestCacheMountSetsEnvAndAnnotation(t *testing.T) {
	e := newEnv(t)
	e.cacheFixture(digestImage("a"))
	e.prepare("create", "-b", e.bundle, "c1")
	got := e.cfgBytes()
	if !strings.Contains(got, `"env":["V3COMPAT_CACHE=/v3compat-cache"]`) || !strings.Contains(got, `"v3compat.cache":"true"`) {
		t.Fatal(got)
	}
}

func TestCacheNotMountedNoEnvNoAnnotation(t *testing.T) {
	e := newEnv(t)
	e.cacheFixture("r.io/x:1")
	e.prepare("create", "-b", e.bundle, "c1")
	if got := e.cfgBytes(); strings.Contains(got, "V3COMPAT_CACHE") || strings.Contains(got, "v3compat.cache") {
		t.Fatal(got)
	}
}

func TestCacheContainerEnvWins(t *testing.T) {
	e := newEnv(t)
	e.cacheFixture(digestImage("a"))
	e.cachedConfig(`{"args":["/app"],"env":["V3COMPAT_CACHE=/mine"]}`)
	e.prepare("create", "-b", e.bundle, "c1")
	got := e.cfgBytes()
	if !strings.Contains(got, `"env":["V3COMPAT_CACHE=/mine"]`) || !strings.Contains(got, `"v3compat.cache":"true"`) {
		t.Fatal(got)
	}
}

// A spec that already mounts /v3compat-cache holds a cache that is not ours: no env, no annotation.
func TestCacheContainerMountGetsNoEnv(t *testing.T) {
	e := newEnv(t)
	e.cacheFixture(digestImage("a"))
	cfg := `{"process":{"args":["/app"]},"mounts":[{"destination":"/v3compat-cache","type":"bind","source":"/mine"}],` +
		`"annotations":{"io.kubernetes.cri.image-name":"` + digestImage("a") + `"}}`
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte(cfg), 0o644))
	e.prepare("create", "-b", e.bundle, "c1")
	got := e.cfgBytes()
	if strings.Contains(got, "V3COMPAT_CACHE") || strings.Contains(got, "v3compat.cache") || strings.Contains(got, "sha256-"+strings.Repeat("a", 64)) {
		t.Fatal(got)
	}
}

func TestCacheUntrustedNotMountedAndWarned(t *testing.T) {
	hex := strings.Repeat("a", 64)
	for name, pick := range map[string]func(root, dir string) string{
		"digest dir": func(root, dir string) string { return dir },
		"cache_root": func(root, dir string) string { return root },
	} {
		e := newEnv(t)
		root := e.cacheFixture(digestImage("a"))
		bad := pick(root, filepath.Join(root, "sha256-"+hex))
		e.trust = func(p string) error {
			if p == bad {
				return fmt.Errorf("%s: not owned by uid 0", p)
			}
			return nil
		}
		e.prepare("create", "-b", e.bundle, "c1")
		got := e.cfgBytes()
		if !e.wrapped() || strings.Contains(got, "v3compat-cache") || strings.Contains(got, "V3COMPAT_CACHE") || strings.Contains(got, "v3compat.cache") {
			t.Errorf("%s: %s", name, got)
		}
		if !strings.Contains(strings.Join(e.logs, "\n"), "warn: c1: translation cache not mounted: "+bad+": not owned by uid 0") {
			t.Errorf("%s: %v", name, e.logs)
		}
	}
}

func TestCacheMissingDirIsNotAWarning(t *testing.T) {
	e := newEnv(t)
	e.cacheFixture(digestImage("c"))
	e.prepare("create", "-b", e.bundle, "c1")
	for _, l := range e.logs {
		if strings.HasPrefix(l, "warn") {
			t.Fatal(e.logs)
		}
	}
}

func (e *env) execWith(process string) string {
	proc := filepath.Join(e.dir, "process.json")
	must(os.WriteFile(proc, []byte(process), 0o644))
	e.state = func(string, []string, string) (string, error) { return e.bundle, nil }
	e.prepare("exec", "--process", proc, "c1")
	return fmt.Sprint(e.procEnvOf(proc, false))
}

func TestExecGetsCacheEnv(t *testing.T) {
	e := newEnv(t)
	e.cacheFixture(digestImage("a"))
	e.prepare("create", "-b", e.bundle, "c1")
	if got := e.execWith(`{"args":["sh"],"env":["PATH=/"]}`); got != "[PATH=/ V3COMPAT_CACHE=/v3compat-cache]" {
		t.Fatal(got)
	}
	if got := e.execWith(`{"args":["sh"],"env":["V3COMPAT_CACHE=/mine"]}`); got != "[V3COMPAT_CACHE=/mine]" {
		t.Fatal(got)
	}
}

func TestExecWithoutCacheGetsNoEnv(t *testing.T) {
	e := newEnv(t)
	e.prepare("create", "-b", e.bundle, "c1")
	if got := e.execWith(`{"args":["sh"]}`); got != "[]" {
		t.Fatal(got)
	}
}
