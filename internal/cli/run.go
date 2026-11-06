package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var (
		follow   bool
		dryRun   bool
		agent    string
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "run [flags] <objective>",
		Short: "Submit an autonomous orchestration objective",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			objective := strings.Join(args, " ")
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if dryRun {
				return printDryPlan(cmd, client, objective, agent)
			}

			resp, err := client.RunStart(ipc.RunStartRequest{
				Objective:     objective,
				AgentOverride: agent,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run started: %s\n", resp.RunID)
			if !follow {
				fmt.Fprintf(cmd.OutOrStdout(), "watch with: qma run status %s --follow\n", resp.RunID)
				return nil
			}
			return followRun(cmd, client, resp.RunID, interval)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow run progress until completion")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without executing")
	cmd.Flags().StringVar(&agent, "agent", "", "force all tasks onto one agent (e.g. mock, claude)")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "follow poll interval")

	cmd.AddCommand(newRunStatusCmd())
	cmd.AddCommand(newRunListCmd())
	cmd.AddCommand(newRunCancelCmd())
	cmd.AddCommand(newRunResumeCmd())
	cmd.AddCommand(newRunDiffCmd())
	return cmd
}

func printDryPlan(cmd *cobra.Command, client *ipc.Client, objective, agent string) error {
	_ = client
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	eng := newLocalPlanner(cfg)
	plan, err := eng.CreatePlan(cmd.Context(), planReq(objective, agent))
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "plan (dry-run, not executed):")
	for _, t := range plan.Tasks {
		fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %-12s agent=%-8s deps=%v\n  %s\n",
			t.ID, t.Category, orDash(t.AssignedAgent), t.Dependencies, t.Description)
	}
	return nil
}

func newRunStatusCmd() *cobra.Command {
	var follow bool
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "status <run-id>",
		Short: "Show orchestration run status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			if follow {
				return followRun(cmd, client, args[0], interval)
			}
			info, err := client.RunStatus(args[0])
			if err != nil {
				return err
			}
			printRun(cmd, info)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow until completion")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "follow poll interval")
	return cmd
}

func newRunListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List recent orchestration runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			list, err := client.RunList()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no runs")
				return nil
			}
			for _, r := range list {
				fmt.Fprintf(cmd.OutOrStdout(), "%-28s  %-10s  %s\n", r.ID, r.Status, truncate(r.Objective, 60))
			}
			return nil
		},
	}
}

func newRunCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a running orchestration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			if err := client.RunCancel(args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "cancel requested for", args[0])
			return nil
		},
	}
}

func newRunResumeCmd() *cobra.Command {
	var follow bool
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume an incomplete run (unstick blocked verify/integrate)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			resp, err := client.RunResume(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "resumed: %s\n", resp.RunID)
			if follow {
				return followRun(cmd, client, resp.RunID, interval)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "watch with: qma run status %s --follow\n", resp.RunID)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow run progress until completion")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "follow poll interval")
	return cmd
}

func newRunDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <run-id> [task-id]",
		Short: "Show collected diffs for a run or task",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			info, err := client.RunStatus(args[0])
			if err != nil {
				return err
			}
			taskFilter := ""
			if len(args) > 1 {
				taskFilter = args[1]
			}
			shown := 0
			for _, t := range info.Tasks {
				if taskFilter != "" && t.ID != taskFilter {
					continue
				}
				if t.DiffPath == "" {
					continue
				}
				data, err := os.ReadFile(t.DiffPath)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "# %s: %v\n", t.ID, err)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "### %s (%s)\n", t.ID, t.DiffPath)
				_, _ = cmd.OutOrStdout().Write(data)
				if len(data) > 0 && data[len(data)-1] != '\n' {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				shown++
			}
			if shown == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no diffs collected yet")
			}
			return nil
		},
	}
}

func followRun(cmd *cobra.Command, client *ipc.Client, runID string, interval time.Duration) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		info, err := client.RunStatus(runID)
		if err != nil {
			return err
		}
		printRun(cmd, info)
		switch info.Status {
		case "succeeded", "failed", "cancelled":
			return nil
		case "needs_attention":
			fmt.Fprintln(cmd.OutOrStdout(), "---")
			fmt.Fprintln(cmd.OutOrStdout(), "paused for attention — resolve with TUI NEEDS YOU or:")
			fmt.Fprintln(cmd.OutOrStdout(), "  qma attention list")
			fmt.Fprintln(cmd.OutOrStdout(), "  qma attention reply <run-id> <task-id> \"Human reply: Approve apply to main working tree.\"")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "---")
		time.Sleep(interval)
	}
}

func printRun(cmd *cobra.Command, info ipc.RunInfo) {
	fmt.Fprintf(cmd.OutOrStdout(), "run: %s\nstatus: %s\nplanner: %s\nobjective: %s\n",
		info.ID, info.Status, info.Planner, info.Objective)
	if info.Summary != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "summary: %s\n", info.Summary)
	}
	if info.Error != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "error: %s\n", info.Error)
	}
	for _, t := range info.Tasks {
		agent := t.AssignedAgent
		if agent == "" {
			agent = "-"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %-10s  agent=%-8s  %s\n", t.ID, t.Status, agent, truncate(t.Description, 50))
		if t.Error != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "           error: %s\n", truncate(t.Error, 80))
		}
		if t.DiffPath != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "           diff: %s\n", t.DiffPath)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
