package events

import (
	"encoding/json"
	"time"
)

// Kind identifies an event type.
type Kind string

const (
	KindSessionStarted   Kind = "session.started"
	KindSessionAttached  Kind = "session.attached"
	KindSessionDetached  Kind = "session.detached"
	KindSessionStopped   Kind = "session.stopped"
	KindSessionExited    Kind = "session.exited"
	KindSessionInterrupted Kind = "session.interrupted"
	KindTaskQueued         Kind = "task.queued"
	KindTaskStarted        Kind = "task.started"
	KindTaskCompleted      Kind = "task.completed"
	KindTaskFailed         Kind = "task.failed"
	KindAttentionRaised    Kind = "attention.raised"
	KindAttentionResolved  Kind = "attention.resolved"
	KindBenchOpened        Kind = "bench.opened"
	KindDaemonStarted      Kind = "daemon.started"
	KindDaemonStopped      Kind = "daemon.stopped"
	KindError              Kind = "error"
)

// Event is a structured observability record.
type Event struct {
	ID        string         `json:"id"`
	Kind      Kind           `json:"kind"`
	Timestamp time.Time      `json:"timestamp"`
	SessionID string         `json:"session_id,omitempty"`
	ProjectID string         `json:"project_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	Message   string         `json:"message,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// New creates an event with a timestamp.
func New(kind Kind, message string) Event {
	return Event{
		Kind:      kind,
		Timestamp: time.Now().UTC(),
		Message:   message,
		Data:      map[string]any{},
	}
}

// JSON serializes the event.
func (e Event) JSON() ([]byte, error) {
	return json.Marshal(e)
}
