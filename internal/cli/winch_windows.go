//go:build windows

package cli

import "github.com/fernandotonon/QtMesh-Agents/internal/ipc"

func startWinchHandler(client *ipc.Client, sessionID string) {
	// Window resize signals are not available the same way on Windows.
	_ = client
	_ = sessionID
}
