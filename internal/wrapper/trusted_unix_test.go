//go:build unix

package wrapper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDir(t *testing.T) {
	root := t.TempDir()
	me := uint32(os.Getuid())
	mk := func(name string, mode os.FileMode) string {
		p := filepath.Join(root, name)
		must(os.Mkdir(p, 0o700))
		must(os.Chmod(p, mode))
		return p
	}
	file := filepath.Join(root, "file")
	must(os.WriteFile(file, nil, 0o600))
	link := filepath.Join(root, "link")
	must(os.Symlink(mk("target", 0o755), link))
	cases := []struct {
		name string
		path string
		uid  uint32
		want string // "" accepted, else a substring of the error
	}{
		{"ok 755", mk("ok", 0o755), me, ""},
		{"ok 700", mk("ok700", 0o700), me, ""},
		{"other owner", mk("owner", 0o755), me + 1, "not owned"},
		{"group writable", mk("gw", 0o775), me, "writable by group or others"},
		{"other writable", mk("ow", 0o757), me, "writable by group or others"},
		{"symlink", link, me, "symlink"},
		{"file", file, me, "not a directory"},
		{"missing", filepath.Join(root, "none"), me, "no such file"},
	}
	for _, c := range cases {
		err := checkDir(c.path, c.uid)
		switch {
		case c.want == "" && err != nil, c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
