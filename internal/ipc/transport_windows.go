//go:build windows

package ipc

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

type pipeConn struct {
	*os.File
	addr pipeAddr
}

func (c *pipeConn) LocalAddr() net.Addr  { return c.addr }
func (c *pipeConn) RemoteAddr() net.Addr { return c.addr }

type pipeListener struct {
	mu        sync.Mutex
	acceptMu  sync.Mutex
	path      string
	pending   windows.Handle
	closed    bool
	accepting bool
}

func newPipe(path string, first bool) (windows.Handle, error) {
	if !strings.HasPrefix(path, `\\.\pipe\`) {
		return 0, errors.New("expected a Windows named-pipe path")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(name, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, windows.PIPE_UNLIMITED_INSTANCES, 65536, 65536, 0, nil)
}

// Listen reserves the pipe name immediately, refusing a second daemon.
func Listen(path string) (net.Listener, error) {
	h, err := newPipe(path, true)
	if err != nil {
		return nil, err
	}
	return &pipeListener{path: path, pending: h}, nil
}
func (l *pipeListener) Addr() net.Addr { return pipeAddr(l.path) }
func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return net.ErrClosed
	}
	l.closed = true
	// Accept owns the handle while connecting; cancellation wakes its wait.
	if l.accepting {
		windows.CancelIoEx(l.pending, nil)
		return nil
	}
	return windows.CloseHandle(l.pending)
}
func (l *pipeListener) Accept() (net.Conn, error) {
	l.acceptMu.Lock()
	defer l.acceptMu.Unlock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	defer windows.CloseHandle(event)
	for {
		h := l.pending
		windows.ResetEvent(event)
		ov := windows.Overlapped{HEvent: event}
		l.accepting = true
		err = windows.ConnectNamedPipe(h, &ov)
		l.mu.Unlock()
		if err == windows.ERROR_IO_PENDING {
			var n uint32
			err = windows.GetOverlappedResult(h, &ov, &n, true)
		}
		l.mu.Lock()
		l.accepting = false
		if l.closed {
			windows.CloseHandle(h)
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if err == windows.ERROR_NO_DATA {
			// A liveness probe can disconnect before Accept reaches ConnectNamedPipe.
			// Reuse the reserved instance rather than stopping the daemon accept loop.
			windows.DisconnectNamedPipe(h)
			continue
		}
		if err != nil && err != windows.ERROR_PIPE_CONNECTED {
			l.mu.Unlock()
			return nil, err
		}
		// Keep an instance reserved before handing the connected one to the caller.
		next, err := newPipe(l.path, false)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
		l.pending = next
		l.mu.Unlock()
		return &pipeConn{File: os.NewFile(uintptr(h), l.path), addr: pipeAddr(l.path)}, nil
	}
}

func Dial(path string, timeout time.Duration) (net.Conn, error) {
	if !strings.HasPrefix(path, `\\.\pipe\`) {
		return nil, errors.New("expected a Windows named-pipe path")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			return &pipeConn{File: os.NewFile(uintptr(h), path), addr: pipeAddr(path)}, nil
		}
		if err != windows.ERROR_PIPE_BUSY {
			return nil, err
		}
		if timeout > 0 && !time.Now().Before(deadline) {
			return nil, os.ErrDeadlineExceeded
		}
		time.Sleep(10 * time.Millisecond)
	}
}
