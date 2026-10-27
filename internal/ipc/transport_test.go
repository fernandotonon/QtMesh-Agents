package ipc

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func transportTestPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\qma-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	dir, err := os.MkdirTemp("", "qma-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "test.sock")
}

func TestTransportRoundTrip(t *testing.T) {
	path := transportTestPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			c, err := ln.Accept()
			if err != nil {
				done <- err
				return
			}
			c.SetDeadline(time.Now().Add(3 * time.Second))
			data := make([]byte, 4)
			_, err = io.ReadFull(c, data)
			if err == nil {
				_, err = c.Write(data)
			}
			c.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 2; i++ {
		c, err := Dial(path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			c.Close()
			t.Fatal(err)
		}
		if _, err := c.Write([]byte("ping")); err != nil {
			c.Close()
			t.Fatal(err)
		}
		data := make([]byte, 4)
		_, err = io.ReadFull(c, data)
		c.Close()
		if err != nil || string(data) != "ping" {
			t.Fatalf("reply %q: %v", data, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTransportCloseUnblocksAccept(t *testing.T) {
	ln, err := Listen(transportTestPath(t))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ln.Accept(); done <- err }()
	// Exercise cancellation of an already pending accept as well as close-before-accept.
	time.Sleep(20 * time.Millisecond)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept after Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not unblock Accept")
	}
}

func TestTransportRefusesSecondListener(t *testing.T) {
	path := transportTestPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if other, err := Listen(path); err == nil {
		other.Close()
		t.Fatal("second listener stole live transport")
	}
}
