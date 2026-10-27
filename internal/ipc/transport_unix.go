//go:build !windows

package ipc

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Listen creates a local listener for the daemon.
// Refuses to steal a live socket — previously os.Remove allowed a second
// daemon to orphan the first and leave runs looking permanently stale.
func Listen(socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, err
	}
	if IsDaemonAlive(socketPath) {
		return nil, fmt.Errorf("daemon already running at %s", socketPath)
	}
	// Stale socket file from a crashed daemon — safe to remove only when dial failed.
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(socketPath, 0o600)
	return ln, nil
}

// Dial connects to the daemon socket with a timeout.
func Dial(socketPath string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	return d.Dial("unix", socketPath)
}
