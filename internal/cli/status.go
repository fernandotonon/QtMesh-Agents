package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show overall status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			st, err := client.Status()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "daemon: running (pid %d)\n", st.PID)
			fmt.Fprintf(cmd.OutOrStdout(), "sessions: %d\n", st.Sessions)
			fmt.Fprintf(cmd.OutOrStdout(), "version: %s\n", st.Version)

			agents, err := client.ListAgents()
			if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "agents:")
				for _, a := range agents {
					mark := "○"
					if a.Detected {
						mark = "●"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %s %-8s  %s\n", mark, a.ID, a.Version)
				}
			}
			return nil
		},
	}
}

func newHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "Show recent execution events",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirs(); err != nil {
				return err
			}
			dataDir, err := config.DataDir()
			if err != nil {
				return err
			}
			store, err := storage.Open(filepath.Join(dataDir, "qtmesh-agents.db"))
			if err != nil {
				return err
			}
			defer store.Close()
			evts, err := store.ListEvents(cmd.Context(), 50)
			if err != nil {
				return err
			}
			if len(evts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no events")
				return nil
			}
			for _, e := range evts {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-20s  %s  %s\n",
					e["timestamp"], e["kind"], e["agent_id"], e["message"])
			}
			return nil
		},
	}
}

func newLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <session-or-execution-id>",
		Short: "Show persisted session logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := config.DataDir()
			if err != nil {
				return err
			}
			path := filepath.Join(dataDir, "logs", args[0]+".log")
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}
