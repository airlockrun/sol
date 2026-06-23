//go:build !unix

package tools

import "os/exec"

// configureProcAttr is a no-op on non-unix platforms: process groups and
// signal-based group kills aren't available the same way. exec.CommandContext
// still kills the direct child and cmd.WaitDelay still bounds the I/O wait, so
// the timeout remains enforceable for the common case.
func configureProcAttr(cmd *exec.Cmd) {}
