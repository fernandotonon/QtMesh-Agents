package ipc

import (
	"fmt"
	"net"
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
	if runtime.GOOS == "windows" {
		// Named pipe path abstraction — clients still dial via Listen/Dial helpers.
		return `\\.\pipe\qtmesh-agents`, nil
	}
	return filepath.Join(runtimeDir, "daemon.sock"), nil
}

// Listen creates a local listener for the daemon.
func Listen(socketPath string) (net.Listener, error) {
	if runtime.GOOS == "windows" {
		return listenWindows(socketPath)
	}
	_ = os.Remove(socketPath)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(socketPath, 0o600)
	return ln, nil
}

// Dial connects to the daemon socket with a timeout.
func Dial(socketPath string, timeout time.Duration) (net.Conn, error) {
	if runtime.GOOS == "windows" {
		return dialWindows(socketPath, timeout)
	}
	d := net.Dialer{Timeout: timeout}
	return d.Dial("unix", socketPath)
}

// IsDaemonAlive probes the socket with a short dial.
func IsDaemonAlive(socketPath string) bool {
	conn, err := Dial(socketPath, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func listenWindows(socketPath string) (net.Listener, error) {
	// Fallback: use a TCP localhost loopback file marker on unsupported builds.
	// Real Windows named-pipe support can replace this without changing call sites.
	if socketPath == "" {
		return nil, fmt.Errorf("empty socket path")
	}
	addr := "127.0.0.1:0"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	meta := socketPath + ".addr"
	if err := os.WriteFile(meta, []byte(ln.Addr().String()), 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

func dialWindows(socketPath string, timeout time.Duration) (net.Conn, error) {
	meta := socketPath + ".addr"
	data, err := os.ReadFile(meta)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: timeout}
	return d.Dial("tcp", string(data))
}
