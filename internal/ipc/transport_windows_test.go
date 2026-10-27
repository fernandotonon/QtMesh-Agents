//go:build windows

package ipc

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestPipeDialTimeoutAndDisconnectedProbe(t *testing.T) {
	path := transportTestPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	probe, err := Dial(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(path, 30*time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("busy pipe timeout: %v", err)
	}
	probe.Close()
	accepted := make(chan net.Conn, 1)
	failed := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			failed <- err
		} else {
			accepted <- c
		}
	}()
	client, err := Dial(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case c := <-accepted:
		defer c.Close()
		if err := c.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read deadline: %v", err)
		}
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("Accept did not recover from disconnected probe")
	}
}
