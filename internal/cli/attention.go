package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newAttentionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attention",
		Short: "List or resolve items that need your attention (permissions, approvals)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Show open attention marks",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			list, err := client.AttentionList()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no open attention marks")
				return nil
			}
			for _, a := range list {
				fmt.Fprintf(cmd.OutOrStdout(), "⚠ %s  [%s] %s", a.ID, a.Kind, a.Message)
				if a.RunID != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  run=%s", a.RunID)
				}
				if a.AgentID != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  agent=%s", a.AgentID)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "resolve <id>",
		Short: "Mark an attention item as resolved",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			if err := client.AttentionResolve(args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "resolved", args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "reply <run-id> <task-id> <message>",
		Short: "Reply to a paused task (e.g. approve merge apply)",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			msg := strings.Join(args[2:], " ")
			if err := client.TaskReply(args[0], args[1], msg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "reply sent; run resuming")
			return nil
		},
	})
	return cmd
}
