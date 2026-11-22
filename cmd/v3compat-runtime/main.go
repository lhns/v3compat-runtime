//go:build unix

// v3compat-runtime is an OCI runtime wrapper: it rewrites the spec of containers that should run
// under the v3compat launcher, then replaces itself with the real runtime (no fork). stdout and stderr belong
// to the runtime protocol, so everything is logged to a file and syslog.
package main

import (
	"fmt"
	"log/syslog"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/cpu"

	"github.com/lhns/v3compat-runtime/internal/config"
	"github.com/lhns/v3compat-runtime/internal/wrapper"
)

func main() {
	real, err := wrapper.Prepare(os.Args[1:], wrapper.Deps{
		ConfigPath: config.DefaultPath,
		HostV3:     hostV3,
		Logf:       logf,
	})
	if err != nil {
		wrapper.Refuse(os.Args[1:], err)
		fmt.Fprintf(os.Stderr, "v3compat-runtime: %v\n", err)
		os.Exit(1)
	}
	if err := syscall.Exec(real, append([]string{real}, os.Args[1:]...), os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "v3compat-runtime: exec %s: %v\n", real, err)
		os.Exit(127)
	}
}

// hostV3 reports the x86-64-v3 features that matter here: lzcnt (ABM) and tzcnt (BMI1) decode as
// bsr/bsf on older CPUs, so a native run there silently computes wrong results.
func hostV3() bool {
	x := cpu.X86
	return x.HasAVX && x.HasAVX2 && x.HasBMI1 && x.HasBMI2 && x.HasFMA && x.HasLZCNT && x.HasMOVBE
}

func logf(file, format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "v3compat-runtime"); err == nil {
		_, _ = w.Write([]byte(line))
		w.Close()
	}
	if file == "" {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s pid=%d %s\n", time.Now().Format(time.RFC3339), os.Getpid(), line)
	f.Close()
}
