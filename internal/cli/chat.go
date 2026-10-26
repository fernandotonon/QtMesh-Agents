package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"github.com/spf13/cobra"
)

func newChatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat [message]",
		Short: "Talk to the top-level orchestrator across all projects",
		Long: `Conversational multi-project orchestrator.

You do not need to select an active project. Mention project names in your
message; the orchestrator can start runs on one or many registered workspaces.

Examples:
  qma chat
  qma chat "What projects do you know about?"
  qma chat "In QtMeshEditor, add hair materials; in Data Core Clash, fix matchmaking"
`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if len(args) > 0 {
				return sendChat(cmd, client, strings.Join(args, " "))
			}

			fmt.Fprintln(cmd.OutOrStdout(), "QtMesh orchestrator chat. Type a message, /projects, /runs, /clear, or /quit.")
			in := bufio.NewScanner(os.Stdin)
			for {
				fmt.Fprint(cmd.OutOrStdout(), "you> ")
				if !in.Scan() {
					break
				}
				line := strings.TrimSpace(in.Text())
				if line == "" {
					continue
				}
				switch strings.ToLower(line) {
				case "/quit", "/exit", "quit", "exit":
					return nil
				case "/clear":
					if err := client.ChatClear(); err != nil {
						return err
					}
					fmt.Fprintln(cmd.OutOrStdout(), "(conversation cleared)")
					continue
				case "/projects":
					list, err := client.ListProjects()
					if err != nil {
						return err
					}
					if len(list) == 0 {
						fmt.Fprintln(cmd.OutOrStdout(), "no projects — qma project add <path>")
						continue
					}
					for _, p := range list {
						fmt.Fprintf(cmd.OutOrStdout(), "  %-20s  %s\n", p.Name, p.Path)
					}
					continue
				case "/runs":
					runs, err := client.RunList()
					if err != nil {
						return err
					}
					if len(runs) == 0 {
						fmt.Fprintln(cmd.OutOrStdout(), "no runs")
						continue
					}
					for i, r := range runs {
						if i >= 10 {
							break
						}
						fmt.Fprintf(cmd.OutOrStdout(), "  %-28s  %-10s  %s\n", r.ID, r.Status, truncate(r.Objective, 50))
					}
					continue
				case "/help", "help":
					fmt.Fprintln(cmd.OutOrStdout(), "Talk naturally. Mention project names to target work.")
					fmt.Fprintln(cmd.OutOrStdout(), "Commands: /projects /runs /clear /quit")
					continue
				}
				if err := sendChat(cmd, client, line); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
				}
			}
			return in.Err()
		},
	}
}

func sendChat(cmd *cobra.Command, client *ipc.Client, msg string) error {
	resp, err := client.Chat(msg)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "orch>", resp.Reply)
	for _, a := range resp.Actions {
		if a.RunID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  ↳ run %s on %s\n", a.RunID, a.ProjectID)
		}
	}
	return nil
}
