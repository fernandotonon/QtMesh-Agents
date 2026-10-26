//go:build windows

package sessions

import "os"

func checkPID(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows FindProcess does not prove liveness; attempt a no-op signal path.
	_ = p
	return true
}
