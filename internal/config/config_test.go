package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(filepath.Join(dir, "missing.toml"))
	if err != nil || c.RealRuntime != "/var/lib/k0s/bin/runc" || c.BundlePath != "/opt/v3compat" || c.Native || c.CacheRoot != "" {
		t.Fatal(c, err)
	}
	p := filepath.Join(dir, "c.toml")
	os.WriteFile(p, []byte("native = true\ncache_root = \"/c\"\n"), 0o644)
	c, err = Load(p)
	if err != nil || !c.Native || c.CacheRoot != "/c" || c.BundlePath != "/opt/v3compat" {
		t.Fatal(c, err)
	}
	os.WriteFile(p, []byte("native = "), 0o644)
	if _, err = Load(p); err == nil {
		t.Fatal("want error")
	}
}
