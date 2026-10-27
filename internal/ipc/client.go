package ipc

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Client talks to the daemon with a background reader demuxing responses and streams.
type Client struct {
	conn     net.Conn
	codec    *Codec
	writeMu  sync.Mutex
	nextID   atomic.Uint64
	pending  map[string]chan Envelope
	pendingMu sync.Mutex
	outputs  chan Envelope
	closed   chan struct{}
	closeOnce sync.Once
	readErr  error
}

// Connect dials the daemon socket and starts the demuxer.
func Connect(socketPath string) (*Client, error) {
	conn, err := Dial(socketPath, 2*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:    conn,
		codec:   NewCodec(conn),
		pending: make(map[string]chan Envelope),
		outputs: make(chan Envelope, 64),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
	return nil
}

func (c *Client) reqID() string {
	return fmt.Sprintf("%d", c.nextID.Add(1))
}

func (c *Client) readLoop() {
	defer close(c.outputs)
	for {
		env, err := c.codec.Read()
		if err != nil {
			c.readErr = err
			c.pendingMu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
			return
		}
		if env.Type == TypeOutput || env.Type == TypeEvent {
			select {
			case c.outputs <- env:
			default:
			}
			continue
		}
		if env.ID != "" {
			c.pendingMu.Lock()
			ch, ok := c.pending[env.ID]
			if ok {
				delete(c.pending, env.ID)
			}
			c.pendingMu.Unlock()
			if ok {
				select {
				case ch <- env:
				default:
				}
				continue
			}
		}
	}
}

func (c *Client) roundTrip(typ string, payload any) (Envelope, error) {
	id := c.reqID()
	ch := make(chan Envelope, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	env := Envelope{Type: typ, ID: id}
	if payload != nil {
		env.Payload = MustPayload(payload)
	}
	c.writeMu.Lock()
	err := c.codec.Write(env)
	c.writeMu.Unlock()
	if err != nil {
		return Envelope{}, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			if c.readErr != nil {
				return Envelope{}, c.readErr
			}
			return Envelope{}, fmt.Errorf("connection closed")
		}
		if resp.Type == TypeError {
			ep, _ := DecodePayload[ErrorPayload](resp.Payload)
			return resp, fmt.Errorf("%s", ep.Message)
		}
		return resp, nil
	case <-time.After(60 * time.Second):
		return Envelope{}, fmt.Errorf("timeout waiting for %s", typ)
	case <-c.closed:
		return Envelope{}, fmt.Errorf("client closed")
	}
}

func (c *Client) Ping() error {
	_, err := c.roundTrip(TypePing, nil)
	return err
}

func (c *Client) Status() (StatusResponse, error) {
	resp, err := c.roundTrip(TypeStatus, nil)
	if err != nil {
		return StatusResponse{}, err
	}
	return DecodePayload[StatusResponse](resp.Payload)
}

func (c *Client) Spawn(req SpawnRequest) (SpawnResponse, error) {
	resp, err := c.roundTrip(TypeSpawn, req)
	if err != nil {
		return SpawnResponse{}, err
	}
	return DecodePayload[SpawnResponse](resp.Payload)
}

func (c *Client) ListSessions() ([]SessionInfo, error) {
	resp, err := c.roundTrip(TypeListSessions, nil)
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]SessionInfo](resp.Payload)
}

func (c *Client) Stop(sessionID string) error {
	_, err := c.roundTrip(TypeStop, SessionRef{SessionID: sessionID})
	return err
}

func (c *Client) Attach(sessionID string) error {
	_, err := c.roundTrip(TypeAttach, SessionRef{SessionID: sessionID})
	return err
}

func (c *Client) Detach() error {
	_, err := c.roundTrip(TypeDetach, nil)
	return err
}

func (c *Client) Input(sessionID string, data []byte) error {
	_, err := c.roundTrip(TypeInput, InputRequest{SessionID: sessionID, Data: data})
	return err
}

// InputFire sends input without waiting for acknowledgment (lower latency for typing).
func (c *Client) InputFire(sessionID string, data []byte) error {
	env := Envelope{
		Type:    TypeInput,
		ID:      c.reqID(),
		Payload: MustPayload(InputRequest{SessionID: sessionID, Data: data}),
	}
	// Register pending so demux doesn't leak, but don't block on it for typing.
	ch := make(chan Envelope, 1)
	c.pendingMu.Lock()
	c.pending[env.ID] = ch
	c.pendingMu.Unlock()
	c.writeMu.Lock()
	err := c.codec.Write(env)
	c.writeMu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, env.ID)
		c.pendingMu.Unlock()
		return err
	}
	go func() {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
		}
		c.pendingMu.Lock()
		delete(c.pending, env.ID)
		c.pendingMu.Unlock()
	}()
	return nil
}

func (c *Client) Resize(sessionID string, cols, rows uint16) error {
	_, err := c.roundTrip(TypeResize, ResizeRequest{SessionID: sessionID, Cols: cols, Rows: rows})
	return err
}

func (c *Client) ListProjects() ([]ProjectInfo, error) {
	resp, err := c.roundTrip(TypeListProjects, nil)
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]ProjectInfo](resp.Payload)
}

func (c *Client) AddProject(path, name string) ([]ProjectInfo, error) {
	resp, err := c.roundTrip(TypeAddProject, AddProjectRequest{Path: path, Name: name})
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]ProjectInfo](resp.Payload)
}

func (c *Client) UseProject(id string) error {
	_, err := c.roundTrip(TypeUseProject, UseProjectRequest{ID: id})
	return err
}

func (c *Client) ListAgents() ([]AgentInfo, error) {
	resp, err := c.roundTrip(TypeListAgents, nil)
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]AgentInfo](resp.Payload)
}

func (c *Client) Shutdown() error {
	_, err := c.roundTrip(TypeShutdown, nil)
	return err
}

// Outputs returns the channel of streaming envelopes (output/event).
func (c *Client) Outputs() <-chan Envelope {
	return c.outputs
}

func (c *Client) RunStart(req RunStartRequest) (RunStartResponse, error) {
	resp, err := c.roundTrip(TypeRunStart, req)
	if err != nil {
		return RunStartResponse{}, err
	}
	return DecodePayload[RunStartResponse](resp.Payload)
}

func (c *Client) RunStatus(runID string) (RunInfo, error) {
	resp, err := c.roundTrip(TypeRunStatus, RunRef{RunID: runID})
	if err != nil {
		return RunInfo{}, err
	}
	return DecodePayload[RunInfo](resp.Payload)
}

func (c *Client) RunList() ([]RunInfo, error) {
	resp, err := c.roundTrip(TypeRunList, nil)
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]RunInfo](resp.Payload)
}

func (c *Client) RunCancel(runID string) error {
	_, err := c.roundTrip(TypeRunCancel, RunRef{RunID: runID})
	return err
}

func (c *Client) Chat(message string) (ChatResponse, error) {
	return c.ChatOpts(ChatRequest{Message: message})
}

func (c *Client) ChatOpts(req ChatRequest) (ChatResponse, error) {
	resp, err := c.roundTrip(TypeChat, req)
	if err != nil {
		return ChatResponse{}, err
	}
	return DecodePayload[ChatResponse](resp.Payload)
}

func (c *Client) LeaderConsult(projectID, message string) (LeaderConsultResponse, error) {
	resp, err := c.roundTrip(TypeLeaderConsult, LeaderConsultRequest{
		Message: message, ProjectID: projectID,
	})
	if err != nil {
		return LeaderConsultResponse{}, err
	}
	return DecodePayload[LeaderConsultResponse](resp.Payload)
}

func (c *Client) ChatHistory() ([]ChatHistoryMessage, error) {
	resp, err := c.roundTrip(TypeChatHistory, nil)
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]ChatHistoryMessage](resp.Payload)
}

func (c *Client) ChatClear() error {
	_, err := c.roundTrip(TypeChatClear, nil)
	return err
}

func (c *Client) AttentionList() ([]AttentionInfo, error) {
	resp, err := c.roundTrip(TypeAttentionList, nil)
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]AttentionInfo](resp.Payload)
}

func (c *Client) AttentionResolve(id string) error {
	_, err := c.roundTrip(TypeAttentionResolve, AttentionRef{ID: id})
	return err
}

func (c *Client) TaskReply(runID, taskID, message string) error {
	return c.TaskReplyOpts(TaskReplyRequest{RunID: runID, TaskID: taskID, Message: message})
}

func (c *Client) TaskReplyOpts(req TaskReplyRequest) error {
	_, err := c.roundTrip(TypeTaskReply, req)
	return err
}

func (c *Client) BenchList() ([]BenchInfo, error) {
	resp, err := c.roundTrip(TypeBenchList, nil)
	if err != nil {
		return nil, err
	}
	return DecodePayload[[]BenchInfo](resp.Payload)
}

// ReadEnvelope is deprecated; use Outputs().
func (c *Client) ReadEnvelope() (Envelope, error) {
	select {
	case env, ok := <-c.outputs:
		if !ok {
			if c.readErr != nil {
				return Envelope{}, c.readErr
			}
			return Envelope{}, fmt.Errorf("connection closed")
		}
		return env, nil
	case <-c.closed:
		return Envelope{}, fmt.Errorf("client closed")
	}
}
