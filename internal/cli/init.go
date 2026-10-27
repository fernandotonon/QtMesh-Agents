package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/claude"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/codex"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/cursor"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/mock"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newInitCmd() *cobra.Command {
	var nonInteractive bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up configuration, detect agents, and optionally register a project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirs(); err != nil {
				return err
			}
			path, err := config.Path()
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			if err := config.Save(path, cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)

			fmt.Fprintln(cmd.OutOrStdout(), "detected agents:")
			adapters := []agents.AgentAdapter{
				claude.New(cfg.Agents.Claude.Command, cfg.Agents.Claude.Args),
				codex.New(cfg.Agents.Codex.Command, cfg.Agents.Codex.Args),
				cursor.New(cfg.Agents.Cursor.Command, cfg.Agents.Cursor.Args),
				mock.New(cfg.Agents.Mock.Command, cfg.Agents.Mock.Args),
			}
			for _, a := range adapters {
				detected, err := a.Detect(cmd.Context())
				mark := "miss"
				if detected && err == nil {
					mark = "ok"
				}
				entry, _ := cfg.AgentByID(a.ID())
				fmt.Fprintf(cmd.OutOrStdout(), "  [%s] %s (%s)\n", mark, a.ID(), entry.Command)
			}
			if !nonInteractive && isTerminalInput(cmd.InOrStdin()) {
				reader := bufio.NewReader(cmd.InOrStdin())
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				if confirmSetup(cmd, reader, "Register current directory as a project ("+cwd+")? [y/N] ") {
					client, _, err := connectClient()
					if err != nil {
						return err
					}
					defer client.Close()
					projects, err := client.AddProject(cwd, "")
					if err != nil {
						return err
					}
					for _, p := range projects {
						fmt.Fprintln(cmd.OutOrStdout(), "registered", p.Name)
					}
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next steps:\n  qma project add .\n  qma project list\n  qma chat\n  qma agents list")
			return nil
		},
	}
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Use defaults without prompting")
	return cmd
}

func isTerminalInput(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func confirmSetup(cmd *cobra.Command, reader *bufio.Reader, prompt string) bool {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}
