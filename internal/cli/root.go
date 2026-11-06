package cli

import (
	"fmt"
	"os"

	"github.com/fernandotonon/QtMesh-Agents/pkg/version"
	"github.com/spf13/cobra"
)

// NewRoot builds the qma command tree.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "qma",
		Short:         "QtMesh Agents — multi-vendor AI coding agent orchestrator",
		Long:          "One terminal. Multiple AI agents. Unified orchestration.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd)
		},
	}

	root.AddCommand(newDaemonCmd())
	root.AddCommand(newSpawnCmd())
	root.AddCommand(newSessionsCmd())
	root.AddCommand(newAttachCmd())
	root.AddCommand(newStopCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newHistoryCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newAgentsCmd())
	root.AddCommand(newProjectCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newChatCmd())
	root.AddCommand(newAttentionCmd())
	root.AddCommand(newReviewCmd())
	root.AddCommand(newMetricsCmd())
	root.AddCommand(newWebCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newInitCmd())

	// Long alias is provided via a second main or symlink; document in README.
	root.Aliases = []string{"qtmesh-agents"}

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
		},
	}
}

func Execute() {
	root := NewRoot()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
