package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Inspect coding agent adapters",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			list, err := client.ListAgents()
			if err != nil {
				return err
			}
			for _, a := range list {
				status := "missing"
				if a.Detected {
					status = "detected"
				}
				en := "disabled"
				if a.Enabled {
					en = "enabled"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-8s  %-10s  %-8s  %-10s  %s\n",
					a.ID, a.Vendor, en, status, a.Version)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Diagnose agent CLI availability",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			list, err := client.ListAgents()
			if err != nil {
				return err
			}
			ok := 0
			for _, a := range list {
				if a.ID == "mock" {
					continue
				}
				if a.Detected {
					fmt.Fprintf(cmd.OutOrStdout(), "OK   %-8s  %s\n", a.ID, a.Version)
					ok++
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "MISS %-8s  command not found (%s)\n", a.ID, a.Command)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d provider(s) available\n", ok)
			return nil
		},
	})
	return cmd
}
