package cli

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/daemon"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"github.com/fernandotonon/QtMesh-Agents/internal/web"
	"github.com/spf13/cobra"
)

func newWebCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Open or manage the web workspace",
		Long: `Web workspace is served by the daemon (daemon.httpEnabled).

  qma web              print URL (with token) and open the browser
  qma web token        show the current auth token
  qma web token --new  regenerate and save a new token (restart daemon after)

Bind with daemon.httpListen (Tailscale IP or 0.0.0.0:7432). Prefer Tailscale
Serve for TLS on the network; Bearer auth is required on every /api and /ws route.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			socket, err := ipc.DefaultSocketPath()
			if err != nil {
				return err
			}
			if err := daemon.EnsureRunning(socket); err != nil {
				return err
			}
			cfg, err := config.LoadDefault()
			if err != nil {
				return err
			}
			if !cfg.Daemon.HTTPEnabled {
				return fmt.Errorf("daemon.httpEnabled is false — enable it in config and restart the daemon")
			}
			tok := strings.TrimSpace(cfg.Daemon.HTTPAuthToken)
			if tok == "" {
				return fmt.Errorf("no httpAuthToken yet — start the daemon once to auto-generate, or set daemon.httpAuthToken")
			}
			url := workspaceURL(cfg) + "?token=" + tok
			fmt.Fprintln(cmd.OutOrStdout(), url)
			_ = openBrowser(url)
			return nil
		},
	}
	cmd.AddCommand(newWebTokenCmd())
	return cmd
}

func newWebTokenCmd() *cobra.Command {
	var regen bool
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Show or regenerate the web auth token",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			if regen || strings.TrimSpace(cfg.Daemon.HTTPAuthToken) == "" {
				tok, _, err := web.EnsureToken(&cfg, nil)
				if err != nil {
					return err
				}
				cfg.Daemon.HTTPAuthToken = tok
				cfg.Daemon.HTTPEnabled = true
				if cfg.Daemon.HTTPListen == "" {
					cfg.Daemon.HTTPListen = "0.0.0.0:7432"
				}
				if err := config.Save(path, cfg); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), tok)
				if regen {
					fmt.Fprintln(cmd.ErrOrStderr(), "token saved — restart the daemon for it to take effect")
				}
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), cfg.Daemon.HTTPAuthToken)
			return nil
		},
	}
	cmd.Flags().BoolVar(&regen, "new", false, "generate and save a new token")
	return cmd
}

func workspaceURL(cfg config.Config) string {
	listen := strings.TrimSpace(cfg.Daemon.HTTPListen)
	if listen == "" {
		listen = "127.0.0.1:7432"
	}
	host := listen
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	} else if strings.HasPrefix(host, "[::]:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "[::]:")
	} else if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	return "http://" + host + "/"
}

func openBrowser(url string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	return c.Start()
}
