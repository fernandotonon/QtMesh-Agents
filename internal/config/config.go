package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the global QtMesh Agents configuration.
type Config struct {
	Orchestrator OrchestratorConfig `yaml:"orchestrator"`
	Agents       AgentsConfig       `yaml:"agents"`
	Execution    ExecutionConfig    `yaml:"execution"`
	Routing      RoutingConfig      `yaml:"routing"`
	Storage      StorageConfig      `yaml:"storage"`
	UI           UIConfig           `yaml:"ui"`
	Daemon       DaemonConfig       `yaml:"daemon"`
}

type OrchestratorConfig struct {
	Provider               string `yaml:"provider"`
	MaxPlanningIterations  int    `yaml:"maxPlanningIterations"`
	RequireApprovalForMerge bool  `yaml:"requireApprovalForMerge"`
}

type AgentsConfig struct {
	Claude AgentEntry `yaml:"claude"`
	Codex  AgentEntry `yaml:"codex"`
	Cursor AgentEntry `yaml:"cursor"`
	Mock   AgentEntry `yaml:"mock"`
}

type AgentEntry struct {
	Enabled        bool     `yaml:"enabled"`
	Command        string   `yaml:"command"`
	Args           []string `yaml:"args,omitempty"`
	Sandbox        string   `yaml:"sandbox,omitempty"`        // codex: read-only|workspace-write|danger-full-access
	ApproveForMe   bool     `yaml:"approveForMe,omitempty"`   // codex: force approval_policy=never for headless workers
	PermissionMode string   `yaml:"permissionMode,omitempty"` // claude: acceptEdits|...
}

type ExecutionConfig struct {
	MaxConcurrentAgents int  `yaml:"maxConcurrentAgents"`
	TimeoutMinutes      int  `yaml:"timeoutMinutes"`
	MaxRetries          int  `yaml:"maxRetries"`
	UseGitWorktrees     bool `yaml:"useGitWorktrees"`
}

type RoutingConfig struct {
	Strategy            string            `yaml:"strategy"`
	Preferences         map[string]string `yaml:"preferences"`
	Fallback            []string          `yaml:"fallback"`
	// LeadershipFallback is the ordered preference for the project bench leader
	// (and AskLeader consults). When the seated leader hits quota/token exhaustion,
	// the next available entry takes over. Default: claude → cursor → codex.
	LeadershipFallback  []string          `yaml:"leadershipFallback"`
	MaxConcurrentAgents int               `yaml:"maxConcurrentAgents"`
}

type StorageConfig struct {
	Driver string `yaml:"driver"`
	Path   string `yaml:"path,omitempty"`
}

type UIConfig struct {
	Theme     string `yaml:"theme"`
	ShowUsage bool   `yaml:"showUsage"`
}

type DaemonConfig struct {
	SocketPath    string `yaml:"socketPath,omitempty"`
	LogPath       string `yaml:"logPath,omitempty"`
	HTTPEnabled   bool   `yaml:"httpEnabled"`
	HTTPListen    string `yaml:"httpListen,omitempty"`    // e.g. 0.0.0.0:7432 or 100.x:7432
	HTTPAuthToken string `yaml:"httpAuthToken,omitempty"` // empty => auto-generate when HTTP enabled
}

// Default returns a sensible default configuration.
func Default() Config {
	return Config{
		Orchestrator: OrchestratorConfig{
			Provider:                "cursor", // full orch agent brain; falls back if unavailable
			MaxPlanningIterations:   5,
			RequireApprovalForMerge: true,
		},
		Agents: AgentsConfig{
			Claude: AgentEntry{Enabled: true, Command: "claude", PermissionMode: "acceptEdits"},
			Codex:  AgentEntry{Enabled: true, Command: "codex", Sandbox: "danger-full-access", ApproveForMe: true},
			Cursor: AgentEntry{Enabled: true, Command: "cursor-agent"},
			Mock:   AgentEntry{Enabled: true, Command: "qma-mock-agent"},
		},
		Execution: ExecutionConfig{
			MaxConcurrentAgents: 3,
			TimeoutMinutes:      30,
			MaxRetries:          2,
			UseGitWorktrees:     true,
		},
		Routing: RoutingConfig{
			Strategy: "balanced",
			Preferences: map[string]string{
				"architecture":   "claude",
				"implementation": "cursor",
				"leadership":     "claude",
				"frontend":       "codex",
				"testing":        "codex",
				"review":         "claude",
			},
			Fallback:            []string{"codex", "claude", "cursor"},
			LeadershipFallback:  []string{"claude", "cursor", "codex"},
			MaxConcurrentAgents: 3,
		},
		Storage: StorageConfig{Driver: "sqlite"},
		UI:      UIConfig{Theme: "dark", ShowUsage: true},
		Daemon: DaemonConfig{
			HTTPEnabled: true,
			HTTPListen:  "0.0.0.0:7432",
		},
	}
}

// Validate checks configuration invariants.
func (c Config) Validate() error {
	var errs []string
	if c.Execution.MaxConcurrentAgents < 1 {
		errs = append(errs, "execution.maxConcurrentAgents must be >= 1")
	}
	if c.Execution.TimeoutMinutes < 1 {
		errs = append(errs, "execution.timeoutMinutes must be >= 1")
	}
	if c.Storage.Driver != "" && c.Storage.Driver != "sqlite" {
		errs = append(errs, "storage.driver must be sqlite")
	}
	if c.Orchestrator.MaxPlanningIterations < 1 {
		errs = append(errs, "orchestrator.maxPlanningIterations must be >= 1")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// AgentByID returns the agent entry for a known adapter ID.
func (c Config) AgentByID(id string) (AgentEntry, bool) {
	switch strings.ToLower(id) {
	case "claude":
		return c.Agents.Claude, true
	case "codex":
		return c.Agents.Codex, true
	case "cursor":
		return c.Agents.Cursor, true
	case "mock":
		return c.Agents.Mock, true
	default:
		return AgentEntry{}, false
	}
}

// Dir returns the OS-appropriate config directory.
func Dir() (string, error) {
	if v := os.Getenv("QTMESH_AGENTS_CONFIG_DIR"); v != "" {
		return v, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "qtmesh-agents"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "qtmesh-agents"), nil
}

// DataDir returns the data directory for SQLite, logs, and sockets.
func DataDir() (string, error) {
	if v := os.Getenv("QTMESH_AGENTS_DATA_DIR"); v != "" {
		return v, nil
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "qtmesh-agents"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "qtmesh-agents"), nil
}

// RuntimeDir returns the directory for daemon sockets and locks.
func RuntimeDir() (string, error) {
	if v := os.Getenv("QTMESH_AGENTS_RUNTIME_DIR"); v != "" {
		return v, nil
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "qtmesh-agents"), nil
	}
	data, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "run"), nil
}

// Path returns the default config file path.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// EnsureDirs creates config, data, and runtime directories.
func EnsureDirs() error {
	for _, fn := range []func() (string, error){Dir, DataDir, RuntimeDir} {
		d, err := fn()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	logs, err := DataDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(logs, "logs"), 0o700)
}
