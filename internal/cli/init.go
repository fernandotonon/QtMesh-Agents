package cli

import (
	"fmt"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write default configuration and detect agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirs(); err != nil {
				return err
			}
			path, err := config.Path()
			if err != nil {
				return err
			}
			cfg := config.Default()
			if err := config.Save(path, cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)

			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			agents, err := client.ListAgents()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "detected agents:")
			for _, a := range agents {
				mark := "miss"
				if a.Detected {
					mark = "ok"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s (%s)\n", mark, a.ID, a.Command)
			}
			return nil
		},
	}
}
