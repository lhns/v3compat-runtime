package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var opts = Options{BundlePath: "/opt/v3compat", InitPaths: []string{"/sbin/docker-init", "/run/podman-init", "/dev/init"}}

func TestWrapArgs(t *testing.T) {
	cases := []struct {
		name    string
		in, out []string
		changed bool
	}{
		{"plain", []string{"/app", "-x"}, []string{"/v3compat/bin/v3compat-run", "--", "/app", "-x"}, true},
		{"empty", nil, nil, false},
		{"already", []string{"/v3compat/bin/v3compat-run", "--", "/app"}, []string{"/v3compat/bin/v3compat-run", "--", "/app"}, false},
		{"docker-init", []string{"/sbin/docker-init", "--", "/app"}, []string{"/sbin/docker-init", "--", "/v3compat/bin/v3compat-run", "--", "/app"}, true},
		{"podman-init flags", []string{"/run/podman-init", "-g", "--", "/app", "--", "x"}, []string{"/run/podman-init", "-g", "--", "/v3compat/bin/v3compat-run", "--", "/app", "--", "x"}, true},
		{"dev-init already", []string{"/dev/init", "--", "/v3compat/bin/v3compat-run", "--", "/app"}, []string{"/dev/init", "--", "/v3compat/bin/v3compat-run", "--", "/app"}, false},
		{"init without dashes wraps all", []string{"/dev/init", "/app"}, []string{"/v3compat/bin/v3compat-run", "--", "/dev/init", "/app"}, true},
		{"init only", []string{"/dev/init", "--"}, []string{"/dev/init", "--"}, false},
	}
	for _, c := range cases {
		got, ch := WrapArgs(c.in, opts)
		if ch != c.changed || !reflect.DeepEqual(got, c.out) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ch, c.out, c.changed)
		}
	}
}

const cfg = `{"ociVersion":"1.0.2","futureField":{"a":[1,2.50,12345678901234567890]},"process":{"args":["/app","<&>"],"x-new":true},
"mounts":[{"destination":"/proc","type":"proc","source":"proc"}],"annotations":{"k":"v"},"linux":{"unknownThing":1}}`

func write(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(s), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) map[string]any {
	b, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRewriteConfigPreservesAndIsIdempotent(t *testing.T) {
	p := write(t, cfg)
	ch, err := RewriteConfig(p, opts)
	if err != nil || !ch {
		t.Fatal(ch, err)
	}
	b1, _ := os.ReadFile(p)
	m := read(t, p)
	if !reflect.DeepEqual(m["futureField"], read(t, write(t, cfg))["futureField"]) {
		t.Fatal("unknown field changed")
	}
	for _, s := range []string{"12345678901234567890", "2.50", "<&>"} {
		if !strings.Contains(string(b1), s) {
			t.Fatalf("%s mangled: %s", s, b1)
		}
	}
	pr := m["process"].(map[string]any)
	if pr["x-new"] != true || m["linux"].(map[string]any)["unknownThing"].(float64) != 1 {
		t.Fatal("unknown nested field lost")
	}
	args := pr["args"].([]any)
	if args[0] != "/v3compat/bin/v3compat-run" || len(args) != 4 {
		t.Fatalf("%v", args)
	}
	mounts := m["mounts"].([]any)
	if len(mounts) != 2 || mounts[1].(map[string]any)["destination"] != "/v3compat" || mounts[1].(map[string]any)["source"] != "/opt/v3compat" {
		t.Fatalf("%v", mounts)
	}
	if w, _ := IsWrapped(p); !w {
		t.Fatal("not marked")
	}
	if m["annotations"].(map[string]any)["k"] != "v" {
		t.Fatal("annotation lost")
	}
	if st, _ := os.Stat(p); filepath.Separator == '/' && st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
	ch, err = RewriteConfig(p, opts)
	b2, _ := os.ReadFile(p)
	if err != nil || ch || string(b1) != string(b2) {
		t.Fatal("not idempotent")
	}
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 1 {
		t.Fatal("temp file left behind")
	}
}

func TestRewriteConfigErrorsLeaveFileAlone(t *testing.T) {
	for _, s := range []string{`{not json`, `[]`, `{"process":{"args":[1]}}`, `{}`} {
		p := write(t, s)
		if ch, err := RewriteConfig(p, opts); ch || err == nil {
			t.Errorf("%s: %v %v", s, ch, err)
		}
		if b, _ := os.ReadFile(p); string(b) != s {
			t.Errorf("%s modified", s)
		}
	}
	if ch, err := RewriteConfig(filepath.Join(t.TempDir(), "none"), opts); ch || err == nil {
		t.Fatal("missing file")
	}
}

func TestExistingBundleMountNotDuplicated(t *testing.T) {
	p := write(t, `{"process":{"args":["/a"]},"mounts":[{"destination":"/v3compat","type":"bind","source":"/x"}]}`)
	if _, err := RewriteConfig(p, opts); err != nil {
		t.Fatal(err)
	}
	if n := len(read(t, p)["mounts"].([]any)); n != 1 {
		t.Fatal(n)
	}
}

func TestSandbox(t *testing.T) {
	p := write(t, `{"process":{"args":["/pause"]},"annotations":{"io.kubernetes.cri.container-type":"sandbox"}}`)
	if s, err := IsSandbox(p); !s || err != nil {
		t.Fatal(s, err)
	}
	p = write(t, `{"process":{"args":["/a"]},"annotations":{"io.kubernetes.cri.container-type":"container"}}`)
	if s, _ := IsSandbox(p); s {
		t.Fatal("container")
	}
}

func TestRewriteProcess(t *testing.T) {
	p := write(t, `{"args":["sh","-c","id"],"terminal":true,"future":1}`)
	ch, err := RewriteProcess(p, opts)
	if err != nil || !ch {
		t.Fatal(ch, err)
	}
	m := read(t, p)
	if m["future"].(float64) != 1 || m["terminal"] != true || m["args"].([]any)[0] != "/v3compat/bin/v3compat-run" {
		t.Fatalf("%v", m)
	}
	if ch, _ := RewriteProcess(p, opts); ch {
		t.Fatal("not idempotent")
	}
}

func TestMountOptionsAndCache(t *testing.T) {
	p := write(t, cfg)
	o := opts
	o.CacheDir = "/cache/sha256-ab"
	if _, err := RewriteConfig(p, o); err != nil {
		t.Fatal(err)
	}
	mounts := read(t, p)["mounts"].([]any)
	if len(mounts) != 3 {
		t.Fatalf("%v", mounts)
	}
	for i, dest := range []string{"/v3compat", "/v3compat-cache"} {
		m := mounts[i+1].(map[string]any)
		opt := strings.Join(toStrings(m["options"].([]any)), ",")
		if m["destination"] != dest || m["type"] != "bind" || opt != "rbind,ro,nosuid,nodev" {
			t.Errorf("%v", m)
		}
	}
	if mounts[2].(map[string]any)["source"] != "/cache/sha256-ab" {
		t.Fatalf("%v", mounts[2])
	}
	// No cache dir: only the bundle mount.
	p = write(t, cfg)
	if _, err := RewriteConfig(p, opts); err != nil {
		t.Fatal(err)
	}
	if n := len(read(t, p)["mounts"].([]any)); n != 2 {
		t.Fatal(n)
	}
}

func toStrings(a []any) []string {
	var s []string
	for _, e := range a {
		s = append(s, e.(string))
	}
	return s
}

func TestImageDigest(t *testing.T) {
	h := strings.Repeat("ab12", 16)
	ok := []string{"duckdb/duckdb:2.0.0@sha256:" + h, "r.io/a/b@sha256:" + h, "@sha256:" + h}
	for _, s := range ok {
		if got, good := ImageDigest(s); !good || got != h {
			t.Errorf("%q: %q %v", s, got, good)
		}
	}
	bad := []string{
		"", "duckdb/duckdb:2.0.0", "a@sha256:" + h[:63], "a@sha256:" + h + "0", "a@sha256:" + strings.ToUpper(h),
		"a@sha256:" + h[:63] + "g", "a@sha256:../" + h[:61], "a@sha256:" + h + "/", "a@sha256:" + h + "\n",
		"a@sha512:" + h, "a:sha256:" + h, "a@sha256:" + h + "@sha256:" + h[:10],
	}
	for _, s := range bad {
		if _, good := ImageDigest(s); good {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestImageName(t *testing.T) {
	p := write(t, `{"annotations":{"io.kubernetes.cri.image-name":"a/b@sha256:x"}}`)
	if n, err := ImageName(p); n != "a/b@sha256:x" || err != nil {
		t.Fatal(n, err)
	}
	if n, _ := ImageName(write(t, `{}`)); n != "" {
		t.Fatal(n)
	}
}
