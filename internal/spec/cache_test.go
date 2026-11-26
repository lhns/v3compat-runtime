package spec

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func mustRead(p string) string { b, _ := os.ReadFile(p); return string(b) }

func procEnvOf(t *testing.T, p string) any { return read(t, p)["process"].(map[string]any)["env"] }

func TestCacheMountEnvAndAnnotation(t *testing.T) {
	o := opts
	o.CacheDir = "/cache/sha256-ab"
	p := write(t, `{"process":{"args":["/app"],"env":["A=1"]}}`)
	if _, err := RewriteConfig(p, o); err != nil {
		t.Fatal(err)
	}
	if got := procEnvOf(t, p); !reflect.DeepEqual(got, []any{"A=1", "V3COMPAT_CACHE=/v3compat-cache"}) {
		t.Fatalf("%v", got)
	}
	if c, _ := HasCache(p); !c {
		t.Fatal("not annotated")
	}
}

func TestCacheContainerEnvWins(t *testing.T) {
	o := opts
	o.CacheDir = "/cache/sha256-ab"
	p := write(t, `{"process":{"args":["/app"],"env":["V3COMPAT_CACHE=/mine"]}}`)
	if _, err := RewriteConfig(p, o); err != nil {
		t.Fatal(err)
	}
	if got := procEnvOf(t, p); !reflect.DeepEqual(got, []any{"V3COMPAT_CACHE=/mine"}) {
		t.Fatalf("%v", got)
	}
}

// An existing mount at /v3compat-cache is not ours: no env, no annotation.
func TestCacheExistingMountGetsNoEnv(t *testing.T) {
	o := opts
	o.CacheDir = "/cache/sha256-ab"
	p := write(t, `{"process":{"args":["/app"]},"mounts":[{"destination":"/v3compat-cache","type":"bind","source":"/x"}]}`)
	if _, err := RewriteConfig(p, o); err != nil {
		t.Fatal(err)
	}
	if _, ok := read(t, p)["process"].(map[string]any)["env"]; ok {
		t.Fatal("env set")
	}
	if c, _ := HasCache(p); c {
		t.Fatal("annotated")
	}
}

func TestNoCacheDirNoEnvNoAnnotation(t *testing.T) {
	p := write(t, `{"process":{"args":["/app"]}}`)
	if _, err := RewriteConfig(p, opts); err != nil {
		t.Fatal(err)
	}
	if c, _ := HasCache(p); c || strings.Contains(mustRead(p), "V3COMPAT_CACHE") {
		t.Fatal("cache marked without a mount")
	}
}

func TestCacheEnvNonStringFailsClosed(t *testing.T) {
	o := opts
	o.CacheDir = "/cache/sha256-ab"
	s := `{"process":{"args":["/app"],"env":[1]}}`
	p := write(t, s)
	if ch, err := RewriteConfig(p, o); ch.Wrapped || err == nil || mustRead(p) != s {
		t.Fatal(ch, err)
	}
}

func TestRewriteProcessExecCache(t *testing.T) {
	o := opts
	o.ExecCache = true
	p := write(t, `{"args":["sh"],"env":["PATH=/"]}`)
	if ch, err := RewriteProcess(p, o); !ch.Wrapped || err != nil {
		t.Fatal(ch, err)
	}
	if got := read(t, p)["env"]; !reflect.DeepEqual(got, []any{"PATH=/", "V3COMPAT_CACHE=/v3compat-cache"}) {
		t.Fatalf("%v", got)
	}
	// Already wrapped: args stay, the cache env is still set.
	p = write(t, `{"args":["/v3compat/bin/v3compat-run","--","sh"]}`)
	if ch, err := RewriteProcess(p, o); ch.Wrapped || err != nil {
		t.Fatal(ch, err)
	}
	if got := read(t, p)["env"]; !reflect.DeepEqual(got, []any{"V3COMPAT_CACHE=/v3compat-cache"}) {
		t.Fatalf("%v", got)
	}
	// Without ExecCache no env is set.
	p = write(t, `{"args":["sh"]}`)
	if _, err := RewriteProcess(p, opts); err != nil || strings.Contains(mustRead(p), "V3COMPAT_CACHE") {
		t.Fatal("env set without ExecCache")
	}
}
