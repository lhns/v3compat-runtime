package wrapper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lhns/v3compat-runtime/internal/spec"
)

type env struct {
	t      *testing.T
	dir    string
	v3c    string
	bundle string
	conf   string
	logs   []string
	v3     bool
	state  func(string, []string, string) (string, error)
	err    error // of the last prepare
}

const appCfg = `{"process":{"args":["/app"]},"annotations":{"io.kubernetes.cri.container-type":"container"}}`

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	e := &env{t: t, dir: dir, v3c: filepath.Join(dir, "v3compat"), bundle: filepath.Join(dir, "b"), conf: filepath.Join(dir, "config.toml")}
	must(os.MkdirAll(filepath.Join(e.v3c, "bin"), 0o755))
	must(os.MkdirAll(e.bundle, 0o755))
	must(os.WriteFile(filepath.Join(e.v3c, "bin", "v3compat-run"), nil, 0o755))
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte(appCfg), 0o644))
	e.config(fmt.Sprintf("real_runtime = %q\nbundle_path = %q\nlog_file = \"\"\n", "/real/runc", filepath.ToSlash(e.v3c)))
	return e
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func (e *env) config(s string) { must(os.WriteFile(e.conf, []byte(s), 0o644)) }

func (e *env) prepare(args ...string) string {
	real, err := Prepare(args, Deps{
		ConfigPath: e.conf,
		HostV3:     func() bool { return e.v3 },
		Logf:       func(_, f string, a ...any) { e.logs = append(e.logs, fmt.Sprintf(f, a...)) },
		State:      e.state,
		Getwd:      func() (string, error) { return e.bundle, nil },
	})
	e.err = err
	if err != nil && real != "" {
		e.t.Fatalf("refused but returned a runtime %q", real)
	}
	return real
}

func (e *env) cfgBytes() string {
	b, _ := os.ReadFile(filepath.Join(e.bundle, "config.json"))
	return string(b)
}

func (e *env) wrapped() bool { return strings.Contains(e.cfgBytes(), spec.Launcher) }

func (e *env) lastLog() string {
	if len(e.logs) == 0 {
		return ""
	}
	return e.logs[len(e.logs)-1]
}

func TestCreateWraps(t *testing.T) {
	e := newEnv(t)
	if got := e.prepare("--root", "/r", "create", "--bundle", e.bundle, "c1"); got != "/real/runc" {
		t.Fatal(got)
	}
	if !e.wrapped() || !strings.Contains(e.lastLog(), "wrapped") {
		t.Fatalf("%s %v", e.cfgBytes(), e.logs)
	}
}

func TestRunUsesCwdWithoutBundleFlag(t *testing.T) {
	e := newEnv(t)
	e.prepare("run", "c1")
	if !e.wrapped() {
		t.Fatal("not wrapped")
	}
}

func (e *env) nativeConf(extra string) {
	e.config(fmt.Sprintf("real_runtime = \"/real/runc\"\nbundle_path = %q\nlog_file = \"\"\n%s", filepath.ToSlash(e.v3c), extra))
}

// The native decision: translate unless native = true and CPUID agrees.
func TestNativeDecisionMatrix(t *testing.T) {
	cases := []struct {
		native, v3 bool
		wrapped    bool
		log        string
	}{
		{false, false, true, "wrapped"},
		{false, true, true, "wrapped"}, // a v3 CPU alone never skips translation
		{true, true, false, "natively"},
		{true, false, true, "CPUID"}, // veto: translate and warn
	}
	for _, c := range cases {
		e := newEnv(t)
		e.v3 = c.v3
		e.nativeConf(fmt.Sprintf("native = %v\n", c.native))
		e.prepare("create", "-b", e.bundle, "c1")
		if e.wrapped() != c.wrapped || !strings.Contains(strings.Join(e.logs, "\n"), c.log) {
			t.Errorf("native=%v v3=%v: wrapped=%v logs=%v", c.native, c.v3, e.wrapped(), e.logs)
		}
		if c.native && !c.v3 && !strings.HasPrefix(e.logs[0], "warn") {
			t.Errorf("veto not warned: %v", e.logs)
		}
	}
}

func (e *env) cacheFixture(image string) (root string) {
	root = filepath.Join(e.dir, "cache")
	must(os.MkdirAll(filepath.Join(root, "sha256-"+strings.Repeat("a", 64)), 0o755))
	must(os.WriteFile(filepath.Join(root, "sha256-"+strings.Repeat("b", 64)), nil, 0o644)) // a file, not a directory
	e.nativeConf(fmt.Sprintf("cache_root = %q\n", filepath.ToSlash(root)))
	cfg := `{"process":{"args":["/app"]},"annotations":{"io.kubernetes.cri.container-type":"container","io.kubernetes.cri.image-name":"` + image + `"}}`
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte(cfg), 0o644))
	return root
}

func TestCacheMounted(t *testing.T) {
	e := newEnv(t)
	root := e.cacheFixture("r.io/x:1@sha256:" + strings.Repeat("a", 64))
	e.prepare("create", "-b", e.bundle, "c1")
	got := e.cfgBytes()
	src := func(p string) string { j, _ := json.Marshal(p); return `"source":` + string(j) }
	if !strings.Contains(got, `"destination":"/v3compat-cache"`) || !strings.Contains(got, src(filepath.Join(root, "sha256-"+strings.Repeat("a", 64)))) {
		t.Fatal(got)
	}
	if strings.Contains(got, src(root)) || strings.Contains(got, "noexec") {
		t.Fatalf("cache_root itself mounted, or noexec: %s", got)
	}
	if !strings.Contains(e.lastLog(), "r.io/x:1@sha256:") {
		t.Fatal(e.logs)
	}
}

func TestCacheNotMounted(t *testing.T) {
	for name, image := range map[string]string{
		"no digest":    "r.io/x:1",
		"no directory": "r.io/x@sha256:" + strings.Repeat("c", 64),
		"a file":       "r.io/x@sha256:" + strings.Repeat("b", 64),
		"traversal":    "r.io/x@sha256:../../" + strings.Repeat("a", 57),
		"uppercase":    "r.io/x@sha256:" + strings.Repeat("A", 64),
		"empty":        "",
	} {
		e := newEnv(t)
		e.cacheFixture(image)
		e.prepare("create", "-b", e.bundle, "c1")
		if !e.wrapped() || strings.Contains(e.cfgBytes(), "v3compat-cache") {
			t.Errorf("%s: wrapped=%v %s", name, e.wrapped(), e.cfgBytes())
		}
		if !strings.Contains(e.lastLog(), "none mounted") {
			t.Errorf("%s: %v", name, e.logs)
		}
	}
}

func TestCacheOffByDefault(t *testing.T) {
	e := newEnv(t)
	cfg := `{"process":{"args":["/app"]},"annotations":{"io.kubernetes.cri.image-name":"r.io/x@sha256:` + strings.Repeat("a", 64) + `"}}`
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte(cfg), 0o644))
	e.prepare("create", "-b", e.bundle, "c1")
	if strings.Contains(e.cfgBytes(), "v3compat-cache") {
		t.Fatal("mounted without cache_root")
	}
}

func TestSandboxNotWrapped(t *testing.T) {
	e := newEnv(t)
	sb := `{"process":{"args":["/pause"]},"annotations":{"io.kubernetes.cri.container-type":"sandbox"}}`
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte(sb), 0o644))
	e.prepare("create", "-b", e.bundle, "c1")
	if e.cfgBytes() != sb {
		t.Fatal("sandbox modified")
	}
}

func TestOtherSubcommandsUntouchedAndSilent(t *testing.T) {
	e := newEnv(t)
	for _, c := range [][]string{{"state", "c1"}, {"start", "c1"}, {"kill", "c1", "TERM"}, {"delete", "c1"}, {"features"}, {"--version"}, {}} {
		if got := e.prepare(c...); got != "/real/runc" {
			t.Fatal(got)
		}
	}
	if e.cfgBytes() != appCfg || len(e.logs) != 0 {
		t.Fatalf("hot path touched things: %v", e.logs)
	}
}

// A config that does not parse leaves the native flag unknown: create, run and exec are refused,
// every other subcommand passes through.
func TestBrokenConfigRefusesSetupOnly(t *testing.T) {
	e := newEnv(t)
	e.config("this = = not toml")
	for _, c := range [][]string{{"create", "-b", e.bundle, "c1"}, {"run", "c1"}, {"exec", "--process", "/p", "c1"}} {
		if e.prepare(c...); e.err == nil || !strings.Contains(e.lastLog(), "refusing") {
			t.Fatalf("%v: %v %v", c, e.err, e.logs)
		}
	}
	if e.cfgBytes() != appCfg {
		t.Fatal("spec modified")
	}
	for _, c := range [][]string{{"state", "c1"}, {"kill", "c1", "KILL"}, {"delete", "-f", "c1"}} {
		if got := e.prepare(c...); got != "/var/lib/k0s/bin/runc" || e.err != nil || !strings.Contains(e.lastLog(), "passing through") {
			t.Fatalf("%v: %q %v %v", c, got, e.err, e.logs)
		}
	}
}

func TestMissingConfigUsesDefaults(t *testing.T) {
	e := newEnv(t)
	must(os.Remove(e.conf))
	if got := e.prepare("state", "c1"); got != "/var/lib/k0s/bin/runc" || len(e.logs) != 0 {
		t.Fatal(got, e.logs)
	}
}

func TestMissingBundleRefuses(t *testing.T) {
	e := newEnv(t)
	must(os.Remove(filepath.Join(e.v3c, "bin", "v3compat-run")))
	e.prepare("create", "-b", e.bundle, "c1")
	if e.err == nil || e.cfgBytes() != appCfg || !strings.Contains(e.lastLog(), "bundle missing") {
		t.Fatal(e.err, e.logs)
	}
}

func TestBrokenSpecRefuses(t *testing.T) {
	e := newEnv(t)
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte("{oops"), 0o644))
	e.prepare("create", "-b", e.bundle, "c1")
	if e.err == nil || e.cfgBytes() != "{oops" || !strings.Contains(e.lastLog(), "refused") {
		t.Fatal(e.err, e.logs)
	}
}

// A native node has nothing to translate: a missing bundle or broken spec does not stop it.
func TestNativeNodePassesThroughErrors(t *testing.T) {
	e := newEnv(t)
	e.v3 = true
	e.nativeConf("native = true\n")
	must(os.Remove(filepath.Join(e.v3c, "bin", "v3compat-run")))
	must(os.WriteFile(filepath.Join(e.bundle, "config.json"), []byte("{oops"), 0o644))
	if got := e.prepare("create", "-b", e.bundle, "c1"); got != "/real/runc" || e.err != nil {
		t.Fatal(got, e.err)
	}
}

func TestRefuseWritesRuntimeLog(t *testing.T) {
	dir := t.TempDir()
	js, txt := filepath.Join(dir, "log.json"), filepath.Join(dir, "log.txt")
	err := errors.New("c1: v3compat bundle missing")
	Refuse([]string{"--root", "/r", "--log", js, "--log-format", "json", "create", "c1"}, err)
	Refuse([]string{"--log=" + txt, "create", "c1"}, err)
	Refuse([]string{"create", "c1"}, err) // no --log: nothing to write
	var m map[string]string
	b, _ := os.ReadFile(js)
	if json.Unmarshal(b, &m) != nil || m["level"] != "error" || m["msg"] != "v3compat-runtime: c1: v3compat bundle missing" {
		t.Fatalf("%s", b)
	}
	if b, _ := os.ReadFile(txt); !strings.Contains(string(b), `level=error msg="v3compat-runtime: c1: v3compat bundle missing"`) {
		t.Fatalf("%s", b)
	}
}

func (e *env) execFixture(wrapped bool) string {
	if wrapped {
		e.prepare("create", "-b", e.bundle, "c1")
	}
	proc := filepath.Join(e.dir, "process.json")
	must(os.WriteFile(proc, []byte(`{"args":["sh","-c","id"],"future":1}`), 0o644))
	var gotGlobal []string
	e.state = func(real string, global []string, id string) (string, error) {
		gotGlobal = global
		if real != "/real/runc" || id != "c1" {
			return "", errors.New("bad args")
		}
		return e.bundle, nil
	}
	e.prepare("--root", "/r", "exec", "--process", proc, "--pid-file", "/p", "c1")
	if strings.Join(gotGlobal, " ") != "--root /r" {
		e.t.Fatalf("global flags not forwarded to state: %v", gotGlobal)
	}
	b, _ := os.ReadFile(proc)
	return string(b)
}

func TestExecMarkedContainerRewritten(t *testing.T) {
	e := newEnv(t)
	if got := e.execFixture(true); !strings.Contains(got, `"/v3compat/bin/v3compat-run","--","sh"`) || !strings.Contains(got, `"future":1`) {
		t.Fatal(got)
	}
}

func TestExecUnmarkedContainerUntouched(t *testing.T) {
	e := newEnv(t)
	if got := e.execFixture(false); got != `{"args":["sh","-c","id"],"future":1}` {
		t.Fatal(got)
	}
}

func TestExecRefusesWhenItCannotWrap(t *testing.T) {
	e := newEnv(t)
	e.prepare("create", "-b", e.bundle, "c1")
	proc := filepath.Join(e.dir, "process.json")
	const orig = `{"args":["sh"]}`
	must(os.WriteFile(proc, []byte(orig), 0o644))
	e.state = func(string, []string, string) (string, error) { return "", errors.New("no such container") }
	e.prepare("exec", "--process", proc, "c1")
	if b, _ := os.ReadFile(proc); string(b) != orig || e.err == nil || !strings.Contains(e.lastLog(), "refused") {
		t.Fatal(string(b), e.err, e.logs)
	}
	e.state = func(string, []string, string) (string, error) { return e.bundle, nil }
	e.prepare("exec", "c1", "sh") // inline args: no process file
	if e.err == nil || !strings.Contains(e.lastLog(), "inline") {
		t.Fatal(e.err, e.logs)
	}
}

func TestExecNative(t *testing.T) {
	e := newEnv(t)
	e.v3 = true
	e.nativeConf("native = true\n")
	e.state = func(string, []string, string) (string, error) { t.Fatal("state called"); return "", nil }
	e.prepare("exec", "--process", "/x", "c1")

	// native = true but CPUID vetoes: exec is translated like create.
	e = newEnv(t)
	e.nativeConf("native = true\n")
	if got := e.execFixture(true); !strings.Contains(got, "v3compat-run") {
		t.Fatal(got)
	}

	// A v3 CPU without native = true is still translated.
	e = newEnv(t)
	e.v3 = true
	if got := e.execFixture(true); !strings.Contains(got, "v3compat-run") {
		t.Fatal(got)
	}
}
