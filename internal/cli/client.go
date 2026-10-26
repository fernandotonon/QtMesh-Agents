package cli

import (
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/daemon"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

func connectClient() (*ipc.Client, string, error) {
	if err := config.EnsureDirs(); err != nil {
		return nil, "", err
	}
	socket, err := ipc.DefaultSocketPath()
	if err != nil {
		return nil, "", err
	}
	if err := daemon.EnsureRunning(socket); err != nil {
		return nil, "", err
	}
	client, err := ipc.Connect(socket)
	if err != nil {
		return nil, "", err
	}
	return client, socket, nil
}
