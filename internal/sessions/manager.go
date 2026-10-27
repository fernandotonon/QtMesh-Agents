package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

const (
	StatusRunning     = "running"
	StatusStopped     = "stopped"
	StatusExited      = "exited"
	StatusInterrupted = "interrupted"
)

// RingBuffer keeps a bounded recent output history for reattach.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
}

func NewRingBuffer(size int) *RingBuffer {
	if size <= 0 {
		size = 256 * 1024
	}
	return &RingBuffer{size: size}
}

func (r *RingBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.size {
		r.buf = r.buf[len(r.buf)-r.size:]
	}
}

func (r *RingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// LiveSession is an in-memory running session.
type LiveSession struct {
	Info     storage.SessionRecord
	Agent    agents.AgentSession
	Buffer   *RingBuffer
	LogFile  *os.File
	mu       sync.Mutex
	watchers map[chan []byte]struct{}
}

func (ls *LiveSession) Subscribe() chan []byte {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ch := make(chan []byte, 32)
	ls.watchers[ch] = struct{}{}
	return ch
}

func (ls *LiveSession) Unsubscribe(ch chan []byte) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	delete(ls.watchers, ch)
	close(ch)
}

func (ls *LiveSession) broadcast(data []byte) {
	ls.Buffer.Write(data)
	if ls.LogFile != nil {
		_, _ = ls.LogFile.Write(data)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for ch := range ls.watchers {
		select {
		case ch <- data:
		default:
		}
	}
}

// Manager owns live sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*LiveSession
	store    *storage.Store
	registry *agents.Registry
	logDir   string
}

func NewManager(store *storage.Store, registry *agents.Registry, logDir string) *Manager {
	return &Manager{
		sessions: make(map[string]*LiveSession),
		store:    store,
		registry: registry,
		logDir:   logDir,
	}
}

// SpawnOpts configures a new interactive session.
type SpawnOpts struct {
	AgentID   string
	ProjectID string
	WorkDir   string
	Cols      uint16
	Rows      uint16
	ResumeID  string
	ExtraArgs []string
	Prompt    string
}

// Spawn starts a new interactive agent session.
func (m *Manager) Spawn(ctx context.Context, opts SpawnOpts) (*LiveSession, error) {
	adapter, err := m.registry.Require(opts.AgentID)
	if err != nil {
		return nil, err
	}
	ok, err := adapter.Detect(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("agent %q not found on PATH", opts.AgentID)
	}

	id := fmt.Sprintf("%s-%d", opts.AgentID, time.Now().UnixNano())
	logPath := filepath.Join(m.logDir, id+".log")
	_ = os.MkdirAll(m.logDir, 0o700)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}

	extra := append([]string{}, opts.ExtraArgs...)
	if opts.Prompt != "" {
		extra = append(extra, opts.Prompt)
	}
	sessOpts := agents.SessionOptions{
		SessionID: id,
		ProjectID: opts.ProjectID,
		WorkDir:   opts.WorkDir,
		Cols:      opts.Cols,
		Rows:      opts.Rows,
		ResumeID:  opts.ResumeID,
		ExtraArgs: extra,
	}
	agentSess, err := adapter.StartSession(ctx, sessOpts)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}

	rec := storage.SessionRecord{
		ID:        id,
		AgentID:   opts.AgentID,
		ProjectID: opts.ProjectID,
		WorkDir:   opts.WorkDir,
		Status:    StatusRunning,
		PID:       agentSess.PID(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		LogPath:   logPath,
		ResumeID:  opts.ResumeID,
	}
	if err := m.store.UpsertSession(ctx, rec); err != nil {
		_ = agentSess.Kill()
		_ = logFile.Close()
		return nil, err
	}

	ls := &LiveSession{
		Info:     rec,
		Agent:    agentSess,
		Buffer:   NewRingBuffer(512 * 1024),
		LogFile:  logFile,
		watchers: make(map[chan []byte]struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = ls
	m.mu.Unlock()

	go m.pump(ls)
	return ls, nil
}

func (m *Manager) pump(ls *LiveSession) {
	for {
		select {
		case data, ok := <-ls.Agent.Output():
			if !ok {
				m.markExited(ls, nil)
				return
			}
			ls.broadcast(data)
		case <-ls.Agent.Done():
			// Drain remaining output briefly.
			deadline := time.After(200 * time.Millisecond)
		drain:
			for {
				select {
				case data, ok := <-ls.Agent.Output():
					if !ok {
						break drain
					}
					ls.broadcast(data)
				case <-deadline:
					break drain
				}
			}
			err := ls.Agent.Wait()
			m.markExited(ls, err)
			return
		}
	}
}

func (m *Manager) markExited(ls *LiveSession, waitErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	ls.Info.Status = StatusExited
	ls.Info.EndedAt = &now
	ls.Info.UpdatedAt = now
	code := 0
	if waitErr != nil {
		code = 1
	}
	ls.Info.ExitCode = &code
	_ = m.store.UpsertSession(context.Background(), ls.Info)
	if ls.LogFile != nil {
		_ = ls.LogFile.Close()
		ls.LogFile = nil
	}
}

func (m *Manager) Get(id string) (*LiveSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ls, ok := m.sessions[id]
	return ls, ok
}

func (m *Manager) ListLive() []*LiveSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*LiveSession, 0, len(m.sessions))
	for _, ls := range m.sessions {
		out = append(out, ls)
	}
	return out
}

func (m *Manager) Stop(id string) error {
	ls, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("session %q not running", id)
	}
	return ls.Agent.Kill()
}

func (m *Manager) Write(id string, data []byte) error {
	ls, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("session %q not running", id)
	}
	_, err := ls.Agent.Write(data)
	return err
}

func (m *Manager) Resize(id string, cols, rows uint16) error {
	ls, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("session %q not running", id)
	}
	return ls.Agent.Resize(cols, rows)
}

// RecoverOnStart marks previously-running sessions as interrupted if PIDs are gone,
// then drops non-running session rows so the TUI isn't cluttered with dead PTYs.
func (m *Manager) RecoverOnStart(ctx context.Context) error {
	recs, err := m.store.ListSessions(ctx)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.Status != StatusRunning {
			continue
		}
		alive := false
		if rec.PID > 0 {
			alive = processAlive(rec.PID)
		}
		if !alive {
			now := time.Now().UTC()
			rec.Status = StatusInterrupted
			rec.EndedAt = &now
			rec.UpdatedAt = now
			_ = m.store.UpsertSession(ctx, rec)
		}
	}
	_, _ = m.store.DeleteNonRunningSessions(ctx)
	return nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return checkPID(pid)
}

func (m *Manager) CountRunning() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, ls := range m.sessions {
		if ls.Info.Status == StatusRunning {
			n++
		}
	}
	return n
}
