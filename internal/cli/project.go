package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "add <path>",
		Short: "Register a project directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			list, err := client.AddProject(args[0], "")
			if err != nil {
				return err
			}
			for _, p := range list {
				fmt.Fprintf(cmd.OutOrStdout(), "added %s (%s)\n", p.Name, p.Path)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			list, err := client.ListProjects()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no projects")
				return nil
			}
			for _, p := range list {
				mark := " "
				if p.Active {
					mark = "*"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %-20s  %s\n", mark, p.Name, p.Path)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "use <name-or-id>",
		Short: "Set the active project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := connectClient()
			if err != nil {
				return err
			}
			defer client.Close()
			if err := client.UseProject(args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "active project:", args[0])
			return nil
		},
	})
	return cmd
}
