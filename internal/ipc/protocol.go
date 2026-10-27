package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Message types exchanged between clients and the daemon.
const (
	TypePing          = "ping"
	TypePong          = "pong"
	TypeError         = "error"
	TypeStatus        = "status"
	TypeStatusResp    = "status_resp"
	TypeSpawn         = "spawn"
	TypeSpawnResp     = "spawn_resp"
	TypeAttach        = "attach"
	TypeDetach        = "detach"
	TypeInput         = "input"
	TypeResize        = "resize"
	TypeStop          = "stop"
	TypeListSessions  = "list_sessions"
	TypeSessions      = "sessions"
	TypeListProjects  = "list_projects"
	TypeProjects      = "projects"
	TypeAddProject    = "add_project"
	TypeUseProject    = "use_project"
	TypeOutput        = "output"
	TypeEvent         = "event"
	TypeOK            = "ok"
	TypeShutdown      = "shutdown"
	TypeListAgents    = "list_agents"
	TypeAgents        = "agents"
	TypeRunStart      = "run_start"
	TypeRunStartResp  = "run_start_resp"
	TypeRunStatus     = "run_status"
	TypeRunStatusResp = "run_status_resp"
	TypeRunList       = "run_list"
	TypeRunListResp   = "run_list_resp"
	TypeRunCancel     = "run_cancel"
	TypeChat            = "chat"
	TypeChatResp        = "chat_resp"
	TypeChatHistory     = "chat_history"
	TypeChatHistoryResp = "chat_history_resp"
	TypeChatClear       = "chat_clear"
	TypeLeaderConsult   = "leader_consult"
	TypeLeaderConsultResp = "leader_consult_resp"
	TypeAttentionList   = "attention_list"
	TypeAttentionListResp = "attention_list_resp"
	TypeAttentionResolve = "attention_resolve"
	TypeBenchList       = "bench_list"
	TypeBenchListResp   = "bench_list_resp"
	TypeTaskReply       = "task_reply"
)

// Envelope is a length-prefixed JSON message.
type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Codec reads/writes framed envelopes on a stream.
type Codec struct {
	rw io.ReadWriter
	mu sync.Mutex
}

func NewCodec(rw io.ReadWriter) *Codec {
	return &Codec{rw: rw}
}

func (c *Codec) Write(env Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := c.rw.Write(hdr[:]); err != nil {
		return err
	}
	_, err = c.rw.Write(data)
	return err
}

func (c *Codec) Read() (Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		return Envelope{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > 16<<20 {
		return Envelope{}, fmt.Errorf("invalid frame size %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.rw, buf); err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// Helpers for payload encode/decode.
func MustPayload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func DecodePayload[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

// Request/response payload types.

type SpawnRequest struct {
	AgentID   string   `json:"agent_id"`
	ProjectID string   `json:"project_id,omitempty"`
	WorkDir   string   `json:"work_dir,omitempty"`
	Cols      uint16   `json:"cols,omitempty"`
	Rows      uint16   `json:"rows,omitempty"`
	ResumeID  string   `json:"resume_id,omitempty"`
	ExtraArgs []string `json:"extra_args,omitempty"`
	Prompt    string   `json:"prompt,omitempty"`
}

type SpawnResponse struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
}

type SessionRef struct {
	SessionID string `json:"session_id"`
}

type InputRequest struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

type ResizeRequest struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type OutputEvent struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

type SessionInfo struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	ProjectID string `json:"project_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	CreatedAt string `json:"created_at"`
}

type ProjectInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Active bool   `json:"active"`
}

type AddProjectRequest struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type UseProjectRequest struct {
	ID string `json:"id"`
}

type StatusResponse struct {
	DaemonRunning bool   `json:"daemon_running"`
	PID           int    `json:"pid"`
	Sessions      int    `json:"sessions"`
	Version       string `json:"version"`
	SocketPath    string `json:"socket_path"`
}

type AgentInfo struct {
	ID       string `json:"id"`
	Vendor   string `json:"vendor"`
	Command  string `json:"command"`
	Detected bool   `json:"detected"`
	Version  string `json:"version,omitempty"`
	Enabled  bool   `json:"enabled"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}

type RunStartRequest struct {
	Objective     string `json:"objective"`
	ProjectID     string `json:"project_id,omitempty"`
	WorkDir       string `json:"work_dir,omitempty"`
	AgentOverride string `json:"agent_override,omitempty"`
}

type RunStartResponse struct {
	RunID string `json:"run_id"`
}

type RunRef struct {
	RunID string `json:"run_id"`
}

// TaskReplyRequest sends a human reply to a worker task waiting on input.
type TaskReplyRequest struct {
	RunID   string `json:"run_id"`
	TaskID  string `json:"task_id"`
	Message string `json:"message"`
	// ElevateSandbox retries with Codex danger-full-access (grants broader FS access).
	ElevateSandbox bool `json:"elevate_sandbox,omitempty"`
	// InteractiveApprovals retries so Codex prompts in the ChatGPT/Codex desktop app.
	InteractiveApprovals bool `json:"interactive_approvals,omitempty"`
}

type TaskInfo struct {
	ID            string   `json:"id"`
	Description   string   `json:"description"`
	Category      string   `json:"category,omitempty"`
	Status        string   `json:"status"`
	AssignedAgent string   `json:"assigned_agent,omitempty"`
	Dependencies  []string `json:"dependencies,omitempty"`
	Error         string   `json:"error,omitempty"`
	DiffPath      string   `json:"diff_path,omitempty"`
	WorktreePath  string   `json:"worktree_path,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	RetryCount    int      `json:"retry_count,omitempty"`
	// Output is the agent's latest feedback (from result_json), for TUI display.
	Output string `json:"output,omitempty"`
}

// ExtractTaskOutput pulls human-readable agent feedback from a task result_json blob.
func ExtractTaskOutput(resultJSON string) string {
	resultJSON = strings.TrimSpace(resultJSON)
	if resultJSON == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &m); err != nil {
		return resultJSON
	}
	for _, key := range []string{"output", "brief", "summary", "reply"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

type RunInfo struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id,omitempty"`
	BenchID   string     `json:"bench_id,omitempty"`
	Objective string     `json:"objective"`
	Status    string     `json:"status"`
	Planner   string     `json:"planner,omitempty"`
	WorkDir   string     `json:"work_dir,omitempty"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	EndedAt   string     `json:"ended_at,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Error     string     `json:"error,omitempty"`
	Tasks     []TaskInfo `json:"tasks,omitempty"`
}

type AttentionInfo struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	BenchID   string `json:"bench_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at,omitempty"`
}

type AttentionRef struct {
	ID string `json:"id"`
}

type BenchInfo struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	LeaderAgentID string   `json:"leader_agent_id,omitempty"`
	Status        string   `json:"status"`
	Members       []string `json:"members,omitempty"`
}

type ChatRequest struct {
	Message   string `json:"message"`
	ProjectID string `json:"project_id,omitempty"` // TUI tree focus
}

// LeaderConsultRequest asks the project bench leader a one-shot question.
type LeaderConsultRequest struct {
	Message   string `json:"message"`
	ProjectID string `json:"project_id,omitempty"`
	BenchID   string `json:"bench_id,omitempty"`
}

type LeaderConsultResponse struct {
	Reply string `json:"reply"`
}

type ChatActionInfo struct {
	Type      string `json:"type"`
	ProjectID string `json:"project_id,omitempty"`
	Objective string `json:"objective,omitempty"`
	RunID     string `json:"run_id,omitempty"`
}

type ChatResponse struct {
	Reply   string           `json:"reply"`
	Actions []ChatActionInfo `json:"actions,omitempty"`
}

type ChatHistoryMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}
