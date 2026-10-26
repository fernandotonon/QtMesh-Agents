package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fernandotonon/QtMesh-Agents/internal/tui"
	"github.com/spf13/cobra"
)

func runTUI(cmd *cobra.Command) error {
	client, _, err := connectClient()
	if err != nil {
		return fmt.Errorf("connect daemon: %w", err)
	}
	defer client.Close()

	m := tui.New(client)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
