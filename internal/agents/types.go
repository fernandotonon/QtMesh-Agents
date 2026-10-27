package agents

import (
	"context"
	"time"
)

// AgentCapabilities describes what an adapter supports.
type AgentCapabilities struct {
	Interactive      bool
	Headless         bool
	SessionRestore   bool
	StructuredOutput bool
	UsageReporting   bool
	Version          string
}

// SessionOptions configures an interactive session start.
type SessionOptions struct {
	SessionID   string
	ProjectID   string
	WorkDir     string
	Env         map[string]string
	Cols        uint16
	Rows        uint16
	ExtraArgs   []string
	ResumeID    string
}

// AgentTask is a headless execution request.
type AgentTask struct {
	ID          string
	Description string
	WorkDir     string
	Prompt      string
	Timeout     time.Duration
	ExtraArgs   []string
	Env         map[string]string
	// ElevatedSandbox requests maximum sandbox access for this run (Codex: danger-full-access).
	ElevatedSandbox bool
	// InteractiveApprovals lets the vendor CLI prompt the human (e.g. ChatGPT/Codex app).
	InteractiveApprovals bool
	// OnOutput is called with each stdout/stderr chunk and the full buffer so far.
	// Used to detect mid-run approval / auth stalls before the full timeout.
	OnOutput func(chunk, full string)
}

// Execution captures headless run results.
type Execution struct {
	ID         string
	AgentID    string
	ExitCode   int
	Output     string
	StartedAt  time.Time
	EndedAt    time.Time
	Err        error
	Usage      Usage
	ChangedFiles []string
}

// Usage holds optional token/cost metrics.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	CostUSD      float64
	Known        bool
}

// AgentSession is a live interactive agent process.
type AgentSession interface {
	ID() string
	AgentID() string
	PID() int
	Wait() error
	Kill() error
	Write(data []byte) (int, error)
	Resize(cols, rows uint16) error
	Output() <-chan []byte
	Done() <-chan struct{}
}

// AgentAdapter is the core vendor-independent adapter.
type AgentAdapter interface {
	ID() string
	Vendor() string
	Detect(ctx context.Context) (bool, error)
	Capabilities(ctx context.Context) (AgentCapabilities, error)
	StartSession(ctx context.Context, opts SessionOptions) (AgentSession, error)
	Execute(ctx context.Context, task AgentTask) (Execution, error)
}

// InteractiveAgent optionally exposes interactive helpers.
type InteractiveAgent interface {
	SupportsInteractive() bool
}

// CancellableAgent optionally supports cooperative cancel.
type CancellableAgent interface {
	Cancel(ctx context.Context, sessionID string) error
}

// UsageReporter optionally reports usage after execution.
type UsageReporter interface {
	LastUsage() Usage
}

// SessionRestorer optionally resumes prior provider sessions.
type SessionRestorer interface {
	RestoreSession(ctx context.Context, opts SessionOptions) (AgentSession, error)
}
