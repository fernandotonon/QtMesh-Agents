package web

import (
	"encoding/json"
	"sync"
)

// Hub broadcasts JSON events to web clients.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newHub() *Hub {
	return &Hub{subs: map[chan []byte]struct{}{}}
}

func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *Hub) Broadcast(kind string, payload any) {
	body, err := json.Marshal(map[string]any{"kind": kind, "data": payload})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- body:
		default:
			// drop if slow
		}
	}
}
