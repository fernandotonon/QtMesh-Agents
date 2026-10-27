package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

type nodeKind int

const (
	nodeRoot nodeKind = iota
	nodeProject
	nodeBench
	nodeRun
	nodeTask
	nodeAttention
	nodeSession
	nodeSection
)

// treeNode is one visible row in the sidebar tree.
type treeNode struct {
	Kind       nodeKind
	ID         string
	Label      string
	Status     string
	Agent      string
	Depth      int
	Expandable bool
	Expanded   bool
	RunID      string
	SessionID  string
	TaskID     string
	AttentionID string
	ProjectID  string
	BenchID    string
}

func statusGlyph(status string) string {
	switch strings.ToLower(status) {
	case "running", "planning", "active":
		return "●"
	case "succeeded":
		return "✓"
	case "failed", "cancelled", "blocked":
		return "✗"
	case "pending", "ready", "idle":
		return "○"
	case "awaiting_input", "needs_attention", "open":
		return "⚠"
	default:
		if status == "" {
			return "◆"
		}
		return "·"
	}
}

func shortTaskID(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && i+1 < len(id) {
		tail := id[i+1:]
		if len(tail) <= 16 {
			return tail
		}
	}
	return truncate(id, 16)
}

func shortRunID(id string) string {
	if strings.HasPrefix(id, "run-") && len(id) > 12 {
		return "run-…" + id[len(id)-6:]
	}
	return truncate(id, 14)
}

func countRunningTasks(r ipc.RunInfo) int {
	n := 0
	for _, t := range r.Tasks {
		if t.Status == "running" {
			n++
		}
	}
	return n
}

func isLiveRun(r ipc.RunInfo) bool {
	switch r.Status {
	case "running", "planning", "needs_attention", "pending":
		return true
	default:
		return false
	}
}

func countAwaiting(r ipc.RunInfo) int {
	n := 0
	for _, t := range r.Tasks {
		if t.Status == "awaiting_input" {
			n++
		}
	}
	if r.Status == "needs_attention" {
		n++
	}
	return n
}

// buildTree: Orchestrator → [ATTENTION] → Projects → Bench → Leader/Runs/Tasks
func (m Model) buildTree() []treeNode {
	var nodes []treeNode

	working, needing := 0, len(m.attention)
	for _, r := range m.runs {
		working += countRunningTasks(r)
		needing += countAwaiting(r)
	}
	rootLabel := "Orchestrator"
	switch {
	case needing > 0 && working > 0:
		rootLabel = fmt.Sprintf("Orchestrator (%d● %d⚠)", working, needing)
	case needing > 0:
		rootLabel = fmt.Sprintf("Orchestrator (%d need you)", needing)
	case working > 0:
		rootLabel = fmt.Sprintf("Orchestrator (%d working)", working)
	}

	rootExpanded := m.expanded["root"]
	if _, ok := m.expanded["root"]; !ok {
		rootExpanded = true
	}
	nodes = append(nodes, treeNode{
		Kind: nodeRoot, ID: "root", Label: rootLabel,
		Status: "online", Depth: 0, Expandable: true, Expanded: rootExpanded,
	})
	if !rootExpanded {
		return nodes
	}

	// Attention section first when anything is open.
	if len(m.attention) > 0 {
		nodes = append(nodes, treeNode{
			Kind: nodeSection, ID: "sec-attn", Label: "NEEDS YOU", Depth: 1,
		})
		for _, a := range m.attention {
			if len(nodes) > 40 {
				break
			}
			nodes = append(nodes, treeNode{
				Kind: nodeAttention, ID: "attn:" + a.ID,
				Label: truncate(a.Kind+": "+a.Message, 22),
				Status: "needs_attention", Depth: 2,
				AttentionID: a.ID, RunID: a.RunID, TaskID: a.TaskID, ProjectID: a.ProjectID,
			})
		}
	}

	benchByProject := map[string]ipc.BenchInfo{}
	for _, b := range m.benches {
		benchByProject[b.ProjectID] = b
	}

	nodes = append(nodes, treeNode{
		Kind: nodeSection, ID: "sec-projects", Label: "PROJECT BENCHES", Depth: 1,
	})
	if len(m.projects) == 0 {
		nodes = append(nodes, treeNode{
			Kind: nodeSection, ID: "sec-proj-empty", Label: "(none registered)", Depth: 2,
		})
	}

	for _, p := range m.projects {
		pkey := "proj:" + p.ID
		pexp := m.expanded[pkey]
		if _, set := m.expanded[pkey]; !set {
			pexp = true // show benches by default
		}
		st := ""
		if p.Active {
			st = "active"
		}
		bench, hasBench := benchByProject[p.ID]
		if hasBench && bench.Status == "needs_attention" {
			st = "needs_attention"
		}
		nodes = append(nodes, treeNode{
			Kind: nodeProject, ID: pkey, Label: truncate(p.Name, 20), Status: st,
			Depth: 2, Expandable: true, Expanded: pexp, ProjectID: p.ID,
		})
		if !pexp {
			continue
		}
		if hasBench {
			bkey := "bench:" + bench.ID
			bexp := m.expanded[bkey]
			if _, set := m.expanded[bkey]; !set {
				bexp = bench.Status == "active" || bench.Status == "needs_attention"
			}
			label := "bench"
			if bench.LeaderAgentID != "" {
				label = "leader:" + bench.LeaderAgentID
			}
			nodes = append(nodes, treeNode{
				Kind: nodeBench, ID: bkey, Label: label, Status: bench.Status,
				Depth: 3, Expandable: true, Expanded: bexp,
				ProjectID: p.ID, BenchID: bench.ID, Agent: bench.LeaderAgentID,
			})
			if bexp {
				for _, mem := range bench.Members {
					role := "worker"
					if mem == bench.LeaderAgentID {
						role = "leader"
					}
					nodes = append(nodes, treeNode{
						Kind: nodeSection, ID: bkey + ":m:" + mem,
						Label: role + " " + mem, Depth: 4, Status: "",
						ProjectID: p.ID, BenchID: bench.ID, Agent: mem,
					})
				}
			}
		} else {
			nodes = append(nodes, treeNode{
				Kind: nodeSection, ID: pkey + ":nobench", Label: "(bench opens on first run)", Depth: 3,
			})
		}

		// Active / attention runs for this project (skip cancelled clutter).
		var projRuns []ipc.RunInfo
		for _, r := range m.runs {
			if r.ProjectID != p.ID && !(r.ProjectID == "" && len(m.projects) == 1) {
				continue
			}
			if !isLiveRun(r) {
				continue
			}
			projRuns = append(projRuns, r)
		}
		sort.SliceStable(projRuns, func(i, j int) bool {
			return countRunningTasks(projRuns[i])+countAwaiting(projRuns[i])*10 >
				countRunningTasks(projRuns[j])+countAwaiting(projRuns[j])*10
		})
		shown := 0
		for _, r := range projRuns {
			if shown >= 3 {
				break
			}
			if r.Status != "running" && r.Status != "planning" && r.Status != "needs_attention" {
				continue
			}
			shown++
			key := "run:" + r.ID
			exp := m.expanded[key]
			if _, set := m.expanded[key]; !set {
				exp = countRunningTasks(r) > 0 || countAwaiting(r) > 0 || shown == 1
			}
			label := shortRunID(r.ID)
			if n := countRunningTasks(r); n > 0 {
				label = fmt.Sprintf("%s (%d●)", label, n)
			}
			if n := countAwaiting(r); n > 0 {
				label = fmt.Sprintf("%s ⚠", label)
			}
			nodes = append(nodes, treeNode{
				Kind: nodeRun, ID: key, Label: label, Status: r.Status,
				Depth: 3, Expandable: true, Expanded: exp, RunID: r.ID, ProjectID: p.ID,
			})
			if !exp {
				continue
			}
			for _, t := range r.Tasks {
				label := shortTaskID(t.ID)
				if t.AssignedAgent != "" {
					label = fmt.Sprintf("%s [%s]", label, t.AssignedAgent)
				}
				if t.Category == "leadership" {
					label = "♔ " + label
				}
				nodes = append(nodes, treeNode{
					Kind: nodeTask, ID: "task:" + t.ID, Label: label,
					Status: t.Status, Agent: t.AssignedAgent, Depth: 4,
					RunID: r.ID, TaskID: t.ID,
				})
			}
		}
	}

	// Runs not tied to a listed project (or tests without projects registered).
	var orphans []ipc.RunInfo
	known := map[string]bool{}
	for _, p := range m.projects {
		known[p.ID] = true
	}
	for _, r := range m.runs {
		if !isLiveRun(r) {
			continue
		}
		if r.ProjectID == "" || !known[r.ProjectID] {
			orphans = append(orphans, r)
		}
	}
	if len(orphans) > 0 {
		nodes = append(nodes, treeNode{
			Kind: nodeSection, ID: "sec-runs", Label: "RUNS", Depth: 1,
		})
		shown := 0
		for _, r := range orphans {
			if shown >= 5 {
				break
			}
			shown++
			key := "run:" + r.ID
			exp := m.expanded[key]
			if _, set := m.expanded[key]; !set {
				// Auto-expand only the hottest orphan run to keep the sidebar compact.
				exp = shown == 1 && (countRunningTasks(r) > 0 || countAwaiting(r) > 0)
			}
			label := shortRunID(r.ID)
			if n := countRunningTasks(r); n > 0 {
				label = fmt.Sprintf("%s (%d●)", label, n)
			}
			nodes = append(nodes, treeNode{
				Kind: nodeRun, ID: key, Label: label, Status: r.Status,
				Depth: 2, Expandable: true, Expanded: exp, RunID: r.ID,
			})
			if !exp {
				continue
			}
			for _, t := range r.Tasks {
				label := shortTaskID(t.ID)
				if t.AssignedAgent != "" {
					label = fmt.Sprintf("%s [%s]", label, t.AssignedAgent)
				}
				nodes = append(nodes, treeNode{
					Kind: nodeTask, ID: "task:" + t.ID, Label: label,
					Status: t.Status, Agent: t.AssignedAgent, Depth: 3,
					RunID: r.ID, TaskID: t.ID,
				})
			}
		}
	}

	// Interactive PTY sessions (spawn/attach). Bench workers do not use these.
	nodes = append(nodes, treeNode{
		Kind: nodeSection, ID: "sec-sessions", Label: "SESSIONS (pty)", Depth: 1,
	})
	live := 0
	for _, s := range m.sessions {
		if s.Status != "running" {
			continue
		}
		if live >= 4 {
			break
		}
		nodes = append(nodes, treeNode{
			Kind: nodeSession, ID: "session:" + s.ID,
			Label: s.AgentID + " " + truncate(s.ID, 10),
			Status: s.Status, Agent: s.AgentID, Depth: 2, SessionID: s.ID,
		})
		live++
	}
	if live == 0 {
		nodes = append(nodes, treeNode{
			Kind: nodeSection, ID: "sec-sess-empty", Label: "(none — use spawn)", Depth: 2,
		})
	}
	return nodes
}

func (n treeNode) render(selected bool, width int) string {
	indent := strings.Repeat("  ", n.Depth)
	prefix := ""
	switch {
	case n.Kind == nodeSection:
		return dimStyle.Render(indent + n.Label)
	case n.Expandable && n.Expanded:
		prefix = "▾ "
	case n.Expandable:
		prefix = "▸ "
	default:
		prefix = "  "
	}
	glyph := statusGlyph(n.Status)
	line := fmt.Sprintf("%s%s%s %s", indent, prefix, glyph, n.Label)
	line = truncate(line, width-1)
	if selected {
		return okStyle.Render("›" + line)
	}
	if n.Kind == nodeAttention || n.Status == "needs_attention" || n.Status == "awaiting_input" {
		return warnStyle.Render(" " + line)
	}
	return " " + line
}
