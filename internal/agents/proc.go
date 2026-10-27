package agents

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	qmapty "github.com/fernandotonon/QtMesh-Agents/internal/pty"
)

// ProcSession is a PTY-backed AgentSession.
type ProcSession struct {
	sessionID string
	agentID   string
	pty       *qmapty.Session
	outCh     chan []byte
	once      sync.Once
}

// NewProcSession starts reading PTY output into a channel.
func NewProcSession(sessionID, agentID string, ptySession *qmapty.Session) *ProcSession {
	ps := &ProcSession{
		sessionID: sessionID,
		agentID:   agentID,
		pty:       ptySession,
		outCh:     make(chan []byte, 64),
	}
	go ps.readLoop()
	return ps
}

func (s *ProcSession) readLoop() {
	defer close(s.outCh)
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case s.outCh <- chunk:
			default:
				// Drop if consumer is slow; ring buffer lives in session manager.
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *ProcSession) ID() string      { return s.sessionID }
func (s *ProcSession) AgentID() string { return s.agentID }
func (s *ProcSession) PID() int        { return s.pty.PID() }
func (s *ProcSession) Wait() error {
	<-s.pty.Done()
	return s.pty.WaitErr()
}
func (s *ProcSession) Kill() error               { return s.pty.Kill() }
func (s *ProcSession) Write(data []byte) (int, error) { return s.pty.Write(data) }
func (s *ProcSession) Resize(cols, rows uint16) error { return s.pty.Resize(cols, rows) }
func (s *ProcSession) Output() <-chan []byte     { return s.outCh }
func (s *ProcSession) Done() <-chan struct{}     { return s.pty.Done() }

// BaseAdapter provides shared CLI detection and process helpers.
type BaseAdapter struct {
	id      string
	vendor  string
	command string
	args    []string
}

func NewBaseAdapter(id, vendor, command string, args []string) BaseAdapter {
	return BaseAdapter{id: id, vendor: vendor, command: command, args: args}
}

func (b BaseAdapter) ID() string      { return b.id }
func (b BaseAdapter) Vendor() string  { return b.vendor }
func (b BaseAdapter) Command() string { return b.command }
func (b BaseAdapter) Args() []string  { return append([]string(nil), b.args...) }

// Detect checks whether the command is available.
func (b BaseAdapter) Detect(ctx context.Context) (bool, error) {
	path, err := exec.LookPath(b.command)
	if err != nil {
		return false, nil
	}
	_ = path
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
		return true, nil
	}
}

// VersionProbe runs command with --version and returns trimmed output.
func (b BaseAdapter) VersionProbe(ctx context.Context, flags ...string) (string, error) {
	if len(flags) == 0 {
		flags = []string{"--version"}
	}
	cmd := exec.CommandContext(ctx, b.command, flags...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// StartPTY launches the agent command in a PTY.
func (b BaseAdapter) StartPTY(ctx context.Context, opts SessionOptions, extraArgs ...string) (*ProcSession, error) {
	args := append(append([]string{}, b.args...), extraArgs...)
	cmd := exec.CommandContext(ctx, b.command, args...)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	cmd.Env = os.Environ()
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	ptySession, err := qmapty.Start(cmd, opts.Cols, opts.Rows)
	if err != nil {
		return nil, err
	}
	sid := opts.SessionID
	if sid == "" {
		sid = fmt.Sprintf("%s-%d", b.id, time.Now().UnixNano())
	}
	return NewProcSession(sid, b.id, ptySession), nil
}

// RunHeadless runs a non-interactive command capturing stdout/stderr.
// Uses a process group so context cancel reaps the whole vendor CLI tree.
// When task.OnOutput is set, output is streamed so callers can detect stalls mid-run.
func (b BaseAdapter) RunHeadless(ctx context.Context, task AgentTask, args []string) (Execution, error) {
	started := time.Now()
	cmd := exec.CommandContext(ctx, b.command, args...)
	if task.WorkDir != "" {
		cmd.Dir = task.WorkDir
	}
	cmd.Env = os.Environ()
	for k, v := range task.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	setHeadlessProcGroup(cmd)

	watcher := NewOutputWatcher(task.OnOutput)
	cmd.Stdout = watcher
	cmd.Stderr = watcher

	err := cmd.Start()
	if err == nil {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			killHeadlessProcGroup(cmd)
			err = <-done
			if err == nil {
				err = ctx.Err()
			}
		}
	}
	ended := time.Now()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if ctx.Err() != nil {
			exitCode = -1
		} else {
			exitCode = -1
		}
	}
	id := task.ID
	if id == "" {
		id = fmt.Sprintf("%s-exec-%d", b.id, started.UnixNano())
	}
	return Execution{
		ID:        id,
		AgentID:   b.id,
		ExitCode:  exitCode,
		Output:    watcher.String(),
		StartedAt: started,
		EndedAt:   ended,
		Err:       err,
	}, nil
}

// OutputWatcher buffers process output and optionally streams chunks to OnOutput.
type OutputWatcher struct {
	mu  sync.Mutex
	buf bytes.Buffer
	on  func(chunk, full string)
}

// NewOutputWatcher creates a stdout/stderr writer that accumulates output and
// invokes on for each Write when non-nil.
func NewOutputWatcher(on func(chunk, full string)) *OutputWatcher {
	return &OutputWatcher{on: on}
}

func (w *OutputWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	full := w.buf.String()
	on := w.on
	w.mu.Unlock()
	if on != nil && n > 0 {
		on(string(p[:n]), full)
	}
	return n, err
}

func (w *OutputWatcher) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// ResolveCommand returns absolute path if found.
func ResolveCommand(command string) (string, error) {
	if filepath.IsAbs(command) {
		if _, err := os.Stat(command); err != nil {
			return "", err
		}
		return command, nil
	}
	return exec.LookPath(command)
}

// SetHeadlessProcGroup puts the command in its own process group (Unix) so
// KillHeadlessProcGroup can reap vendor CLI child trees on cancel.
func SetHeadlessProcGroup(cmd *exec.Cmd) { setHeadlessProcGroup(cmd) }

// KillHeadlessProcGroup kills the process group started by SetHeadlessProcGroup.
func KillHeadlessProcGroup(cmd *exec.Cmd) { killHeadlessProcGroup(cmd) }

// ScanLines is a helper for streaming command output in tests.
func ScanLines(r io.Reader, fn func(string)) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fn(sc.Text())
	}
	return sc.Err()
}
