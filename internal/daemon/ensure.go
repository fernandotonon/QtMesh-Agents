package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if ipc.IsDaemonAlive(socketPath) {
		return nil
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

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ipc.IsDaemonAlive(socketPath) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
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
