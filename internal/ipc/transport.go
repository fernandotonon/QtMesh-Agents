package ipc

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// DefaultSocketPath returns the platform socket/pipe path.
func DefaultSocketPath() (string, error) {
	if v := os.Getenv("QTMESH_AGENTS_SOCKET"); v != "" {
		return v, nil
	}
	if runtime.GOOS == "windows" {
		// Native named pipe; clients use the same Listen/Dial helpers.
		return `\\.\pipe\qtmesh-agents`, nil
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		runtimeDir = filepath.Join(home, ".local", "share", "qtmesh-agents", "run")
	} else {
		runtimeDir = filepath.Join(runtimeDir, "qtmesh-agents")
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(runtimeDir, "daemon.sock"), nil
}

// IsDaemonAlive probes the socket with a dial.
// Uses a generous timeout — under agent load a 300ms probe falsely reported
// "dead" and EnsureRunning would spawn a second daemon that stole the socket.
func IsDaemonAlive(socketPath string) bool {
	conn, err := Dial(socketPath, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
