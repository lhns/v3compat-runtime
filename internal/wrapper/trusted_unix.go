//go:build unix

package wrapper

import (
	"fmt"
	"os"
	"syscall"
)

// trustedDir accepts a real directory (no symlink) owned by root and not writable by group or others.
func trustedDir(path string) error { return checkDir(path, 0) }

func checkDir(path string, uid uint32) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s: is a symlink", path)
	case !st.IsDir():
		return fmt.Errorf("%s: not a directory", path)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != uid {
		return fmt.Errorf("%s: not owned by uid %d", path, uid)
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: writable by group or others (mode %04o)", path, st.Mode().Perm())
	}
	return nil
}
