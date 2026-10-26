package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"golang.org/x/term"
)

func runAttach(sessionID string) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Attach(sessionID); err != nil {
		return err
	}
	defer func() { _ = client.Detach() }()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil {
		_ = client.Resize(sessionID, uint16(cols), uint16(rows))
	}
	startWinchHandler(client, sessionID)

	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := client.InputFire(sessionID, buf[:n]); werr != nil {
					errCh <- werr
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errCh <- err
				}
				return
			}
		}
	}()
	go func() {
		for env := range client.Outputs() {
			if env.Type == ipc.TypeOutput {
				out, err := ipc.DecodePayload[ipc.OutputEvent](env.Payload)
				if err != nil {
					continue
				}
				_, _ = os.Stdout.Write(out.Data)
			}
		}
		errCh <- fmt.Errorf("daemon connection closed")
	}()

	return <-errCh
}
