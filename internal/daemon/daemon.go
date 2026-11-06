package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/claude"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/codex"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/cursor"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/mock"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/events"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
	"github.com/fernandotonon/QtMesh-Agents/internal/orchestrator"
	"github.com/fernandotonon/QtMesh-Agents/internal/projects"
	"github.com/fernandotonon/QtMesh-Agents/internal/sessions"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
	"github.com/fernandotonon/QtMesh-Agents/internal/web"
	"github.com/fernandotonon/QtMesh-Agents/pkg/version"
)

// Daemon is the persistent process owner.
type Daemon struct {
	cfg       config.Config
	store     *storage.Store
	registry  *agents.Registry
	sessions  *sessions.Manager
	projects  *projects.Service
	orch      *orchestrator.Engine
	conductor *orchestrator.Conductor
	socket    string
	ln        net.Listener
	web       *web.Server
	log       *slog.Logger
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	lockFile  *os.File // exclusive instance lock (unix flock)
}

// Options configures daemon startup.
type Options struct {
	Config     config.Config
	SocketPath string
	Logger     *slog.Logger
}

// Start creates and runs a daemon (blocking accept loop in goroutine).
func Start(ctx context.Context, opts Options) (*Daemon, error) {
	if err := config.EnsureDirs(); err != nil {
		return nil, err
	}
	cfg := opts.Config
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	socket := opts.SocketPath
	if socket == "" {
		var err error
		socket, err = ipc.DefaultSocketPath()
		if err != nil {
			return nil, err
		}
	}
	if cfg.Daemon.SocketPath != "" {
		socket = cfg.Daemon.SocketPath
	}

	dataDir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	dbPath := cfg.Storage.Path
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "qtmesh-agents.db")
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		return nil, err
	}

	reg := agents.NewRegistry()
	reg.Register(claude.New(cfg.Agents.Claude.Command, cfg.Agents.Claude.Args))
	sandbox := cfg.Agents.Codex.Sandbox
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	// ApproveForMe defaults on (config.Default sets true) so headless workers don't stall.
	reg.Register(codex.NewWithOptions(cfg.Agents.Codex.Command, cfg.Agents.Codex.Args, sandbox, cfg.Agents.Codex.ApproveForMe || sandbox == "workspace-write"))
	reg.Register(cursor.New(cfg.Agents.Cursor.Command, cfg.Agents.Cursor.Args))
	reg.Register(mock.New(cfg.Agents.Mock.Command, cfg.Agents.Mock.Args))

	logDir := filepath.Join(dataDir, "logs")
	mgr := sessions.NewManager(store, reg, logDir)
	if err := mgr.RecoverOnStart(ctx); err != nil {
		opts.Logger.Warn("session recovery", "err", err)
	}

	proj := projects.New(store)

	planner := orchestrator.Planner(orchestrator.HeuristicPlanner{})
	provider := strings.ToLower(strings.TrimSpace(cfg.Orchestrator.Provider))
	if provider != "" && provider != "heuristic" {
		if adapter, ok := reg.Get(provider); ok {
			planner = orchestrator.ProviderPlanner{
				Adapter:  adapter,
				Fallback: orchestrator.HeuristicPlanner{},
			}
		}
	}
	orch := orchestrator.New(orchestrator.Deps{
		Config:   cfg,
		Store:    store,
		Registry: reg,
		Planner:  planner,
		Logger:   opts.Logger,
	})

	var chatAdapter agents.AgentAdapter
	if provider != "" && provider != "heuristic" {
		if a, ok := reg.Get(provider); ok {
			if det, _ := a.Detect(ctx); det {
				chatAdapter = a
			}
		}
	}
	if chatAdapter == nil {
		// Full orch agent: prefer Cursor, then Claude, then Codex.
		for _, id := range []string{"cursor", "claude", "codex"} {
			if a, ok := reg.Get(id); ok {
				if det, _ := a.Detect(ctx); det {
					chatAdapter = a
					opts.Logger.Info("orchestrator agent", "provider", id)
					break
				}
			}
		}
	} else {
		opts.Logger.Info("orchestrator agent", "provider", provider)
	}
	conductor := orchestrator.NewConductor(orch, proj, chatAdapter)
	orch.OnAttentionResolved = func(ctx context.Context, a storage.Attention) {
		if a.GoalID == "" {
			return
		}
		if err := conductor.ResumeBenchGoal(ctx, a.GoalID, ""); err != nil {
			opts.Logger.Warn("resume bench goal after attention", "goal", a.GoalID, "err", err)
		}
	}

	lockFile, err := acquireInstanceLock(socket)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	ln, err := ipc.Listen(socket)
	if err != nil {
		if lockFile != nil {
			_ = lockFile.Close()
		}
		_ = store.Close()
		return nil, fmt.Errorf("listen %s: %w", socket, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	d := &Daemon{
		cfg:       cfg,
		store:     store,
		registry:  reg,
		sessions:  mgr,
		projects:  proj,
		orch:      orch,
		conductor: conductor,
		socket:    socket,
		ln:        ln,
		log:       opts.Logger,
		cancel:    cancel,
		lockFile:  lockFile,
	}

	webSrv, err := web.Start(runCtx, web.Deps{
		Config:    cfg,
		Store:     store,
		Sessions:  mgr,
		Projects:  proj,
		Orch:      orch,
		Conductor: conductor,
		Registry:  reg,
		Logger:    opts.Logger,
		PersistToken: func(token string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			cur, err := config.Load(path)
			if err != nil {
				return err
			}
			cur.Daemon.HTTPAuthToken = token
			cur.Daemon.HTTPEnabled = true
			if cur.Daemon.HTTPListen == "" {
				cur.Daemon.HTTPListen = cfg.Daemon.HTTPListen
			}
			return config.Save(path, cur)
		},
	})
	if err != nil {
		cancel()
		_ = ln.Close()
		if lockFile != nil {
			_ = lockFile.Close()
		}
		_ = store.Close()
		return nil, fmt.Errorf("web workspace: %w", err)
	}
	d.web = webSrv

	_ = d.recordEvent(runCtx, events.New(events.KindDaemonStarted, "daemon started"))

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.acceptLoop(runCtx)
	}()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.sweepLoop(runCtx)
	}()

	d.log.Info("daemon listening", "socket", socket, "pid", os.Getpid())
	return d, nil
}

// sweepLoop resumes orphaned runs after restart and fails truly stalled live tasks.
func (d *Daemon) sweepLoop(ctx context.Context) {
	t := time.NewTicker(45 * time.Second)
	defer t.Stop()
	if n, err := d.orch.ResumeOrphanedRuns(ctx); err != nil {
		d.log.Warn("resume orphans", "err", err)
	} else if n > 0 {
		d.log.Info("resumed orphaned runs", "count", n)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := d.orch.SweepStaleTasks(ctx, 20*time.Minute); err != nil {
				d.log.Warn("sweep", "err", err)
			} else if n > 0 {
				d.log.Info("sweep reaped", "count", n)
			}
		}
	}
}

func (d *Daemon) acceptLoop(ctx context.Context) {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			// Listener closed during shutdown, or fatal accept error.
			return
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.handleConn(ctx, conn)
		}()
	}
}

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	codec := ipc.NewCodec(conn)
	var attached string
	var sub chan []byte

	cleanupAttach := func() {
		if attached != "" && sub != nil {
			if ls, ok := d.sessions.Get(attached); ok {
				ls.Unsubscribe(sub)
			}
			attached = ""
			sub = nil
		}
	}
	defer cleanupAttach()

	// Output pump when attached.
	outDone := make(chan struct{})
	defer close(outDone)

	for {
		env, err := codec.Read()
		if err != nil {
			return
		}
		switch env.Type {
		case ipc.TypePing:
			_ = codec.Write(ipc.Envelope{Type: ipc.TypePong, ID: env.ID})
		case ipc.TypeShutdown:
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
			go d.Stop()
			return
		case ipc.TypeStatus:
			_ = codec.Write(ipc.Envelope{
				Type: ipc.TypeStatusResp,
				ID:   env.ID,
				Payload: ipc.MustPayload(ipc.StatusResponse{
					DaemonRunning: true,
					PID:           os.Getpid(),
					Sessions:      d.sessions.CountRunning(),
					Version:       version.Version,
					SocketPath:    d.socket,
				}),
			})
		case ipc.TypeSpawn:
			req, err := ipc.DecodePayload[ipc.SpawnRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			workDir := req.WorkDir
			projectID := req.ProjectID
			if workDir == "" {
				if p, err := d.projects.Active(ctx); err == nil {
					workDir = p.Path
					if projectID == "" {
						projectID = p.ID
					}
				}
			}
			ls, err := d.sessions.Spawn(ctx, sessions.SpawnOpts{
				AgentID:   req.AgentID,
				ProjectID: projectID,
				WorkDir:   workDir,
				Cols:      req.Cols,
				Rows:      req.Rows,
				ResumeID:  req.ResumeID,
				ExtraArgs: req.ExtraArgs,
				Prompt:    req.Prompt,
			})
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = d.recordEvent(ctx, events.Event{
				Kind: events.KindSessionStarted, Timestamp: time.Now().UTC(),
				SessionID: ls.Info.ID, AgentID: ls.Info.AgentID, ProjectID: projectID,
				Message: "session started",
			})
			_ = codec.Write(ipc.Envelope{
				Type: ipc.TypeSpawnResp, ID: env.ID,
				Payload: ipc.MustPayload(ipc.SpawnResponse{SessionID: ls.Info.ID, PID: ls.Info.PID}),
			})
		case ipc.TypeListSessions:
			live := d.sessions.ListLive()
			infos := make([]ipc.SessionInfo, 0, len(live))
			for _, ls := range live {
				infos = append(infos, ipc.SessionInfo{
					ID: ls.Info.ID, AgentID: ls.Info.AgentID, ProjectID: ls.Info.ProjectID,
					WorkDir: ls.Info.WorkDir, Status: ls.Info.Status, PID: ls.Info.PID,
					CreatedAt: ls.Info.CreatedAt.Format(time.RFC3339),
				})
			}
			// Also include recently stopped from store.
			if recs, err := d.store.ListSessions(ctx); err == nil {
				seen := map[string]bool{}
				for _, i := range infos {
					seen[i.ID] = true
				}
				for _, r := range recs {
					if seen[r.ID] {
						continue
					}
					infos = append(infos, ipc.SessionInfo{
						ID: r.ID, AgentID: r.AgentID, ProjectID: r.ProjectID,
						WorkDir: r.WorkDir, Status: r.Status, PID: r.PID,
						CreatedAt: r.CreatedAt.Format(time.RFC3339),
					})
				}
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeSessions, ID: env.ID, Payload: ipc.MustPayload(infos)})
		case ipc.TypeAttach:
			req, err := ipc.DecodePayload[ipc.SessionRef](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			cleanupAttach()
			ls, ok := d.sessions.Get(req.SessionID)
			if !ok {
				d.writeErr(codec, env.ID, fmt.Errorf("session %q not running", req.SessionID))
				continue
			}
			attached = req.SessionID
			sub = ls.Subscribe()
			// Replay buffer.
			if hist := ls.Buffer.Bytes(); len(hist) > 0 {
				_ = codec.Write(ipc.Envelope{
					Type: ipc.TypeOutput,
					Payload: ipc.MustPayload(ipc.OutputEvent{SessionID: attached, Data: hist}),
				})
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
			go d.forwardOutput(codec, attached, sub, outDone)
		case ipc.TypeDetach:
			cleanupAttach()
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeInput:
			req, err := ipc.DecodePayload[ipc.InputRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.sessions.Write(req.SessionID, req.Data); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeResize:
			req, err := ipc.DecodePayload[ipc.ResizeRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.sessions.Resize(req.SessionID, req.Cols, req.Rows); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeStop:
			req, err := ipc.DecodePayload[ipc.SessionRef](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.sessions.Stop(req.SessionID); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = d.recordEvent(ctx, events.Event{
				Kind: events.KindSessionStopped, Timestamp: time.Now().UTC(),
				SessionID: req.SessionID, Message: "session stopped",
			})
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeListProjects:
			list, err := d.projects.List(ctx)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			infos := make([]ipc.ProjectInfo, 0, len(list))
			for _, p := range list {
				infos = append(infos, ipc.ProjectInfo{ID: p.ID, Name: p.Name, Path: p.Path, Active: p.Active})
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeProjects, ID: env.ID, Payload: ipc.MustPayload(infos)})
		case ipc.TypeAddProject:
			req, err := ipc.DecodePayload[ipc.AddProjectRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			p, err := d.projects.Add(ctx, req.Path, req.Name)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{
				Type: ipc.TypeProjects, ID: env.ID,
				Payload: ipc.MustPayload([]ipc.ProjectInfo{{ID: p.ID, Name: p.Name, Path: p.Path, Active: p.Active}}),
			})
		case ipc.TypeUseProject:
			req, err := ipc.DecodePayload[ipc.UseProjectRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.projects.Use(ctx, req.ID); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeListAgents:
			infos := d.agentInfos(ctx)
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeAgents, ID: env.ID, Payload: ipc.MustPayload(infos)})
		case ipc.TypeRunStart:
			req, err := ipc.DecodePayload[ipc.RunStartRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			workDir := req.WorkDir
			projectID := req.ProjectID
			if workDir == "" || projectID == "" {
				if resolved, err := d.resolveProject(ctx, projectID, req.Objective); err == nil {
					if projectID == "" {
						projectID = resolved.ID
					}
					if workDir == "" {
						workDir = resolved.Path
					}
				} else if p, err := d.projects.Active(ctx); err == nil {
					// Active project is optional fallback only.
					if workDir == "" {
						workDir = p.Path
					}
					if projectID == "" {
						projectID = p.ID
					}
				}
			}
			runID, err := d.orch.StartRun(ctx, orchestrator.PlanRequest{
				Objective:     req.Objective,
				ProjectID:     projectID,
				WorkDir:       workDir,
				AgentOverride: req.AgentOverride,
			})
			if err != nil && runID == "" {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{
				Type: ipc.TypeRunStartResp, ID: env.ID,
				Payload: ipc.MustPayload(ipc.RunStartResponse{RunID: runID}),
			})
		case ipc.TypeChat:
			req, err := ipc.DecodePayload[ipc.ChatRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			res, err := d.conductor.ChatWith(ctx, req.Message, orchestrator.ChatOpts{ProjectID: req.ProjectID})
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			actions := make([]ipc.ChatActionInfo, 0, len(res.Actions))
			for _, a := range res.Actions {
				actions = append(actions, ipc.ChatActionInfo{
					Type: a.Type, ProjectID: a.ProjectID, Objective: a.Objective, RunID: a.RunID,
				})
			}
			_ = codec.Write(ipc.Envelope{
				Type: ipc.TypeChatResp, ID: env.ID,
				Payload: ipc.MustPayload(ipc.ChatResponse{Reply: res.Reply, Actions: actions}),
			})
		case ipc.TypeLeaderConsult:
			req, err := ipc.DecodePayload[ipc.LeaderConsultRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			projs, err := d.projects.List(ctx)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			refs := orchestrator.RefsFromStorage(projs)
			pid := req.ProjectID
			if pid == "" && req.BenchID != "" {
				if b, err := d.store.GetBench(ctx, req.BenchID); err == nil {
					pid = b.ProjectID
				}
			}
			res, err := d.conductor.LeaderTurn(ctx, pid, req.Message, refs)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			actions := make([]ipc.ChatActionInfo, 0, len(res.Actions))
			for _, a := range res.Actions {
				actions = append(actions, ipc.ChatActionInfo{
					Type: a.Type, ProjectID: a.ProjectID, Objective: a.Objective, RunID: a.RunID,
				})
			}
			_ = codec.Write(ipc.Envelope{
				Type: ipc.TypeLeaderConsultResp, ID: env.ID,
				Payload: ipc.MustPayload(ipc.LeaderConsultResponse{Reply: res.Reply, Actions: actions}),
			})
		case ipc.TypeChatHistory:
			hist := d.conductor.History()
			msgs := make([]ipc.ChatHistoryMessage, 0, len(hist))
			for _, m := range hist {
				msgs = append(msgs, ipc.ChatHistoryMessage{
					Role: m.Role, Content: m.Content, Timestamp: m.Timestamp.Format(time.RFC3339),
				})
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeChatHistoryResp, ID: env.ID, Payload: ipc.MustPayload(msgs)})
		case ipc.TypeChatClear:
			d.conductor.ClearHistory()
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeRunStatus:
			req, err := ipc.DecodePayload[ipc.RunRef](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			info, err := d.runInfo(ctx, req.RunID)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeRunStatusResp, ID: env.ID, Payload: ipc.MustPayload(info)})
		case ipc.TypeRunList:
			list, err := d.store.ListRuns(ctx, 50)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			infos := make([]ipc.RunInfo, 0, len(list))
			for _, r := range list {
				tasks, _ := d.store.ListTasksByRun(ctx, r.ID)
				infos = append(infos, runToInfo(r, tasks))
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeRunListResp, ID: env.ID, Payload: ipc.MustPayload(infos)})
		case ipc.TypeRunCancel:
			req, err := ipc.DecodePayload[ipc.RunRef](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.orch.CancelRun(req.RunID); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeRunResume:
			req, err := ipc.DecodePayload[ipc.RunRef](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.orch.ResumeIncompleteRun(ctx, req.RunID); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{
				Type: ipc.TypeRunResumeResp, ID: env.ID,
				Payload: ipc.MustPayload(ipc.RunStartResponse{RunID: req.RunID}),
			})
		case ipc.TypeAttentionList:
			list, err := d.orch.ListOpenAttention(ctx)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			infos := make([]ipc.AttentionInfo, 0, len(list))
			for _, a := range list {
				infos = append(infos, ipc.AttentionInfo{
					ID: a.ID, ProjectID: a.ProjectID, BenchID: a.BenchID, RunID: a.RunID,
					TaskID: a.TaskID, SessionID: a.SessionID, AgentID: a.AgentID, GoalID: a.GoalID,
					Kind: a.Kind, Message: a.Message, Status: a.Status,
					CreatedAt: a.CreatedAt.Format(time.RFC3339),
				})
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeAttentionListResp, ID: env.ID, Payload: ipc.MustPayload(infos)})
		case ipc.TypeAttentionResolve:
			req, err := ipc.DecodePayload[ipc.AttentionRef](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.orch.ResolveAttention(ctx, req.ID); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeTaskReply:
			req, err := ipc.DecodePayload[ipc.TaskReplyRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			if err := d.orch.ReplyToTask(ctx, req.RunID, req.TaskID, req.Message, orchestrator.TaskReplyOpts{
				ElevateSandbox:       req.ElevateSandbox,
				InteractiveApprovals: req.InteractiveApprovals,
			}); err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeOK, ID: env.ID})
		case ipc.TypeBenchList:
			list, err := d.orch.ListBenches(ctx)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			infos := make([]ipc.BenchInfo, 0, len(list))
			for _, b := range list {
				members, _ := d.store.ListBenchMembers(ctx, b.ID)
				ids := make([]string, 0, len(members))
				for _, m := range members {
					ids = append(ids, m.AgentID)
				}
				info := ipc.BenchInfo{
					ID: b.ID, ProjectID: b.ProjectID, Name: b.Name,
					LeaderAgentID: b.LeaderAgentID, Status: b.Status, Members: ids,
				}
				if g, gerr := d.store.LatestBenchGoal(ctx, b.ProjectID); gerr == nil {
					info.GoalID = g.ID
					info.GoalStatus = g.Status
					info.GoalObjective = g.Objective
					info.GoalIteration = g.Iteration
					if msgs, merr := d.store.ListThreadMessages(ctx, g.ThreadID, 4); merr == nil {
						for _, m := range msgs {
							info.ThreadPreview = append(info.ThreadPreview, ipc.ThreadMsgInfo{
								Role: m.Role, AgentID: m.AgentID,
								Content: truncateStr(m.Content, 80),
								CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
							})
						}
					}
				}
				infos = append(infos, info)
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeBenchListResp, ID: env.ID, Payload: ipc.MustPayload(infos)})
		case ipc.TypeGoalStart:
			req, err := ipc.DecodePayload[ipc.GoalStartRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			goalID, err := d.conductor.StartBenchGoal(ctx, req.ProjectID, req.Objective, req.AgentOverride)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeGoalStartResp, ID: env.ID, Payload: ipc.MustPayload(ipc.GoalStartResponse{GoalID: goalID})})
		case ipc.TypeGoalStatus:
			req, err := ipc.DecodePayload[ipc.GoalStatusRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			var g storage.BenchGoal
			if req.GoalID != "" {
				g, err = d.store.GetBenchGoal(ctx, req.GoalID)
			} else if req.ProjectID != "" {
				g, err = d.store.LatestBenchGoal(ctx, req.ProjectID)
			} else {
				d.writeErr(codec, env.ID, fmt.Errorf("goal_id or project_id required"))
				continue
			}
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeGoalStatusResp, ID: env.ID, Payload: ipc.MustPayload(ipc.GoalStatusResponse{
				GoalID: g.ID, ProjectID: g.ProjectID, BenchID: g.BenchID, Objective: g.Objective,
				Status: g.Status, Iteration: g.Iteration, RunID: g.RunID, Error: g.Error, ThreadID: g.ThreadID,
			})})
		case ipc.TypeThreadList:
			req, err := ipc.DecodePayload[ipc.ThreadListRequest](env.Payload)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			threadID := req.ThreadID
			if threadID == "" && req.ProjectID != "" {
				if g, gerr := d.store.LatestBenchGoal(ctx, req.ProjectID); gerr == nil {
					threadID = g.ThreadID
				}
			}
			if threadID == "" {
				d.writeErr(codec, env.ID, fmt.Errorf("thread_id or project_id required"))
				continue
			}
			limit := req.Limit
			if limit <= 0 {
				limit = 30
			}
			msgs, err := d.store.ListThreadMessages(ctx, threadID, limit)
			if err != nil {
				d.writeErr(codec, env.ID, err)
				continue
			}
			out := make([]ipc.ThreadMsgInfo, 0, len(msgs))
			for _, m := range msgs {
				out = append(out, ipc.ThreadMsgInfo{
					Role: m.Role, AgentID: m.AgentID, Content: m.Content,
					CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
				})
			}
			_ = codec.Write(ipc.Envelope{Type: ipc.TypeThreadListResp, ID: env.ID, Payload: ipc.MustPayload(out)})
		default:
			d.writeErr(codec, env.ID, fmt.Errorf("unknown type %q", env.Type))
		}
	}
}

func (d *Daemon) runInfo(ctx context.Context, runID string) (ipc.RunInfo, error) {
	r, err := d.store.GetRun(ctx, runID)
	if err != nil {
		return ipc.RunInfo{}, err
	}
	tasks, err := d.store.ListTasksByRun(ctx, runID)
	if err != nil {
		return ipc.RunInfo{}, err
	}
	return runToInfo(r, tasks), nil
}

// resolveProject picks a project from an explicit id/name or by matching the objective text.
// It does not require an "active" project.
func (d *Daemon) resolveProject(ctx context.Context, idOrName, objective string) (storage.Project, error) {
	list, err := d.projects.List(ctx)
	if err != nil {
		return storage.Project{}, err
	}
	if idOrName != "" {
		for _, p := range list {
			if strings.EqualFold(p.ID, idOrName) || strings.EqualFold(p.Name, idOrName) {
				return p, nil
			}
		}
		return storage.Project{}, fmt.Errorf("project %q not found", idOrName)
	}
	refs := make([]orchestrator.ProjectRef, 0, len(list))
	for _, p := range list {
		refs = append(refs, orchestrator.ProjectRef{ID: p.ID, Name: p.Name, Path: p.Path})
	}
	matched := orchestrator.MatchProjects(objective, refs)
	if len(matched) == 1 {
		for _, p := range list {
			if p.ID == matched[0].ID {
				return p, nil
			}
		}
	}
	if len(list) == 1 {
		return list[0], nil
	}
	if len(matched) > 1 {
		return storage.Project{}, fmt.Errorf("ambiguous project in objective; mention one of: %s", joinProjectNames(matched))
	}
	return storage.Project{}, fmt.Errorf("no project resolved — mention a registered project name in the objective")
}

func joinProjectNames(refs []orchestrator.ProjectRef) string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

func runToInfo(r storage.Run, tasks []storage.TaskRecord) ipc.RunInfo {
	info := ipc.RunInfo{
		ID: r.ID, ProjectID: r.ProjectID, BenchID: r.BenchID, Objective: r.Objective, Status: r.Status,
		Planner: r.Planner, WorkDir: r.WorkDir,
		CreatedAt: r.CreatedAt.Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.Format(time.RFC3339),
		Summary:   r.Summary, Error: r.Error,
	}
	if r.EndedAt != nil {
		info.EndedAt = r.EndedAt.Format(time.RFC3339)
	}
	for _, t := range tasks {
		info.Tasks = append(info.Tasks, ipc.TaskInfo{
			ID: t.ID, Description: t.Description, Category: t.Category, Status: t.Status,
			AssignedAgent: t.AssignedAgent, Dependencies: t.Dependencies, Error: t.Error,
			DiffPath: t.DiffPath, WorktreePath: t.WorktreePath, Branch: t.Branch, RetryCount: t.RetryCount,
			Output: ipc.ExtractTaskOutput(t.ResultJSON),
		})
	}
	return info
}

func (d *Daemon) forwardOutput(codec *ipc.Codec, sessionID string, sub chan []byte, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case data, ok := <-sub:
			if !ok {
				return
			}
			_ = codec.Write(ipc.Envelope{
				Type:    ipc.TypeOutput,
				Payload: ipc.MustPayload(ipc.OutputEvent{SessionID: sessionID, Data: data}),
			})
		}
	}
}

func (d *Daemon) agentInfos(ctx context.Context) []ipc.AgentInfo {
	var out []ipc.AgentInfo
	for _, a := range d.registry.List() {
		entry, _ := d.cfg.AgentByID(a.ID())
		detected, _ := a.Detect(ctx)
		info := ipc.AgentInfo{
			ID: a.ID(), Vendor: a.Vendor(), Command: entry.Command,
			Detected: detected, Enabled: entry.Enabled,
		}
		if caps, err := a.Capabilities(ctx); err == nil {
			info.Version = caps.Version
		}
		out = append(out, info)
	}
	return out
}

func (d *Daemon) writeErr(codec *ipc.Codec, id string, err error) {
	_ = codec.Write(ipc.Envelope{
		Type: ipc.TypeError, ID: id,
		Payload: ipc.MustPayload(ipc.ErrorPayload{Message: err.Error()}),
	})
}

func (d *Daemon) recordEvent(ctx context.Context, ev events.Event) error {
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	data, _ := json.Marshal(ev.Data)
	return d.store.InsertEvent(ctx, ev.ID, string(ev.Kind), ev.Timestamp.Format(time.RFC3339),
		ev.SessionID, ev.ProjectID, ev.AgentID, ev.TaskID, ev.Message, string(data))
}

// Stop shuts down the daemon.
func (d *Daemon) Stop() {
	d.log.Info("daemon stopping")
	_ = d.recordEvent(context.Background(), events.New(events.KindDaemonStopped, "daemon stopped"))
	d.cancel()
	if d.web != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = d.web.Shutdown(ctx)
		cancel()
	}
	_ = d.ln.Close()
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		d.log.Warn("shutdown timed out waiting for connections")
	}
	_ = d.store.Close()
	if d.lockFile != nil {
		_ = d.lockFile.Close()
		d.lockFile = nil
	}
}

// WebURL returns the workspace URL when HTTP is enabled.
func (d *Daemon) WebURL() string {
	if d.web == nil {
		return ""
	}
	return d.web.URL()
}

// WebToken returns the HTTP bearer token when the web server is running.
func (d *Daemon) WebToken() string {
	if d.web == nil {
		return ""
	}
	return d.web.Token()
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// SocketPath returns the listening socket.
func (d *Daemon) SocketPath() string { return d.socket }

// Wait blocks until the daemon stops.
func (d *Daemon) Wait() { d.wg.Wait() }
