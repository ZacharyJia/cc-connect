package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// APIServer exposes a local Unix socket API for external tools (e.g. cron jobs)
// to send messages to active sessions.
type APIServer struct {
	socketPath     string
	socketListener net.Listener
	webListener    net.Listener
	webAddr        string
	socketServer   *http.Server
	webServer      *http.Server
	mux            *http.ServeMux
	engines        map[string]*Engine // project name → engine
	cron           *CronScheduler
	mu             sync.RWMutex
}

// SendRequest is the JSON body for POST /send.
type SendRequest struct {
	Project    string `json:"project"`
	SessionKey string `json:"session_key"`
	Message    string `json:"message"`
}

type AdminPromptRequest struct {
	Project    string `json:"project"`
	SessionKey string `json:"session_key"`
	SessionID  string `json:"session_id"`
	Prompt     string `json:"prompt"`
}

type AdminCreateSessionRequest struct {
	Project    string `json:"project"`
	SessionKey string `json:"session_key"`
	Name       string `json:"name"`
	WorkDir    string `json:"work_dir"`
}

// NewAPIServer creates an API server on a Unix socket.
func NewAPIServer(dataDir, webAddr string) (*APIServer, error) {
	sockDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	sockPath := filepath.Join(sockDir, "api.sock")

	// Remove stale socket
	os.Remove(sockPath)

	socketListener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen unix socket: %w", err)
	}
	os.Chmod(sockPath, 0o660)

	var webListener net.Listener
	if webAddr != "" {
		webListener, err = net.Listen("tcp", webAddr)
		if err != nil {
			socketListener.Close()
			os.Remove(sockPath)
			return nil, fmt.Errorf("listen web address %q: %w", webAddr, err)
		}
		webAddr = webListener.Addr().String()
	}

	s := &APIServer{
		socketPath:     sockPath,
		socketListener: socketListener,
		webListener:    webListener,
		webAddr:        webAddr,
		mux:            http.NewServeMux(),
		engines:        make(map[string]*Engine),
	}
	s.mux.HandleFunc("/send", s.handleSend)
	s.mux.HandleFunc("/sessions", s.handleSessions)
	s.mux.HandleFunc("/cron/add", s.handleCronAdd)
	s.mux.HandleFunc("/cron/list", s.handleCronList)
	s.mux.HandleFunc("/cron/del", s.handleCronDel)
	s.mux.HandleFunc("/api/admin/projects", s.handleAdminProjects)
	s.mux.HandleFunc("/api/admin/sessions", s.handleAdminSessions)
	s.mux.HandleFunc("/api/admin/session", s.handleAdminSession)
	s.mux.HandleFunc("/api/admin/session/create", s.handleAdminCreateSession)
	s.mux.HandleFunc("/api/admin/prompt", s.handleAdminPrompt)
	s.mux.HandleFunc("/docs/api", s.handleWebAPIDocs)
	s.mux.HandleFunc("/", s.handleWebIndex)

	return s, nil
}

func (s *APIServer) SocketPath() string {
	return s.socketPath
}

func (s *APIServer) WebURL() string {
	if s.webAddr == "" {
		return ""
	}
	return "http://" + s.webAddr
}

func (s *APIServer) RegisterEngine(name string, e *Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engines[name] = e
}

func (s *APIServer) SetCronScheduler(cs *CronScheduler) {
	s.cron = cs
}

func (s *APIServer) Start() {
	s.socketServer = &http.Server{Handler: s.mux}
	go s.serve("api", s.socketListener, s.socketServer)
	slog.Info("api server started", "socket", s.socketPath)

	if s.webListener != nil {
		s.webServer = &http.Server{Handler: s.mux}
		go s.serve("web", s.webListener, s.webServer)
		slog.Info("web ui started", "addr", s.WebURL())
	}
}

func (s *APIServer) Stop() {
	if s.socketServer != nil {
		s.socketServer.Close()
	}
	if s.webServer != nil {
		s.webServer.Close()
	}
	if s.socketListener != nil {
		s.socketListener.Close()
	}
	if s.webListener != nil {
		s.webListener.Close()
	}
	os.Remove(s.socketPath)
}

func (s *APIServer) serve(name string, listener net.Listener, server *http.Server) {
	if listener == nil || server == nil {
		return
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		slog.Error(name+" server error", "error", err)
	}
}

func (s *APIServer) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	engine, ok := s.engines[req.Project]
	s.mu.RUnlock()

	if !ok {
		// If only one engine, use it by default
		s.mu.RLock()
		if len(s.engines) == 1 {
			for _, e := range s.engines {
				engine = e
				ok = true
			}
		}
		s.mu.RUnlock()
	}

	if !ok {
		http.Error(w, fmt.Sprintf("project %q not found", req.Project), http.StatusNotFound)
		return
	}

	if err := engine.SendToSession(req.SessionKey, req.Message); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *APIServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type sessionInfo struct {
		Project    string `json:"project"`
		SessionKey string `json:"session_key"`
		Platform   string `json:"platform"`
	}

	var result []sessionInfo
	for name, e := range s.engines {
		e.interactiveMu.Lock()
		for key, state := range e.interactiveStates {
			if state.platform != nil {
				result = append(result, sessionInfo{
					Project:    name,
					SessionKey: key,
					Platform:   state.platform.Name(),
				})
			}
		}
		e.interactiveMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *APIServer) handleAdminProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	projects := make([]string, 0, len(s.engines))
	for name := range s.engines {
		projects = append(projects, name)
	}
	s.mu.RUnlock()

	sort.Strings(projects)
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *APIServer) handleAdminSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	project := r.URL.Query().Get("project")
	project, engine, err := s.resolveEngine(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project": project,
		"groups":  engine.AdminSessionGroups(),
	})
}

func (s *APIServer) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	project := r.URL.Query().Get("project")
	sessionKey := r.URL.Query().Get("session_key")
	sessionID := r.URL.Query().Get("session_id")
	if sessionKey == "" || sessionID == "" {
		http.Error(w, "session_key and session_id are required", http.StatusBadRequest)
		return
	}

	project, engine, err := s.resolveEngine(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	detail, err := engine.AdminSessionDetail(sessionKey, sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	detail.Project = project
	writeJSON(w, http.StatusOK, detail)
}

func (s *APIServer) handleAdminPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req AdminPromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SessionKey == "" || req.SessionID == "" || strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, "session_key, session_id and prompt are required", http.StatusBadRequest)
		return
	}

	project, engine, err := s.resolveEngine(req.Project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if err := engine.SubmitPrompt(req.SessionKey, req.SessionID, req.Prompt); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "accepted",
		"project": project,
	})
}

func (s *APIServer) handleAdminCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req AdminCreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	project, engine, err := s.resolveEngine(req.Project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	result, err := engine.CreateSession(req.SessionKey, req.Name, req.WorkDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result.Project = project
	writeJSON(w, http.StatusCreated, result)
}

func (s *APIServer) handleWebIndex(w http.ResponseWriter, r *http.Request) {
	if s.webListener == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(adminWebUIHTML))
}

func (s *APIServer) handleWebAPIDocs(w http.ResponseWriter, r *http.Request) {
	if s.webListener == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != "/docs/api" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(adminAPIDocsHTML))
}

func (s *APIServer) resolveEngine(project string) (string, *Engine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if project != "" {
		engine, ok := s.engines[project]
		if !ok {
			return "", nil, fmt.Errorf("project %q not found", project)
		}
		return project, engine, nil
	}

	if len(s.engines) == 1 {
		for name, engine := range s.engines {
			return name, engine, nil
		}
	}

	return "", nil, fmt.Errorf("project is required (multiple projects configured)")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ── Cron API ───────────────────────────────────────────────────

// CronAddRequest is the JSON body for POST /cron/add.
type CronAddRequest struct {
	Project     string `json:"project"`
	SessionKey  string `json:"session_key"`
	CronExpr    string `json:"cron_expr"`
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
}

func (s *APIServer) handleCronAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req CronAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.CronExpr == "" || req.Prompt == "" {
		http.Error(w, "cron_expr and prompt are required", http.StatusBadRequest)
		return
	}

	// Resolve project: use provided, or pick single engine
	project := req.Project
	if project == "" {
		s.mu.RLock()
		if len(s.engines) == 1 {
			for name := range s.engines {
				project = name
			}
		}
		s.mu.RUnlock()
	}
	if project == "" {
		http.Error(w, "project is required (multiple projects configured)", http.StatusBadRequest)
		return
	}

	job := &CronJob{
		ID:          GenerateCronID(),
		Project:     project,
		SessionKey:  req.SessionKey,
		CronExpr:    req.CronExpr,
		Prompt:      req.Prompt,
		Description: req.Description,
		Enabled:     true,
	}
	job.CreatedAt = time.Now()

	if err := s.cron.AddJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

func (s *APIServer) handleCronList(w http.ResponseWriter, r *http.Request) {
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")
	var jobs []*CronJob
	if project != "" {
		jobs = s.cron.Store().ListByProject(project)
	} else {
		jobs = s.cron.Store().List()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

func (s *APIServer) handleCronDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	if s.cron.RemoveJob(req.ID) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	} else {
		http.Error(w, fmt.Sprintf("job %q not found", req.ID), http.StatusNotFound)
	}
}
