package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

type mode int

const (
	modeOrchestrator mode = iota
	modeAgent
)

// Model is the Bubble Tea root model.
type Model struct {
	client    *ipc.Client
	width     int
	height    int
	connected bool
	errMsg    string

	projects []ipc.ProjectInfo
	agents   []ipc.AgentInfo
	sessions []ipc.SessionInfo

	mode         mode
	attachedID   string
	orchLog      []string
	agentOutput  string
	agentView    viewport.Model
	input        textinput.Model
	ready        bool
}

// New constructs the TUI model.
func New(client *ipc.Client) Model {
	ti := textinput.New()
	ti.Placeholder = "Talk to the orchestrator, or spawn/attach an agent..."
	ti.Focus()
	ti.CharLimit = 512
	ti.Width = 60

	vp := viewport.New(40, 10)

	return Model{
		client:      client,
		connected:   true,
		mode:        modeOrchestrator,
		input:       ti,
		agentView:   vp,
		orchLog: []string{
			"Welcome to QtMesh Agents.",
			"Talk to the orchestrator — mention any registered project (no need to select one).",
			"Example: In QtMeshEditor, add hair materials",
			"Meta: spawn|attach|stop|detach|run|runs   Tab: orch/agent view   q: quit",
		},
	}
}

type tickMsg time.Time
type dataMsg struct {
	projects []ipc.ProjectInfo
	agents   []ipc.AgentInfo
	sessions []ipc.SessionInfo
	err      error
}
type outputMsg struct {
	sessionID string
	data      string
}
type statusLineMsg string
type attachedMsg struct {
	id   string
	note string
}
type disconnectMsg struct{ err error }

func (m Model) Init() tea.Cmd {
	return tea.Batch(refreshCmd(m.client), listenOutputs(m.client), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func refreshCmd(client *ipc.Client) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return dataMsg{err: fmt.Errorf("no client")}
		}
		projects, err1 := client.ListProjects()
		agents, err2 := client.ListAgents()
		sessions, err3 := client.ListSessions()
		var err error
		if err1 != nil {
			err = err1
		} else if err2 != nil {
			err = err2
		} else if err3 != nil {
			err = err3
		}
		return dataMsg{projects: projects, agents: agents, sessions: sessions, err: err}
	}
}

func listenOutputs(client *ipc.Client) tea.Cmd {
	return func() tea.Msg {
		env, ok := <-client.Outputs()
		if !ok {
			return disconnectMsg{fmt.Errorf("disconnected from daemon")}
		}
		if env.Type == ipc.TypeOutput {
			out, err := ipc.DecodePayload[ipc.OutputEvent](env.Payload)
			if err != nil {
				return listenOutputs(client)
			}
			return outputMsg{sessionID: out.SessionID, data: string(out.Data)}
		}
		return listenOutputs(client)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case tickMsg:
		return m, tea.Batch(refreshCmd(m.client), tickCmd())

	case dataMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.connected = false
		} else {
			m.projects = msg.projects
			m.agents = msg.agents
			m.sessions = msg.sessions
			m.connected = true
			m.errMsg = ""
		}
		return m, nil

	case outputMsg:
		if m.attachedID == "" || m.attachedID == msg.sessionID {
			m.agentOutput += msg.data
			m.agentView.SetContent(m.agentOutput)
			m.agentView.GotoBottom()
		}
		return m, listenOutputs(m.client)

	case disconnectMsg:
		m.connected = false
		m.errMsg = msg.err.Error()
		return m, nil

	case statusLineMsg:
		m.orchLog = append(m.orchLog, string(msg))
		return m, refreshCmd(m.client)

	case attachedMsg:
		m.attachedID = msg.id
		if msg.id != "" {
			m.mode = modeAgent
			note := msg.note
			if note == "" {
				note = "attached to " + msg.id
			}
			m.orchLog = append(m.orchLog, note)
		} else {
			m.orchLog = append(m.orchLog, "detached")
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			if m.mode == modeOrchestrator {
				m.mode = modeAgent
			} else {
				m.mode = modeOrchestrator
			}
			return m, nil
		case "q":
			if m.input.Value() == "" {
				return m, tea.Quit
			}
		case "enter":
			line := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if line == "" {
				return m, nil
			}
			return m, m.handleCommand(line)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) layout() {
	mainW := m.width - 26
	if mainW < 20 {
		mainW = 20
	}
	mainH := m.height - 6
	if mainH < 5 {
		mainH = 5
	}
	m.agentView.Width = mainW
	m.agentView.Height = mainH
	m.input.Width = max(10, m.width-4)
}

func (m Model) handleCommand(line string) tea.Cmd {
	client := m.client
	attachedID := m.attachedID
	return func() tea.Msg {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			return nil
		}
		switch parts[0] {
		case "spawn":
			if len(parts) < 2 {
				return statusLineMsg("usage: spawn <agent> [prompt...]")
			}
			req := ipc.SpawnRequest{AgentID: parts[1], Cols: 100, Rows: 30}
			if len(parts) > 2 {
				req.Prompt = strings.Join(parts[2:], " ")
			}
			resp, err := client.Spawn(req)
			if err != nil {
				return statusLineMsg("spawn error: " + err.Error())
			}
			if err := client.Attach(resp.SessionID); err != nil {
				return statusLineMsg(fmt.Sprintf("spawned %s but attach failed: %v", resp.SessionID, err))
			}
			msg := "spawned " + resp.SessionID
			if req.Prompt != "" {
				msg += " with prompt"
			}
			return attachedMsg{id: resp.SessionID, note: msg}
		case "attach":
			if len(parts) < 2 {
				return statusLineMsg("usage: attach <session-id>")
			}
			if err := client.Attach(parts[1]); err != nil {
				return statusLineMsg("attach error: " + err.Error())
			}
			return attachedMsg{id: parts[1]}
		case "stop":
			if len(parts) < 2 {
				return statusLineMsg("usage: stop <session-id>")
			}
			if err := client.Stop(parts[1]); err != nil {
				return statusLineMsg("stop error: " + err.Error())
			}
			return statusLineMsg("stopped " + parts[1])
		case "detach":
			_ = client.Detach()
			return attachedMsg{id: ""}
		case "run":
			if len(parts) < 2 {
				return statusLineMsg("usage: run <objective>")
			}
			objective := strings.Join(parts[1:], " ")
			resp, err := client.RunStart(ipc.RunStartRequest{Objective: objective})
			if err != nil {
				return statusLineMsg("run error: " + err.Error())
			}
			return statusLineMsg("orchestration started: " + resp.RunID + " (qma run status " + resp.RunID + ")")
		case "runs":
			list, err := client.RunList()
			if err != nil {
				return statusLineMsg("runs error: " + err.Error())
			}
			if len(list) == 0 {
				return statusLineMsg("no runs")
			}
			msg := "recent runs:"
			for i, r := range list {
				if i >= 5 {
					break
				}
				msg += fmt.Sprintf("\n  %s [%s] %s", r.ID, r.Status, r.Objective)
			}
			return statusLineMsg(msg)
		default:
			// Forward to the attached agent PTY when attached.
			if attachedID != "" {
				if err := client.InputFire(attachedID, []byte(line+"\n")); err != nil {
					return statusLineMsg("send error: " + err.Error())
				}
				return statusLineMsg("→ agent: " + line)
			}
			// Otherwise talk to the cross-project orchestrator.
			resp, err := client.Chat(line)
			if err != nil {
				return statusLineMsg("orchestrator error: " + err.Error())
			}
			msg := resp.Reply
			for _, a := range resp.Actions {
				if a.RunID != "" {
					msg += fmt.Sprintf("\n  ↳ run %s on %s", a.RunID, a.ProjectID)
				}
			}
			return statusLineMsg(msg)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
