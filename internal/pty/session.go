package pty

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	creackpty "github.com/creack/pty"
)

// Session wraps a process attached to a PTY.
type Session struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	waitErr error
}

// Start launches command in a PTY with the given size.
func Start(cmd *exec.Cmd, cols, rows uint16) (*Session, error) {
	if cols == 0 {
		cols = 120
	}
	if rows == 0 {
		rows = 40
	}
	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}
	s := &Session{cmd: cmd, ptmx: ptmx, done: make(chan struct{})}
	go func() {
		s.waitErr = cmd.Wait()
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = ptmx.Close()
		close(s.done)
	}()
	return s, nil
}

// PID returns the process ID.
func (s *Session) PID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Read implements io.Reader.
func (s *Session) Read(p []byte) (int, error) {
	return s.ptmx.Read(p)
}

// Write implements io.Writer.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	return s.ptmx.Write(p)
}

// Resize changes the PTY window size.
func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	return creackpty.Setsize(s.ptmx, &creackpty.Winsize{Cols: cols, Rows: rows})
}

// Kill terminates the process.
func (s *Session) Kill() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

// Done is closed when the process exits.
func (s *Session) Done() <-chan struct{} { return s.done }

// WaitErr returns the process wait error after Done.
func (s *Session) WaitErr() error { return s.waitErr }

// Close closes the PTY master.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.ptmx.Close()
}
