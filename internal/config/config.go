// Package config reads /etc/v3compat-runtime/config.toml. All keys are optional.
package config

import (
	"errors"
	"io/fs"

	"github.com/BurntSushi/toml"
)

const DefaultPath = "/etc/v3compat-runtime/config.toml"

type Config struct {
	RealRuntime string   `toml:"real_runtime"`
	BundlePath  string   `toml:"bundle_path"`
	InitPaths   []string `toml:"init_paths"`
	LogFile     string   `toml:"log_file"`
	// Native runs containers unwrapped on a CPU that reports x86-64-v3. The install writes it for
	// nodes labelled as v3 (ADR 0004); CPUID only vetoes it. Default false: translate.
	Native bool `toml:"native"`
	// CacheRoot holds one directory per image digest (sha256-<hex>); empty disables the cache mount.
	CacheRoot string `toml:"cache_root"`
	// DROpts is set as V3COMPAT_DR_OPTS in every wrapped container, over the container's own value.
	DROpts string `toml:"dr_opts"`
}

func Defaults() Config {
	return Config{
		RealRuntime: "/var/lib/k0s/bin/runc",
		BundlePath:  "/opt/v3compat",
		InitPaths:   []string{"/sbin/docker-init", "/run/podman-init", "/dev/init"},
		LogFile:     "/var/log/v3compat-runtime.log",
	}
}

// Load returns the defaults if the file is missing. If it is invalid, it returns the defaults
// and the error; callers should then not wrap anything.
func Load(path string) (Config, error) {
	c := Defaults()
	_, err := toml.DecodeFile(path, &c)
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Defaults(), err
	}
	return c, nil
}
