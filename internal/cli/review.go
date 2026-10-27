package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
	"github.com/spf13/cobra"
)

func newReviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "List or show static review reports for runs",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recent review reports",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()
			list, err := store.ListRecentReviews(cmd.Context(), 30)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no reviews yet")
				return nil
			}
			for _, r := range list {
				mark := "✗"
				if r.Approved {
					mark = "✓"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  run=%s task=%s  %s\n",
					mark, r.CreatedAt.Format("15:04:05"), short(r.RunID), r.TaskID, r.Summary)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show <run-id>",
		Short: "Show review findings for a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()
			list, err := store.ListReviewsByRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(list) == 0 {
				// Allow short suffix match via recent list.
				recent, _ := store.ListRecentReviews(cmd.Context(), 50)
				for _, r := range recent {
					if strings.HasSuffix(r.RunID, args[0]) || strings.Contains(r.RunID, args[0]) {
						list = append(list, r)
					}
				}
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no reviews for", args[0])
				return nil
			}
			for _, r := range list {
				mark := "blocked"
				if r.Approved {
					mark = "ok"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "=== %s  task=%s  %s ===\n%s\n", r.ID, r.TaskID, mark, r.Summary)
				if r.Findings != "" {
					var findings []map[string]string
					if json.Unmarshal([]byte(r.Findings), &findings) == nil {
						for _, f := range findings {
							fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s: %s\n", f["Severity"], f["Path"], f["Message"])
						}
					} else {
						fmt.Fprintln(cmd.OutOrStdout(), r.Findings)
					}
				}
				if snip := strings.TrimSpace(r.DiffSnippet); snip != "" {
					lines := strings.Split(snip, "\n")
					if len(lines) > 40 {
						lines = append(lines[:40], "…")
					}
					fmt.Fprintln(cmd.OutOrStdout(), "--- diff (trimmed) ---")
					fmt.Fprintln(cmd.OutOrStdout(), strings.Join(lines, "\n"))
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	})
	return cmd
}

func newMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Show recent run metrics (duration, retries, success)",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()
			list, err := store.ListRunMetrics(cmd.Context(), 20)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no metrics yet")
				return nil
			}
			for _, m := range list {
				mark := "fail"
				if m.Success {
					mark = "ok"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-6s  %6dms  retries=%d  agent=%s  run=%s\n",
					m.CreatedAt.Format("2006-01-02 15:04"), mark, m.DurationMS, m.Retries, m.AgentID, short(m.RunID))
			}
			return nil
		},
	}
}

func openStore() (*storage.Store, error) {
	if err := config.EnsureDirs(); err != nil {
		return nil, err
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	return storage.Open(filepath.Join(dataDir, "qtmesh-agents.db"))
}

func short(id string) string {
	if len(id) > 24 {
		return id[:12] + "…" + id[len(id)-6:]
	}
	return id
}
