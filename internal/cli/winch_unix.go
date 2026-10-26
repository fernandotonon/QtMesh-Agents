//go:build unix

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"golang.org/x/term"
)

func startWinchHandler(client *ipc.Client, sessionID string) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			c, r, err := term.GetSize(int(os.Stdout.Fd()))
			if err == nil {
				_ = client.Resize(sessionID, uint16(c), uint16(r))
			}
		}
	}()
}
