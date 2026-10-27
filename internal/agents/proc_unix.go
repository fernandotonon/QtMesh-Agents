//go:build unix

package agents

import (
	"os/exec"
	"syscall"
)

func setHeadlessProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killHeadlessProcGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative PID = kill the process group started with Setpgid.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
