package cli

import (
	"fmt"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"github.com/spf13/cobra"
)

func newSpawnCmd() *cobra.Command {
	var cols, rows int
	cmd := &cobra.Command{
		Use:   "spawn [agent] [prompt...]",
		Short: "Spawn an interactive agent session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			req := ipc.SpawnRequest{
				AgentID: args[0],
				Cols:    uint16(cols),
				Rows:    uint16(rows),
			}
			if len(args) > 1 {
				req.Prompt = strings.Join(args[1:], " ")
			}
			resp, err := client.Spawn(req)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session %s (pid %d)\n", resp.SessionID, resp.PID)
			return nil
		},
	}
	cmd.Flags().IntVar(&cols, "cols", 120, "terminal columns")
	cmd.Flags().IntVar(&rows, "rows", 40, "terminal rows")
	return cmd
}

func newSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List agent sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			list, err := client.ListSessions()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no sessions")
				return nil
			}
			for _, s := range list {
				fmt.Fprintf(cmd.OutOrStdout(), "%-28s  %-8s  %-10s  pid=%d  %s\n",
					s.ID, s.AgentID, s.Status, s.PID, s.CreatedAt)
			}
			return nil
		},
	}
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <session-id>",
		Short: "Stop a running session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			if err := client.Stop(args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "stopped", args[0])
			return nil
		},
	}
}

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <session-id>",
		Short: "Attach to a running session (raw terminal)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttach(args[0])
		},
	}
}
