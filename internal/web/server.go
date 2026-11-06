package web

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/orchestrator"
	"github.com/fernandotonon/QtMesh-Agents/internal/projects"
	"github.com/fernandotonon/QtMesh-Agents/internal/sessions"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

// Deps wires the web workspace into the daemon.
type Deps struct {
	Config    config.Config
	Store     *storage.Store
	Sessions  *sessions.Manager
	Projects  *projects.Service
	Orch      *orchestrator.Engine
	Conductor *orchestrator.Conductor
	Registry  *agents.Registry
	Logger    *slog.Logger
	// PersistToken writes an auto-generated token back to config on disk.
	PersistToken func(token string) error
}

// Server is the HTTP/WS workspace gateway.
type Server struct {
	deps   Deps
	auth   *authGuard
	hub    *Hub
	http   *http.Server
	ln     net.Listener
	log    *slog.Logger
	token  string
	listen string
}

// EnsureToken returns the auth token, generating+persisting if needed.
func EnsureToken(cfg *config.Config, persist func(string) error) (string, bool, error) {
	tok := strings.TrimSpace(cfg.Daemon.HTTPAuthToken)
	generated := false
	if tok == "" {
		var err error
		tok, err = generateToken()
		if err != nil {
			return "", false, err
		}
		cfg.Daemon.HTTPAuthToken = tok
		generated = true
		if persist != nil {
			_ = persist(tok)
		}
	}
	return tok, generated, nil
}

// Start begins serving the web workspace. Returns the public base URL hint.
func Start(ctx context.Context, deps Deps) (*Server, error) {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	if !deps.Config.Daemon.HTTPEnabled {
		return nil, nil
	}
	listen := strings.TrimSpace(deps.Config.Daemon.HTTPListen)
	if listen == "" {
		listen = "127.0.0.1:7432"
	}
	tok, generated, err := EnsureToken(&deps.Config, deps.PersistToken)
	if err != nil {
		return nil, err
	}
	if generated {
		log.Warn("web auth token auto-generated — save this; required for browser access", "token", tok)
	}
	if strings.HasPrefix(listen, "0.0.0.0:") || strings.HasPrefix(listen, "[::]:") || strings.HasPrefix(listen, ":") {
		if strings.TrimSpace(deps.Config.Daemon.HTTPAuthToken) == "" && !generated {
			log.Warn("http listening on all interfaces without auth token configured")
		} else {
			log.Info("http listening on all interfaces — use Tailscale or a reverse proxy for TLS", "listen", listen)
		}
	}

	s := &Server{
		deps:   deps,
		auth:   newAuthGuard(tok),
		hub:    newHub(),
		log:    log,
		token:  tok,
		listen: listen,
	}
	mux := http.NewServeMux()
	s.routes(mux)
	handler := s.auth.middleware(mux)

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("web listen %s: %w", listen, err)
	}
	s.ln = ln
	s.http = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		err := s.http.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			log.Error("web server stopped", "err", err)
		}
	}()
	log.Info("web workspace listening", "listen", listen, "url", s.URL())
	return s, nil
}

func (s *Server) URL() string {
	host := s.listen
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	} else if strings.HasPrefix(host, "[::]:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "[::]:")
	} else if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	return "http://" + host + "/"
}

func (s *Server) Token() string { return s.token }

func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("GET /api/benches", s.handleListBenches)
	mux.HandleFunc("POST /api/benches", s.handleEnsureBench)
	mux.HandleFunc("POST /api/benches/{id}/leader", s.handleSetBenchLeader)
	mux.HandleFunc("POST /api/benches/{id}/workers", s.handleAddBenchWorker)
	mux.HandleFunc("DELETE /api/benches/{id}/workers/{agent}", s.handleRemoveBenchWorker)
	mux.HandleFunc("GET /api/runs", s.handleListRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.handleGetRun)
	mux.HandleFunc("GET /api/goals", s.handleListGoals)
	mux.HandleFunc("GET /api/goals/{id}", s.handleGetGoal)
	mux.HandleFunc("POST /api/goals", s.handleStartGoal)
	mux.HandleFunc("POST /api/goals/{id}/resume", s.handleResumeGoal)
	mux.HandleFunc("GET /api/threads", s.handleThreadMessages)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("GET /api/attention", s.handleListAttention)
	mux.HandleFunc("POST /api/attention/{id}/resolve", s.handleResolveAttention)
	mux.HandleFunc("POST /api/tasks/reply", s.handleTaskReply)
	mux.HandleFunc("GET /api/orchestrator", s.handleGetOrchestrator)
	mux.HandleFunc("POST /api/orchestrator", s.handleSetOrchestrator)

	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleSpawnSession)
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.handleStopSession)

	mux.HandleFunc("GET /api/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/profiles", s.handleListProfiles)
	mux.HandleFunc("POST /api/profiles", s.handleUpsertProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.handleDeleteProfile)
	mux.HandleFunc("POST /api/profiles/{id}/spawn", s.handleSpawnProfile)

	mux.HandleFunc("GET /ws/pty", s.handlePTY)
	mux.HandleFunc("GET /ws/events", s.handleEvents)

	mux.Handle("/", s.spaHandler())
}
