//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

func execBackground(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	// New session so the daemon survives the parent CLI exiting (no SIGHUP).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
