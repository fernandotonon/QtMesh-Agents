package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/daemon"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background daemon",
	}
	cmd.AddCommand(newDaemonStartCmd())
	cmd.AddCommand(newDaemonStopCmd())
	cmd.AddCommand(newDaemonStatusCmd())
	return cmd
}

func newDaemonStartCmd() *cobra.Command {
	var foreground bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			socket, err := ipc.DefaultSocketPath()
			if err != nil {
				return err
			}
			if ipc.IsDaemonAlive(socket) {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon already running")
				return nil
			}
			if !foreground {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				c := execBackground(exe, "daemon", "start", "--foreground")
				if err := c.Start(); err != nil {
					return err
				}
				_ = c.Process.Release()
				fmt.Fprintln(cmd.OutOrStdout(), "daemon starting")
				return nil
			}
			return runDaemonForeground(cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", false, "run in foreground")
	return cmd
}

func runDaemonForeground(w io.Writer) error {
	// Survive parent shell exit / hangup when launched via EnsureRunning.
	signal.Ignore(syscall.SIGHUP)

	if err := config.EnsureDirs(); err != nil {
		return err
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	dataDir, _ := config.DataDir()
	logPath := filepath.Join(dataDir, "logs", "daemon.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(w, logFile), &slog.HandlerOptions{Level: slog.LevelInfo}))
	socket, err := ipc.DefaultSocketPath()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := daemon.Start(ctx, daemon.Options{Config: cfg, SocketPath: socket, Logger: logger})
	if err != nil {
		return err
	}
	_ = daemon.WritePid(socket)
	defer daemon.RemovePid(socket)

	<-ctx.Done()
	d.Stop()
	return nil
}

func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			socket, err := ipc.DefaultSocketPath()
			if err != nil {
				return err
			}
			if ipc.IsDaemonAlive(socket) {
				client, err := ipc.Connect(socket)
				if err == nil {
					_ = client.Shutdown()
					_ = client.Close()
				}
			}
			// Reap leftover daemon processes / stale socket (duplicates caused stale UI).
			daemon.ForceCleanup(socket)
			fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			return nil
		},
	}
}

func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			socket, err := ipc.DefaultSocketPath()
			if err != nil {
				return err
			}
			if !ipc.IsDaemonAlive(socket) {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon: stopped")
				fmt.Fprintf(cmd.OutOrStdout(), "socket: %s\n", socket)
				return nil
			}
			client, err := ipc.Connect(socket)
			if err != nil {
				return err
			}
			defer client.Close()
			st, err := client.Status()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "daemon: running\npid: %d\nsessions: %d\nversion: %s\nsocket: %s\n",
				st.PID, st.Sessions, st.Version, st.SocketPath)
			return nil
		},
	}
}
