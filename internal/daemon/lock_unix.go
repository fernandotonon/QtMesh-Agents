//go:build unix

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireInstanceLock holds an exclusive flock so only one daemon can listen.
// The returned file must stay open for the life of the daemon.
func acquireInstanceLock(socketPath string) (*os.File, error) {
	lockPath := filepath.Join(filepath.Dir(socketPath), "daemon.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another qma daemon holds %s: %w", lockPath, err)
	}
	return f, nil
}
