package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

// EnsureRunning starts the daemon in the background if it is not alive.
func EnsureRunning(socketPath string) error {
	if socketPath == "" {
		var err error
		socketPath, err = ipc.DefaultSocketPath()
		if err != nil {
			return err
		}
	}
	// Retry the alive probe — a single timeout under load used to spawn duplicates.
	for i := 0; i < 3; i++ {
		if ipc.IsDaemonAlive(socketPath) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "daemon", "start", "--foreground=false")
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// Detach.
	_ = cmd.Process.Release()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ipc.IsDaemonAlive(socketPath) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// If another process won the lock/listen race, treat as success when alive now.
	if ipc.IsDaemonAlive(socketPath) {
		return nil
	}
	return fmt.Errorf("daemon did not become ready at %s", socketPath)
}

// PidFilePath returns the daemon pid file location next to the socket.
func PidFilePath(socketPath string) string {
	return filepath.Join(filepath.Dir(socketPath), "daemon.pid")
}

// WritePid writes the current process pid.
func WritePid(socketPath string) error {
	return os.WriteFile(PidFilePath(socketPath), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}

// RemovePid removes the pid file.
func RemovePid(socketPath string) {
	_ = os.Remove(PidFilePath(socketPath))
}

// ForceCleanup stops leftover daemon processes and removes stale socket/pid files.
func ForceCleanup(socketPath string) {
	if b, err := os.ReadFile(PidFilePath(socketPath)); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = proc.Kill()
			}
		}
	}
	_ = exec.Command("pkill", "-f", "qma daemon start").Run()
	_ = os.Remove(socketPath)
	RemovePid(socketPath)
	// Give the OS a moment to release the socket path.
	time.Sleep(200 * time.Millisecond)
}
