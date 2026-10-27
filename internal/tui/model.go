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
	modeRunDetail
	modeTaskDetail
	modeAttentionDetail
)

type focusPane int

const (
	focusInput focusPane = iota
	focusTree
	focusOptions // NEEDS YOU reply list
)

// Model is the Bubble Tea root model.
type Model struct {
	client    *ipc.Client
	width     int
	height    int
	connected bool
	errMsg    string

	projects  []ipc.ProjectInfo
	agents    []ipc.AgentInfo
	sessions  []ipc.SessionInfo
	runs      []ipc.RunInfo
	benches   []ipc.BenchInfo
	attention []ipc.AttentionInfo

	mode         mode
	focus        focusPane
	attachedID   string
	selectedRun  string
	selectedTask string
	selectedAttn string
	cursor       int
	treeOffset   int // first visible tree row (scroll)
	sideInnerH   int // rows available inside sidebar border
	expanded     map[string]bool
	tree         []treeNode

	attnOptions []replyOption
	attnOptCur  int
	attnQuestion string

	orchLog     []string
	orchView    viewport.Model
	agentOutput string
	agentView   viewport.Model
	input       textinput.Model
	ready       bool
}

// New constructs the TUI model.
func New(client *ipc.Client) Model {
	ti := textinput.New()
	ti.Placeholder = "Talk to orchestrator · j/k navigate tree · enter open · Tab focus"
	ti.Focus()
	ti.CharLimit = 512
	ti.Width = 60

	vp := viewport.New(40, 10)
	orchVP := viewport.New(40, 10)

	m := Model{
		client:    client,
		connected: true,
		mode:      modeOrchestrator,
		focus:     focusInput,
		expanded: map[string]bool{
			"root": true,
		},
		input:     ti,
		agentView: vp,
		orchView:  orchVP,
		orchLog: []string{
			"system: Welcome to QtMesh Agents.\n\nThe **orchestrator is a full agent** (Cursor-first): it uses tools to inspect git, worktrees, runs, install, and start work.\nAsk anything — or select **leader:…** to talk to the project lead.\nKeys: Tab · j/k · enter · pgup/pgdn · q quit",
		},
	}
	m.syncOrchView()
	return m
}

type tickMsg time.Time
type dataMsg struct {
	projects  []ipc.ProjectInfo
	agents    []ipc.AgentInfo
	sessions  []ipc.SessionInfo
	runs      []ipc.RunInfo
	benches   []ipc.BenchInfo
	attention []ipc.AttentionInfo
	err       error
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
		runs, err4 := client.RunList()
		benches, _ := client.BenchList()
		attention, _ := client.AttentionList()
		var err error
		for _, e := range []error{err1, err2, err3, err4} {
			if e != nil {
				err = e
				break
			}
		}
		return dataMsg{
			projects: projects, agents: agents, sessions: sessions, runs: runs,
			benches: benches, attention: attention, err: err,
		}
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
		m.rebuildTree()
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
			m.runs = msg.runs
			m.benches = msg.benches
			m.attention = msg.attention
			m.connected = true
			m.errMsg = ""
			m.rebuildTree()
			if m.mode == modeAttentionDetail && m.selectedAttn != "" {
				still := false
				for _, a := range m.attention {
					if a.ID != m.selectedAttn {
						continue
					}
					still = true
					cur := m.attnOptCur
					task := m.selectedTaskInfo()
					m.attnQuestion = attentionQuestion(a, task)
					m.attnOptions = buildAttentionOptions(a, task)
					if cur >= len(m.attnOptions) {
						cur = 0
					}
					m.attnOptCur = cur
					break
				}
				if !still {
					m.mode = modeOrchestrator
					m.selectedAttn = ""
				}
			}
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
		text := strings.TrimRight(string(msg), "\r\n")
		keepTask := false
		switch {
		case strings.HasPrefix(text, "leader: "):
			m.orchLog = appendOrchTurn(m.orchLog, "leader", strings.TrimPrefix(text, "leader: "))
		case strings.HasPrefix(text, "you: "):
			m.orchLog = appendOrchTurn(m.orchLog, "you", strings.TrimPrefix(text, "you: "))
		case strings.HasPrefix(text, "you→leader: "):
			m.orchLog = appendOrchTurn(m.orchLog, "you→leader", strings.TrimPrefix(text, "you→leader: "))
		case strings.HasPrefix(text, "→ "):
			m.orchLog = appendOrchTurn(m.orchLog, "note", text)
			if strings.Contains(text, "→ worker:") {
				keepTask = true
			}
		default:
			// One orch turn = one bubble (keep newlines; do not prefix every line).
			m.orchLog = appendOrchTurn(m.orchLog, "orch", text)
		}
		if !keepTask {
			m.mode = modeOrchestrator
		}
		m.syncOrchView()
		m.layout()
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
			m.mode = modeOrchestrator
		}
		m.syncOrchView()
		return m, nil

	case tea.KeyMsg:
		key := msg.String()
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			if m.mode == modeAttentionDetail {
				switch m.focus {
				case focusOptions:
					m.focus = focusTree
					m.input.Blur()
				case focusTree:
					m.focus = focusOptions
					m.input.Blur()
				case focusInput:
					m.focus = focusOptions
					m.input.Blur()
				}
				m.layout()
				return m, nil
			}
			if m.focus == focusInput {
				m.focus = focusTree
				m.input.Blur()
			} else {
				m.focus = focusInput
				m.input.Focus()
			}
			return m, nil
		case "esc":
			if m.input.Value() != "" {
				m.input.SetValue("")
				return m, nil
			}
			if m.mode == modeAttentionDetail {
				switch m.focus {
				case focusInput:
					m.focus = focusOptions
					m.input.Blur()
					m.layout()
					return m, nil
				case focusOptions:
					m.focus = focusTree
					m.input.Blur()
					m.layout()
					return m, nil
				default:
					m.mode = modeOrchestrator
					m.selectedAttn = ""
					m.focus = focusTree
					m.input.Blur()
					return m, nil
				}
			}
			m.focus = focusTree
			m.input.Blur()
			m.mode = modeOrchestrator
			m.selectedAttn = ""
			return m, nil
		case "left", "h":
			if m.mode == modeAttentionDetail && m.focus == focusOptions {
				m.focus = focusTree
				m.input.Blur()
				m.layout()
				return m, nil
			}
		case "right", "l":
			if m.mode == modeAttentionDetail && m.focus == focusTree {
				m.focus = focusOptions
				m.input.Blur()
				m.layout()
				return m, nil
			}
		case "q":
			if m.focus == focusInput && m.input.Value() != "" {
				break
			}
			if m.input.Value() == "" {
				return m, tea.Quit
			}
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			if m.mode == modeOrchestrator && m.focus == focusInput {
				var cmd tea.Cmd
				m.orchView, cmd = m.orchView.Update(msg)
				return m, cmd
			}
			if m.mode == modeAgent {
				var cmd tea.Cmd
				m.agentView, cmd = m.agentView.Update(msg)
				return m, cmd
			}
		}

		// Option list navigation only while the reply list is focused.
		if m.mode == modeAttentionDetail && m.focus == focusOptions {
			switch key {
			case "j", "down":
				if len(m.attnOptions) > 0 {
					m.attnOptCur = (m.attnOptCur + 1) % len(m.attnOptions)
				}
				return m, nil
			case "k", "up":
				if len(m.attnOptions) > 0 {
					m.attnOptCur = (m.attnOptCur - 1 + len(m.attnOptions)) % len(m.attnOptions)
				}
				return m, nil
			case "enter":
				return m.applyAttentionOption()
			case "t":
				m.focus = focusInput
				m.input.Focus()
				m.input.Placeholder = "custom reply to the worker…"
				m.layout()
				return m, nil
			}
		}

		if m.focus == focusTree {
			return m.handleTreeKey(key)
		}

		if key == "enter" {
			line := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if line == "" {
				return m, nil
			}
			if m.mode == modeAttentionDetail || m.waitingWorkerReply() {
				runID := m.selectedRun
				taskID := m.selectedTask
				attnID := m.selectedAttn
				client := m.client
				return m, func() tea.Msg {
					if taskID != "" && runID != "" {
						if err := client.TaskReply(runID, taskID, line); err != nil {
							return statusLineMsg("worker reply error: " + err.Error())
						}
					}
					if attnID != "" {
						_ = client.AttentionResolve(attnID)
					}
					return statusLineMsg("→ worker: " + line + "\n(task resumed with your reply)")
				}
			}
			// Echo chat turns into the orchestrator pane before the reply arrives.
			if m.attachedID == "" && !isMetaCommand(line) {
				if m.talkingToLeader() {
					m.orchLog = appendOrchTurn(m.orchLog, "you→leader", line)
				} else {
					m.orchLog = appendOrchTurn(m.orchLog, "you", line)
				}
				m.mode = modeOrchestrator
				m.syncOrchView()
			}
			if m.talkingToLeader() && m.attachedID == "" && !isMetaCommand(line) {
				pid := m.selectedFocusProjectID()
				client := m.client
				return m, func() tea.Msg {
					resp, err := client.LeaderConsult(pid, line)
					if err != nil {
						return statusLineMsg("leader error: " + err.Error())
					}
					return statusLineMsg("leader: " + resp.Reply)
				}
			}
			return m, m.handleCommand(line)
		}
	}

	if m.focus == focusInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) rebuildTree() {
	prevID := ""
	if m.cursor >= 0 && m.cursor < len(m.tree) {
		prevID = m.tree[m.cursor].ID
	}
	m.tree = m.buildTree()
	if len(m.tree) == 0 {
		m.cursor = 0
		m.treeOffset = 0
		return
	}
	// Prefer keeping selection on the same node after refresh.
	if prevID != "" {
		for i, n := range m.tree {
			if n.ID == prevID {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.tree) {
		m.cursor = len(m.tree) - 1
	}
	m.snapCursorToSelectable()
	m.ensureTreeVisible()
}

// ensureTreeVisible scrolls so the cursor stays inside the sidebar viewport.
func (m *Model) ensureTreeVisible() {
	vis := m.sideInnerH
	if vis <= 0 {
		vis = 8
	}
	if m.cursor < m.treeOffset {
		m.treeOffset = m.cursor
	}
	if m.cursor >= m.treeOffset+vis {
		m.treeOffset = m.cursor - vis + 1
	}
	if m.treeOffset < 0 {
		m.treeOffset = 0
	}
	maxOff := len(m.tree) - vis
	if maxOff < 0 {
		maxOff = 0
	}
	if m.treeOffset > maxOff {
		m.treeOffset = maxOff
	}
}

func (m *Model) snapCursorToSelectable() {
	if len(m.tree) == 0 {
		return
	}
	if m.tree[m.cursor].Kind != nodeSection {
		return
	}
	for i := m.cursor; i < len(m.tree); i++ {
		if m.tree[i].Kind != nodeSection {
			m.cursor = i
			return
		}
	}
	for i := m.cursor; i >= 0; i-- {
		if m.tree[i].Kind != nodeSection {
			m.cursor = i
			return
		}
	}
}

func (m Model) handleTreeKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		m.moveCursor(1)
		return m, nil
	case "k", "up":
		m.moveCursor(-1)
		return m, nil
	case "right", "l":
		n := m.currentNode()
		if n != nil && n.Expandable && !n.Expanded {
			m.expanded[n.ID] = true
			m.rebuildTree()
		}
		return m, nil
	case "left", "h":
		n := m.currentNode()
		if n != nil && n.Expandable && n.Expanded {
			m.expanded[n.ID] = false
			m.rebuildTree()
			return m, nil
		}
		// Collapse parent / go up a level visually.
		if n != nil && n.Depth > 0 {
			for i := m.cursor - 1; i >= 0; i-- {
				if m.tree[i].Depth < n.Depth && m.tree[i].Expandable {
					m.expanded[m.tree[i].ID] = false
					m.cursor = i
					m.rebuildTree()
					break
				}
			}
		}
		return m, nil
	case "enter", " ":
		return m.activateNode()
	case "o":
		// Jump main pane to orchestrator chat.
		m.mode = modeOrchestrator
		m.focus = focusInput
		m.input.Focus()
		return m, nil
	}
	return m, nil
}

func (m *Model) moveCursor(delta int) {
	if len(m.tree) == 0 {
		return
	}
	for i := 0; i < len(m.tree); i++ {
		m.cursor += delta
		if m.cursor < 0 {
			m.cursor = len(m.tree) - 1
		}
		if m.cursor >= len(m.tree) {
			m.cursor = 0
		}
		if m.tree[m.cursor].Kind != nodeSection {
			m.ensureTreeVisible()
			return
		}
	}
}

func (m *Model) currentNode() *treeNode {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return nil
	}
	return &m.tree[m.cursor]
}

func (m Model) activateNode() (tea.Model, tea.Cmd) {
	n := m.currentNode()
	if n == nil {
		return m, nil
	}
	switch n.Kind {
	case nodeRoot:
		m.mode = modeOrchestrator
		m.focus = focusInput
		m.input.Focus()
		return m, nil
	case nodeProject, nodeBench:
		if n.Expandable {
			m.expanded[n.ID] = !n.Expanded
			m.rebuildTree()
		}
		m.mode = modeOrchestrator
		return m, nil
	case nodeRun:
		if n.Expandable {
			m.expanded[n.ID] = !n.Expanded
			m.rebuildTree()
		}
		m.selectedRun = n.RunID
		m.selectedTask = ""
		m.mode = modeRunDetail
		return m, nil
	case nodeTask:
		m.selectedRun = n.RunID
		m.selectedTask = n.TaskID
		m.mode = modeTaskDetail
		m.focus = focusInput
		m.input.Focus()
		m.layout()
		return m, nil
	case nodeAttention:
		m.openAttentionDetail(n.AttentionID)
		return m, nil
	case nodeSession:
		if n.SessionID == "" {
			return m, nil
		}
		client := m.client
		sid := n.SessionID
		return m, func() tea.Msg {
			if err := client.Attach(sid); err != nil {
				return statusLineMsg("attach error: " + err.Error())
			}
			return attachedMsg{id: sid}
		}
	}
	return m, nil
}

func (m *Model) layout() {
	sideW := 30
	if m.width < 70 {
		sideW = max(18, m.width/2)
	}
	mainW := m.width - sideW - 1
	if mainW < 16 {
		mainW = 16
	}
	footerLines := m.footerLineCount()
	bodyH := m.height - 1 - footerLines
	if bodyH < 4 {
		bodyH = 4
	}
	m.sideInnerH = bodyH - 2
	if m.sideInnerH < 1 {
		m.sideInnerH = 1
	}
	m.agentView.Width = max(10, mainW-2)
	m.agentView.Height = max(1, bodyH-3)
	m.orchView.Width = max(10, mainW-2)
	m.orchView.Height = max(1, bodyH-3)
	if m.waitingWorkerReply() {
		m.input.Width = max(10, m.width-len("WORKER › ")-2)
		m.input.Placeholder = "your reply to the worker…"
	} else if m.talkingToLeader() {
		m.input.Width = max(10, m.width-len("LEADER › ")-2)
		m.input.Placeholder = "ask the project leader…"
	} else {
		m.input.Width = max(10, m.width-4)
		m.input.Placeholder = "Talk to orchestrator · select leader to consult lead · Tab focus"
	}
	m.syncOrchView()
	m.ensureTreeVisible()
}

// waitingWorkerReply is true when the selected task needs a human reply.
func (m Model) waitingWorkerReply() bool {
	if m.mode == modeAttentionDetail {
		return true
	}
	if m.mode != modeTaskDetail || m.selectedTask == "" {
		return false
	}
	t := m.selectedTaskInfo()
	if t == nil {
		return false
	}
	return t.Status == "awaiting_input" || t.Status == "needs_attention"
}

func (m Model) selectedTaskInfo() *ipc.TaskInfo {
	for i := range m.runs {
		if m.runs[i].ID != m.selectedRun {
			continue
		}
		for j := range m.runs[i].Tasks {
			if m.runs[i].Tasks[j].ID == m.selectedTask {
				return &m.runs[i].Tasks[j]
			}
		}
	}
	return nil
}

func (m Model) selectedAttentionInfo() *ipc.AttentionInfo {
	for i := range m.attention {
		if m.attention[i].ID == m.selectedAttn {
			return &m.attention[i]
		}
	}
	return nil
}

func (m *Model) openAttentionDetail(attnID string) {
	m.selectedAttn = attnID
	m.mode = modeAttentionDetail
	m.attnOptCur = 0
	var attn ipc.AttentionInfo
	for _, a := range m.attention {
		if a.ID == attnID {
			attn = a
			break
		}
	}
	m.selectedRun = attn.RunID
	m.selectedTask = attn.TaskID
	task := m.selectedTaskInfo()
	m.attnQuestion = attentionQuestion(attn, task)
	m.attnOptions = buildAttentionOptions(attn, task)
	m.focus = focusOptions
	m.input.Blur()
	m.input.SetValue("")
	m.layout()
}

func (m Model) applyAttentionOption() (tea.Model, tea.Cmd) {
	if len(m.attnOptions) == 0 {
		return m, nil
	}
	if m.attnOptCur < 0 || m.attnOptCur >= len(m.attnOptions) {
		m.attnOptCur = 0
	}
	opt := m.attnOptions[m.attnOptCur]
	switch opt.Action {
	case "custom":
		m.focus = focusInput
		m.input.Focus()
		m.input.Placeholder = "custom reply to the worker…"
		m.layout()
		return m, nil
	case "dismiss":
		aid := m.selectedAttn
		client := m.client
		m.mode = modeOrchestrator
		return m, func() tea.Msg {
			if err := client.AttentionResolve(aid); err != nil {
				return statusLineMsg("attention resolve error: " + err.Error())
			}
			return statusLineMsg("dismissed attention " + aid + " (worker not resumed)")
		}
	default:
		runID := m.selectedRun
		taskID := m.selectedTask
		attnID := m.selectedAttn
		reply := opt.Reply
		if reply == "" {
			reply = "Human reply: " + opt.Label
		}
		req := ipc.TaskReplyRequest{
			RunID: runID, TaskID: taskID, Message: reply,
			ElevateSandbox:       opt.ElevateSandbox,
			InteractiveApprovals: opt.InteractiveApprovals,
		}
		client := m.client
		return m, func() tea.Msg {
			if taskID != "" && runID != "" {
				if err := client.TaskReplyOpts(req); err != nil {
					return statusLineMsg("worker reply error: " + err.Error())
				}
			} else if attnID != "" {
				_ = client.AttentionResolve(attnID)
			}
			if attnID != "" {
				_ = client.AttentionResolve(attnID)
			}
			note := "→ worker: " + opt.Label + "\n(task resumed)"
			if opt.ElevateSandbox {
				note += "\n(elevated sandbox granted)"
			}
			if opt.InteractiveApprovals {
				note += "\n(watch the ChatGPT/Codex app for approval popups)"
			}
			return statusLineMsg(note)
		}
	}
}

// syncOrchView refreshes the orchestrator chat viewport and pins to the latest message.
func (m *Model) syncOrchView() {
	w := m.orchView.Width
	if w < 10 {
		w = 40
	}
	m.orchView.SetContent(renderOrchChat(m.orchLog, w))
	m.orchView.GotoBottom()
}

func (m Model) handleCommand(line string) tea.Cmd {
	client := m.client
	attachedID := m.attachedID
	projectID := m.selectedFocusProjectID()
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
			return statusLineMsg("orchestration started: " + resp.RunID)
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
			if attachedID != "" {
				if err := client.InputFire(attachedID, []byte(line+"\n")); err != nil {
					return statusLineMsg("send error: " + err.Error())
				}
				return statusLineMsg("→ agent: " + line)
			}
			resp, err := client.ChatOpts(ipc.ChatRequest{Message: line, ProjectID: projectID})
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

func (m Model) selectedFocusProjectID() string {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return ""
	}
	return m.tree[m.cursor].ProjectID
}

// talkingToLeader is true when the tree selection is the bench leader row
// (leader:claude) or the "leader <agent>" member under the bench.
func (m Model) talkingToLeader() bool {
	if m.mode == modeAttentionDetail || m.waitingWorkerReply() {
		return false
	}
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return false
	}
	n := m.tree[m.cursor]
	if n.Kind == nodeBench && n.Agent != "" {
		return true
	}
	if n.Kind == nodeSection && strings.HasPrefix(n.Label, "leader ") {
		return true
	}
	return false
}

func isMetaCommand(line string) bool {
	cmd, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch strings.ToLower(cmd) {
	case "spawn", "attach", "stop", "detach", "run", "runs", "cancel", "help":
		return true
	default:
		return false
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
