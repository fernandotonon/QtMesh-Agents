//go:build windows

package agents

import "os/exec"

func setHeadlessProcGroup(cmd *exec.Cmd) {}

func killHeadlessProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
