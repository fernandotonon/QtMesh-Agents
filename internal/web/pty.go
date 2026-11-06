package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

type ptyClientMsg struct {
	Type string `json:"type"` // input | resize | ping
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

func (s *Server) handlePTY(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id required", 400)
		return
	}
	ls, ok := s.deps.Sessions.Get(sessionID)
	if !ok {
		http.Error(w, "session not running", 404)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // same-origin embed; Tailscale may rewrite Host
	})
	if err != nil {
		s.log.Warn("pty ws accept", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	conn.SetReadLimit(1 << 20)

	// Replay ring buffer so reattach feels continuous.
	if hist := ls.Buffer.Bytes(); len(hist) > 0 {
		_ = conn.Write(ctx, websocket.MessageBinary, hist)
	}

	sub := ls.Subscribe()
	defer ls.Unsubscribe(sub)

	errCh := make(chan error, 2)

	go func() {
		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case data, ok := <-sub:
				if !ok {
					errCh <- io.EOF
					return
				}
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageBinary, data)
				cancel()
				if err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			var msg ptyClientMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				// Treat raw bytes as terminal input (xterm may send plain).
				if werr := s.deps.Sessions.Write(sessionID, data); werr != nil {
					errCh <- werr
					return
				}
				continue
			}
			switch msg.Type {
			case "input":
				if err := s.deps.Sessions.Write(sessionID, []byte(msg.Data)); err != nil {
					errCh <- err
					return
				}
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					_ = s.deps.Sessions.Resize(sessionID, msg.Cols, msg.Rows)
				}
			case "ping":
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong"}`))
			}
		}
	}()

	<-errCh
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Warn("events ws accept", "err", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	// Hello so clients know they're live.
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"kind":"connected","data":{}}`))

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.Ping(ctx); err != nil {
				return
			}
		case msg, ok := <-ch:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
