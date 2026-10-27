//go:build windows

package daemon

import "os"

func acquireInstanceLock(socketPath string) (*os.File, error) {
	// Best-effort: rely on Listen refusal when already alive.
	return nil, nil
}
