//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// configureProcAttr puts cmd in its own process group and installs a Cancel
// hook that SIGKILLs the entire group (negative PID) when the command's
// context fires. exec.CommandContext on its own kills only the direct child
// (the bash process), leaving descendants — the actual `go test` binary, a
// spawned server — running as orphans. Killing the group reaps the tree.
func configureProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
