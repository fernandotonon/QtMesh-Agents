//go:build unix

package sessions

import "syscall"

func checkPID(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
