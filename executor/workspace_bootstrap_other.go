//go:build !unix

package executor

import "os/exec"

func configureBootstrapProcess(cmd *exec.Cmd) {
	panic("executor: container workspace bootstrap requires Unix process groups")
}
