package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ZacharyJia/cx-connect/config"
)

const (
	maxPlatformMessageLen       = 4000
	newSessionDefaultButtonData = "cc:new:default"
	outputConciseButtonData     = "/output concise"
	outputVerboseButtonData     = "/output verbose"
	outputQuietButtonData       = "/output quiet"
)

type progressOutputMode string

const (
	progressOutputConcise progressOutputMode = "concise"
	progressOutputVerbose progressOutputMode = "verbose"
	progressOutputQuiet   progressOutputMode = "quiet"
)

// VersionInfo is set by main at startup so that /version works.
var VersionInfo string

// Engine routes messages between platforms and the agent for a single project.
type Engine struct {
	name        string
	agent       Agent
	platforms   []Platform
	webPlatform Platform
	sessions    *SessionManager
	ctx         context.Context
	cancel      context.CancelFunc
	i18n        *I18n
	speech      SpeechCfg
	allowUsers  map[string]bool // key = "platform:user_id"

	providerSaveFunc       func(providerName string) error
	providerAddSaveFunc    func(p ProviderConfig) error
	providerRemoveSaveFunc func(name string) error

	cronScheduler *CronScheduler

	// Interactive agent session management
	interactiveMu     sync.Mutex
	interactiveStates map[string]*interactiveState // key = sessionKey
	pendingNewSession map[string]*pendingNewSessionState
}

// interactiveState tracks a running interactive agent session and its permission state.
type interactiveState struct {
	agentSession AgentSession
	platform     Platform
	replyCtx     any
	session      *Session
	mu           sync.Mutex
	pending      *pendingPermission
	approveAll   bool // when true, auto-approve all permission requests for this session
	progressMode progressOutputMode
	stopCh       chan struct{}
	stopOnce     sync.Once
}

func newInteractiveState(p Platform, replyCtx any, progressMode progressOutputMode) *interactiveState {
	return &interactiveState{
		platform:     p,
		replyCtx:     replyCtx,
		progressMode: progressMode,
		stopCh:       make(chan struct{}),
	}
}

func (s *interactiveState) requestStop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *interactiveState) stopRequested() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

// pendingPermission represents a permission request waiting for user response.
type pendingPermission struct {
	RequestID    string
	ToolName     string
	ToolInput    map[string]any
	InputPreview string
	Resolved     chan struct{} // closed when user responds
}

// pendingNewSessionState tracks a /new flow waiting for the user to provide work dir.
type pendingNewSessionState struct {
	Name string
}

func NewEngine(name string, ag Agent, platforms []Platform, sessionStorePath string, lang Language, allowUsers []config.AllowUser) *Engine {
	ctx, cancel := context.WithCancel(context.Background())

	// Build whitelist map
	allowUsersMap := make(map[string]bool)
	for _, u := range allowUsers {
		key := u.Platform + ":" + u.UserID
		allowUsersMap[key] = true
	}

	return &Engine{
		name:              name,
		agent:             ag,
		platforms:         platforms,
		webPlatform:       newWebAdminPlatform(),
		sessions:          NewSessionManager(sessionStorePath),
		ctx:               ctx,
		cancel:            cancel,
		i18n:              NewI18n(lang),
		interactiveStates: make(map[string]*interactiveState),
		pendingNewSession: make(map[string]*pendingNewSessionState),
		allowUsers:        allowUsersMap,
	}
}

// isAllowedUser checks if a user is allowed to use the bot.
// Returns true if no whitelist is configured (backward compatibility).
func (e *Engine) isAllowedUser(platform, userID string) bool {
	if len(e.allowUsers) == 0 {
		return true
	}
	key := platform + ":" + userID
	return e.allowUsers[key]
}

// SetSpeechConfig configures the speech-to-text subsystem.
func (e *Engine) SetSpeechConfig(cfg SpeechCfg) {
	e.speech = cfg
}

func (e *Engine) SetLanguageSaveFunc(fn func(Language) error) {
	e.i18n.SetSaveFunc(fn)
}

func (e *Engine) SetProviderSaveFunc(fn func(providerName string) error) {
	e.providerSaveFunc = fn
}

func (e *Engine) SetProviderAddSaveFunc(fn func(ProviderConfig) error) {
	e.providerAddSaveFunc = fn
}

func (e *Engine) SetProviderRemoveSaveFunc(fn func(string) error) {
	e.providerRemoveSaveFunc = fn
}

func (e *Engine) SetCronScheduler(cs *CronScheduler) {
	e.cronScheduler = cs
}

func (e *Engine) ProjectName() string {
	return e.name
}

// ExecuteCronJob runs a cron job by injecting a synthetic message into the engine.
// It finds the platform that owns the session key, reconstructs a reply context,
// and processes the message as if the user sent it.
func (e *Engine) ExecuteCronJob(job *CronJob) error {
	sessionKey := job.SessionKey
	platformName := ""
	if idx := strings.Index(sessionKey, ":"); idx > 0 {
		platformName = sessionKey[:idx]
	}

	var targetPlatform Platform
	for _, p := range e.platforms {
		if p.Name() == platformName {
			targetPlatform = p
			break
		}
	}
	if targetPlatform == nil {
		return fmt.Errorf("platform %q not found for session %q", platformName, sessionKey)
	}

	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		return fmt.Errorf("platform %q does not support proactive messaging (cron)", platformName)
	}

	replyCtx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		return fmt.Errorf("reconstruct reply context: %w", err)
	}

	// Notify user that a cron job is executing
	desc := job.Description
	if desc == "" {
		desc = truncateStr(job.Prompt, 40)
	}
	e.send(targetPlatform, replyCtx, fmt.Sprintf("⏰ %s", desc))

	msg := &Message{
		SessionKey: sessionKey,
		Platform:   platformName,
		UserID:     "cron",
		UserName:   "cron",
		Content:    job.Prompt,
		ReplyCtx:   replyCtx,
	}

	session := e.sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		return fmt.Errorf("session %q is busy", sessionKey)
	}

	e.processInteractiveMessage(targetPlatform, msg, session)
	return nil
}

func (e *Engine) Start() error {
	for _, p := range e.platforms {
		if err := p.Start(e.handleMessage); err != nil {
			return fmt.Errorf("[%s] start platform %s: %w", e.name, p.Name(), err)
		}
		slog.Info("platform started", "project", e.name, "platform", p.Name())
	}
	slog.Info("engine started", "project", e.name, "agent", e.agent.Name(), "platforms", len(e.platforms))
	return nil
}

func (e *Engine) Stop() error {
	e.cancel()

	e.interactiveMu.Lock()
	states := make([]*interactiveState, 0, len(e.interactiveStates))
	for key, state := range e.interactiveStates {
		delete(e.interactiveStates, key)
		states = append(states, state)
	}
	e.interactiveMu.Unlock()

	for _, state := range states {
		e.closeInteractiveState(state, false)
	}

	var errs []error
	for _, p := range e.platforms {
		if err := p.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop platform %s: %w", p.Name(), err))
		}
	}
	if err := e.agent.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("stop agent %s: %w", e.agent.Name(), err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("engine stop errors: %v", errs)
	}
	return nil
}

func (e *Engine) handleMessage(p Platform, msg *Message) {
	// Check whitelist
	if !e.isAllowedUser(msg.Platform, msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgUserNotAllowed))
		slog.Warn("user not in whitelist",
			"platform", msg.Platform,
			"user_id", msg.UserID,
			"user_name", msg.UserName,
		)
		return
	}

	// Voice message: transcribe to text first
	if msg.Audio != nil {
		e.handleVoiceMessage(p, msg)
		return
	}

	content := strings.TrimSpace(msg.Content)
	if content == "" && len(msg.Images) == 0 {
		return
	}

	if e.handlePendingNewSession(p, msg, content) {
		return
	}

	if len(msg.Images) == 0 && strings.HasPrefix(content, "/") {
		e.handleCommand(p, msg, content)
		return
	}

	// Permission responses bypass the session lock
	if e.handlePendingPermission(p, msg, content) {
		return
	}

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	slog.Info("processing message",
		"platform", msg.Platform,
		"user", msg.UserName,
		"session", session.ID,
	)

	go e.processInteractiveMessage(p, msg, session)
}

// ──────────────────────────────────────────────────────────────
// Voice message handling
// ──────────────────────────────────────────────────────────────

func (e *Engine) handleVoiceMessage(p Platform, msg *Message) {
	if !e.speech.Enabled || e.speech.STT == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgVoiceNotEnabled))
		return
	}

	audio := msg.Audio
	if NeedsConversion(audio.Format) && !HasFFmpeg() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgVoiceNoFFmpeg))
		return
	}

	slog.Info("transcribing voice message",
		"platform", msg.Platform, "user", msg.UserName,
		"format", audio.Format, "size", len(audio.Data),
	)
	e.send(p, msg.ReplyCtx, e.i18n.T(MsgVoiceTranscribing))

	text, err := TranscribeAudio(e.ctx, e.speech.STT, audio, e.speech.Language)
	if err != nil {
		slog.Error("speech transcription failed", "error", err)
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgVoiceTranscribeFailed), err))
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgVoiceEmpty))
		return
	}

	slog.Info("voice transcribed", "text_len", len(text))
	e.send(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgVoiceTranscribed), text))

	// Replace audio with transcribed text and re-dispatch
	msg.Audio = nil
	msg.Content = text
	e.handleMessage(p, msg)
}

// ──────────────────────────────────────────────────────────────
// Permission handling
// ──────────────────────────────────────────────────────────────

func (e *Engine) handlePendingPermission(p Platform, msg *Message, content string) bool {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()
	if !ok || state == nil {
		return false
	}

	state.mu.Lock()
	pending := state.pending
	state.mu.Unlock()
	if pending == nil {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(content))

	if isApproveAllResponse(lower) {
		state.mu.Lock()
		state.approveAll = true
		state.mu.Unlock()

		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{
			Behavior:     "allow",
			UpdatedInput: pending.ToolInput,
		}); err != nil {
			slog.Error("failed to send permission response", "error", err)
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionApproveAll))
		}
	} else if isAllowResponse(lower) {
		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{
			Behavior:     "allow",
			UpdatedInput: pending.ToolInput,
		}); err != nil {
			slog.Error("failed to send permission response", "error", err)
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionAllowed))
		}
	} else if isDenyResponse(lower) {
		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{
			Behavior: "deny",
			Message:  "User denied this tool use.",
		}); err != nil {
			slog.Error("failed to send deny response", "error", err)
		}
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionDenied))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionHint))
		return true
	}

	state.mu.Lock()
	state.pending = nil
	state.mu.Unlock()
	close(pending.Resolved)

	return true
}

func (e *Engine) handlePendingNewSession(p Platform, msg *Message, content string) bool {
	e.interactiveMu.Lock()
	pending, ok := e.pendingNewSession[msg.SessionKey]
	e.interactiveMu.Unlock()
	if !ok || pending == nil {
		return false
	}
	if isKnownSlashCommand(content) {
		// Let explicit bot commands pass through while waiting for workdir input.
		return false
	}

	workDir, displayDir, err := e.resolveSessionWorkDir(pending.Name, content)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgNewSessionInputHint), e.defaultSessionDisplayDirName(pending.Name)))
		return true
	}

	e.clearPendingNewSession(msg.SessionKey)
	s := e.sessions.NewSession(msg.SessionKey, pending.Name, workDir)
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgNewSessionCreated), s.Name, s.ID, displayDir))
	return true
}

func isApproveAllResponse(s string) bool {
	for _, w := range []string{
		"allow all", "allowall", "approve all", "yes all",
		"允许所有", "允许全部", "全部允许", "所有允许", "都允许", "全部同意",
	} {
		if s == w {
			return true
		}
	}
	return false
}

func isAllowResponse(s string) bool {
	for _, w := range []string{"allow", "yes", "y", "ok", "允许", "同意", "可以", "好", "好的", "是", "确认", "approve"} {
		if s == w {
			return true
		}
	}
	return false
}

func isDenyResponse(s string) bool {
	for _, w := range []string{"deny", "no", "n", "reject", "拒绝", "不允许", "不行", "不", "否", "取消", "cancel"} {
		if s == w {
			return true
		}
	}
	return false
}

func isKnownSlashCommand(content string) bool {
	parts := strings.Fields(strings.TrimSpace(content))
	if len(parts) == 0 {
		return false
	}
	cmd := strings.ToLower(parts[0])
	switch cmd {
	case "/new", "/list", "/sessions", "/switch", "/current", "/history",
		"/allow", "/mode", "/lang", "/output", "/quiet", "/provider", "/memory",
		"/compress", "/compact", "/cron", "/stop",
		"/help", "/version":
		return true
	default:
		return false
	}
}

// ──────────────────────────────────────────────────────────────
// Interactive agent processing
// ──────────────────────────────────────────────────────────────

func (e *Engine) processInteractiveMessage(p Platform, msg *Message, session *Session) {
	defer session.Unlock()

	e.i18n.DetectAndSet(msg.Content)
	session.AddHistory("user", msg.Content)

	state := e.getOrCreateInteractiveState(msg.SessionKey, p, msg.ReplyCtx, session)

	// Update reply context for this turn
	state.mu.Lock()
	state.platform = p
	state.replyCtx = msg.ReplyCtx
	state.mu.Unlock()

	if state.agentSession == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), "failed to start agent session"))
		return
	}

	if err := state.agentSession.Send(msg.Content, msg.Images); err != nil {
		slog.Error("failed to send prompt", "error", err)

		if !state.agentSession.Alive() {
			e.cleanupInteractiveState(msg.SessionKey)
			e.send(p, msg.ReplyCtx, e.i18n.T(MsgSessionRestarting))

			state = e.getOrCreateInteractiveState(msg.SessionKey, p, msg.ReplyCtx, session)
			if state.agentSession == nil {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), "failed to restart agent session"))
				return
			}
			if err := state.agentSession.Send(msg.Content, msg.Images); err != nil {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
				return
			}
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
			return
		}
	}

	e.processInteractiveEvents(state, session, msg.SessionKey)
}

func (e *Engine) getOrCreateInteractiveState(sessionKey string, p Platform, replyCtx any, session *Session) *interactiveState {
	var staleState *interactiveState
	progressMode := progressOutputConcise

	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[sessionKey]
	if ok && state != nil {
		state.mu.Lock()
		progressMode = normalizeProgressOutputMode(state.progressMode)
		currentSession := state.session
		state.mu.Unlock()

		if state.agentSession != nil && state.agentSession.Alive() && currentSession != nil && currentSession.ID == session.ID {
			state.mu.Lock()
			state.session = session
			state.platform = p
			state.replyCtx = replyCtx
			state.mu.Unlock()
			e.interactiveMu.Unlock()
			return state
		}

		delete(e.interactiveStates, sessionKey)
		staleState = state
	}
	e.interactiveMu.Unlock()

	if staleState != nil {
		e.closeInteractiveState(staleState, true)
	}

	// Inject per-session env vars so the agent subprocess can call `cx-connect cron add` etc.
	if inj, ok := e.agent.(SessionEnvInjector); ok {
		env := []string{
			"CC_PROJECT=" + e.name,
			"CC_SESSION_KEY=" + sessionKey,
		}
		// Inject session work directory if set
		if session.WorkDir != "" {
			env = append(env, "CC_SESSION_DIR="+session.WorkDir)
		}
		inj.SetSessionEnv(env)
	}

	agentSession, err := e.agent.StartSession(e.ctx, session.AgentSessionID)
	if err != nil {
		slog.Error("failed to start interactive session", "error", err)
		state = newInteractiveState(p, replyCtx, progressMode)
		state.session = session
		e.interactiveMu.Lock()
		e.interactiveStates[sessionKey] = state
		e.interactiveMu.Unlock()
		return state
	}

	state = newInteractiveState(p, replyCtx, progressMode)
	state.agentSession = agentSession
	state.session = session
	e.interactiveMu.Lock()
	e.interactiveStates[sessionKey] = state
	e.interactiveMu.Unlock()

	slog.Info("interactive session started", "session_key", sessionKey, "agent_session", session.AgentSessionID)
	return state
}

func (e *Engine) cleanupInteractiveState(sessionKey string) {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[sessionKey]
	if ok {
		delete(e.interactiveStates, sessionKey)
	}
	e.interactiveMu.Unlock()
	if ok {
		e.closeInteractiveState(state, true)
	}
}

func (e *Engine) closeInteractiveState(state *interactiveState, async bool) {
	if state == nil {
		return
	}

	state.requestStop()

	state.mu.Lock()
	pending := state.pending
	state.pending = nil
	session := state.session
	agentSession := state.agentSession
	state.mu.Unlock()

	if pending != nil {
		close(pending.Resolved)
	}

	if session != nil && agentSession != nil {
		if currentID := agentSession.CurrentSessionID(); currentID != "" {
			session.AgentSessionID = currentID
			e.sessions.Save()
		}
	}

	if agentSession == nil {
		return
	}

	closeFn := func() {
		if err := agentSession.Close(); err != nil {
			slog.Warn("failed to close interactive session", "error", err)
		}
	}
	if async {
		go closeFn()
		return
	}
	closeFn()
}

func (e *Engine) processInteractiveEvents(state *interactiveState, session *Session, sessionKey string) {
	var textParts []string
	toolCount := 0
	progress := conciseProgressState{}
	var draftCtx any
	lastDraftAt := time.Time{}
	isCodex := strings.EqualFold(e.agent.Name(), "codex")
	var codexDraftParts []string
	var codexPendingLast string
	codexVerboseStreamed := false

	events := state.agentSession.Events()
	for {
		if e.ctx.Err() != nil {
			return
		}
		if state.stopRequested() {
			return
		}

		var event Event
		var ok bool
		select {
		case <-state.stopCh:
			return
		case event, ok = <-events:
			if !ok {
				goto processExited
			}
		}

		state.mu.Lock()
		p := state.platform
		replyCtx := state.replyCtx
		mode := normalizeProgressOutputMode(state.progressMode)
		state.mu.Unlock()

		switch event.Type {
		case EventThinking:
			if event.Content != "" {
				preview := truncate(event.Content, 300)
				switch mode {
				case progressOutputVerbose:
					e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgThinking), preview))
				case progressOutputConcise:
					progress.observeThinking(preview)
					// Throttle frequent thought updates to avoid flood while keeping one draft fresh.
					if time.Since(lastDraftAt) >= 1200*time.Millisecond {
						rendered := renderConciseDraftContent(e.i18n.CurrentLang(), progress, codexDraftParts, false)
						if updated := e.upsertDraft(p, replyCtx, &draftCtx, rendered); updated {
							lastDraftAt = time.Now()
						}
					}
				}
			}

		case EventToolUse:
			toolCount++
			switch mode {
			case progressOutputVerbose:
				inputPreview := truncate(event.ToolInput, 500)
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgTool), toolCount, event.ToolName, inputPreview))
			case progressOutputConcise:
				progress.observeTool(toolCount, event.ToolName)
				rendered := renderConciseDraftContent(e.i18n.CurrentLang(), progress, codexDraftParts, false)
				if updated := e.upsertDraft(p, replyCtx, &draftCtx, rendered); updated {
					lastDraftAt = time.Now()
				}
			}

		case EventText:
			if event.Content != "" {
				textParts = append(textParts, event.Content)
				if isCodex {
					switch mode {
					case progressOutputVerbose:
						for _, chunk := range splitMessage(event.Content, maxPlatformMessageLen) {
							e.send(p, replyCtx, chunk)
						}
						codexVerboseStreamed = true
					case progressOutputConcise:
						// We keep one message pending so we can treat only the final message as final output.
						if codexPendingLast != "" {
							codexDraftParts = append(codexDraftParts, codexPendingLast)
							rendered := renderConciseDraftContent(e.i18n.CurrentLang(), progress, codexDraftParts, false)
							if updated := e.upsertDraft(p, replyCtx, &draftCtx, rendered); updated {
								lastDraftAt = time.Now()
							}
						}
						codexPendingLast = event.Content
					}
				}
			}
			if event.SessionID != "" && session.AgentSessionID == "" {
				session.AgentSessionID = event.SessionID
				e.sessions.Save()
			}

		case EventPermissionRequest:
			state.mu.Lock()
			autoApprove := state.approveAll
			state.mu.Unlock()

			if autoApprove {
				slog.Debug("auto-approving (approve-all)", "request_id", event.RequestID, "tool", event.ToolName)
				_ = state.agentSession.RespondPermission(event.RequestID, PermissionResult{
					Behavior:     "allow",
					UpdatedInput: event.ToolInputRaw,
				})
				continue
			}

			slog.Info("permission request",
				"request_id", event.RequestID,
				"tool", event.ToolName,
			)

			prompt := fmt.Sprintf(e.i18n.T(MsgPermissionPrompt), event.ToolName, truncate(event.ToolInput, 800))
			e.send(p, replyCtx, prompt)

			pending := &pendingPermission{
				RequestID:    event.RequestID,
				ToolName:     event.ToolName,
				ToolInput:    event.ToolInputRaw,
				InputPreview: event.ToolInput,
				Resolved:     make(chan struct{}),
			}
			state.mu.Lock()
			state.pending = pending
			state.mu.Unlock()

			select {
			case <-pending.Resolved:
			case <-state.stopCh:
				return
			}
			slog.Info("permission resolved", "request_id", event.RequestID)

		case EventResult:
			if event.SessionID != "" {
				session.AgentSessionID = event.SessionID
			}

			lang := e.i18n.CurrentLang()
			fullResponse := event.Content
			finalOutbound := ""

			if isCodex {
				switch mode {
				case progressOutputVerbose:
					fullResponse = joinAgentMessagesWithSeparator(textParts, lang)
					if fullResponse == "" {
						fullResponse = event.Content
					}
					if fullResponse == "" {
						fullResponse = e.i18n.T(MsgEmptyResponse)
					}
					// In verbose mode, agent messages have already been streamed individually.
					if !codexVerboseStreamed {
						finalOutbound = fullResponse
					}
				case progressOutputQuiet:
					fullResponse = joinAgentMessagesWithSeparator(textParts, lang)
					if fullResponse == "" {
						fullResponse = event.Content
					}
					if fullResponse == "" {
						fullResponse = e.i18n.T(MsgEmptyResponse)
					}
					finalOutbound = fullResponse
				default:
					finalMessage := codexPendingLast
					if finalMessage == "" && len(textParts) > 0 {
						finalMessage = textParts[len(textParts)-1]
					}
					if finalMessage == "" {
						finalMessage = event.Content
					}

					historyParts := append([]string{}, codexDraftParts...)
					if finalMessage != "" {
						historyParts = append(historyParts, finalMessage)
					}
					fullResponse = joinAgentMessagesWithSeparator(historyParts, lang)
					if fullResponse == "" {
						if finalMessage != "" {
							fullResponse = finalMessage
						} else {
							fullResponse = e.i18n.T(MsgEmptyResponse)
						}
					}

					if draftCtx != nil {
						rendered := renderConciseDraftContent(lang, progress, codexDraftParts, true)
						e.updateDraft(p, draftCtx, rendered)
					}

					if finalMessage != "" {
						finalOutbound = finalMessage
					} else {
						finalOutbound = fullResponse
					}
				}
			} else {
				if fullResponse == "" && len(textParts) > 0 {
					fullResponse = strings.Join(textParts, "")
				}
				if fullResponse == "" {
					fullResponse = e.i18n.T(MsgEmptyResponse)
				}
				finalOutbound = fullResponse
				if mode == progressOutputConcise && draftCtx != nil {
					rendered := renderConciseDraftContent(lang, progress, nil, true)
					e.updateDraft(p, draftCtx, rendered)
				}
			}

			session.AddHistory("assistant", fullResponse)
			e.sessions.Save()

			slog.Debug("turn complete",
				"session", session.ID,
				"agent_session", session.AgentSessionID,
				"tools", toolCount,
				"response_len", len(fullResponse),
			)
			if strings.TrimSpace(finalOutbound) == "" {
				return
			}

			for _, chunk := range splitMessage(finalOutbound, maxPlatformMessageLen) {
				if err := p.Send(e.ctx, replyCtx, chunk); err != nil {
					slog.Error("failed to send reply", "error", err)
					return
				}
			}
			return

		case EventError:
			if event.Error != nil {
				slog.Error("agent error", "error", event.Error)
				if mode == progressOutputConcise && draftCtx != nil {
					rendered := renderConciseDraftContent(e.i18n.CurrentLang(), progress, codexDraftParts, true)
					e.updateDraft(p, draftCtx, rendered)
				}
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), event.Error))
			}
			return
		}
	}

processExited:
	if state.stopRequested() {
		return
	}

	// Channel closed - process exited unexpectedly
	slog.Warn("agent process exited", "session_key", sessionKey)
	e.cleanupInteractiveState(sessionKey)

	if len(textParts) > 0 {
		state.mu.Lock()
		p := state.platform
		replyCtx := state.replyCtx
		mode := normalizeProgressOutputMode(state.progressMode)
		state.mu.Unlock()

		if isCodex && mode == progressOutputVerbose && codexVerboseStreamed {
			return
		}

		fullResponse := strings.Join(textParts, "")
		if isCodex {
			fullResponse = joinAgentMessagesWithSeparator(textParts, e.i18n.CurrentLang())
		}
		session.AddHistory("assistant", fullResponse)
		for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
			e.send(p, replyCtx, chunk)
		}
	}
}

type conciseProgressState struct {
	thinkingCount int
	toolCount     int
	lastThinking  string
	lastToolLabel string
}

func (ps *conciseProgressState) observeThinking(preview string) {
	ps.thinkingCount++
	ps.lastThinking = preview
}

func (ps *conciseProgressState) observeTool(n int, toolName string) {
	ps.toolCount = n
	if toolName == "" {
		toolName = "Tool"
	}
	ps.lastToolLabel = fmt.Sprintf("#%d %s", n, toolName)
}

func (ps *conciseProgressState) render(lang Language, done bool) string {
	lastThinking := ps.lastThinking
	if lastThinking == "" {
		lastThinking = "-"
	}
	lastTool := ps.lastToolLabel
	if lastTool == "" {
		lastTool = "-"
	}

	if lang == LangChinese {
		status := "运行中"
		if done {
			status = "已完成"
		}
		return fmt.Sprintf(
			"📝 进度摘要（简洁模式）\n状态: %s\n思考事件: %d\n工具调用: %d\n最近工具: %s\n最近思考: %s",
			status, ps.thinkingCount, ps.toolCount, lastTool, truncate(lastThinking, 140),
		)
	}

	status := "Running"
	if done {
		status = "Completed"
	}
	return fmt.Sprintf(
		"📝 Progress Summary (concise)\nStatus: %s\nThinking events: %d\nTool calls: %d\nLatest tool: %s\nLatest thought: %s",
		status, ps.thinkingCount, ps.toolCount, lastTool, truncate(lastThinking, 140),
	)
}

func joinAgentMessagesWithSeparator(parts []string, lang Language) string {
	var kept []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return ""
	}

	sep := "\n\n-----\n\n"
	var sb strings.Builder
	for i, p := range kept {
		if i > 0 {
			sb.WriteString(sep)
		}
		if lang == LangChinese {
			sb.WriteString(fmt.Sprintf("第 %d 段:\n%s", i+1, p))
		} else {
			sb.WriteString(fmt.Sprintf("Part %d:\n%s", i+1, p))
		}
	}
	return sb.String()
}

func renderConciseDraftContent(lang Language, progress conciseProgressState, nonFinalParts []string, done bool) string {
	base := progress.render(lang, done)
	nonFinal := joinAgentMessagesWithSeparator(nonFinalParts, lang)
	if nonFinal == "" {
		return truncate(base, 3600)
	}

	var content string
	if lang == LangChinese {
		content = fmt.Sprintf("%s\n\n⏳ 中间 agent_message（非最终结论）\n\n%s", base, nonFinal)
	} else {
		content = fmt.Sprintf("%s\n\n⏳ Interim agent_message (not final)\n\n%s", base, nonFinal)
	}
	return truncate(content, 3600)
}

func normalizeProgressOutputMode(mode progressOutputMode) progressOutputMode {
	switch mode {
	case progressOutputVerbose, progressOutputQuiet, progressOutputConcise:
		return mode
	default:
		return progressOutputConcise
	}
}

func parseProgressOutputMode(raw string) (progressOutputMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "concise", "summary", "simple", "default", "简洁", "简洁模式":
		return progressOutputConcise, true
	case "verbose", "detail", "detailed", "啰嗦", "详细", "详细模式":
		return progressOutputVerbose, true
	case "quiet", "silent", "安静", "静默", "安静模式":
		return progressOutputQuiet, true
	default:
		return progressOutputConcise, false
	}
}

// ──────────────────────────────────────────────────────────────
// Command handling
// ──────────────────────────────────────────────────────────────

func (e *Engine) handleCommand(p Platform, msg *Message, raw string) {
	parts := strings.Fields(raw)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/new":
		e.cmdNew(p, msg, args)
	case "/list", "/sessions":
		e.cmdList(p, msg)
	case "/switch":
		e.cmdSwitch(p, msg, args)
	case "/current":
		e.cmdCurrent(p, msg)
	case "/history":
		e.cmdHistory(p, msg, args)
	case "/allow":
		e.cmdAllow(p, msg, args)
	case "/mode":
		e.cmdMode(p, msg, args)
	case "/lang":
		e.cmdLang(p, msg, args)
	case "/output":
		e.cmdOutput(p, msg, args)
	case "/quiet":
		e.cmdQuiet(p, msg)
	case "/provider":
		e.cmdProvider(p, msg, args)
	case "/memory":
		e.cmdMemory(p, msg, args)
	case "/cron":
		e.cmdCron(p, msg, args)
	case "/compress", "/compact":
		e.cmdCompress(p, msg)
	case "/stop":
		e.cmdStop(p, msg)
	case "/help":
		e.cmdHelp(p, msg)
	case "/version":
		e.reply(p, msg.ReplyCtx, VersionInfo)
	default:
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("Unknown command: %s\nType /help for available commands.", cmd))
	}
}

func (e *Engine) cmdNew(p Platform, msg *Message, args []string) {
	e.cleanupInteractiveState(msg.SessionKey)

	name := newRandomSessionName()
	if len(args) > 0 {
		name = args[0]
	}
	workDir := ""
	if len(args) > 1 {
		workDir = strings.TrimSpace(strings.Join(args[1:], " "))
	}

	e.clearPendingNewSession(msg.SessionKey)

	// Explicit work dir provided: create immediately.
	if workDir != "" {
		resolvedDir, displayDir, err := e.resolveSessionWorkDir(name, workDir)
		if err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
			return
		}
		s := e.sessions.NewSession(msg.SessionKey, name, resolvedDir)
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgNewSessionCreated), s.Name, s.ID, displayDir))
		return
	}

	// No work dir provided: ask user to pick default or enter a path.
	e.setPendingNewSession(msg.SessionKey, name)
	defaultDisplay := e.defaultSessionDisplayDirName(name)
	prompt := fmt.Sprintf("%s\n\n1. %s\n2. %s\n\n%s",
		fmt.Sprintf(e.i18n.T(MsgNewSessionWorkDir), name),
		fmt.Sprintf(e.i18n.T(MsgNewSessionDefaultDir), defaultDisplay),
		e.i18n.T(MsgNewSessionCustomDir),
		fmt.Sprintf(e.i18n.T(MsgNewSessionInputHint), defaultDisplay),
	)
	e.replyWithButtons(p, msg.ReplyCtx, prompt, []Button{
		{Text: e.i18n.T(MsgNewSessionUseDefault), Data: newSessionDefaultButtonData},
	})
}

func (e *Engine) cmdList(p Platform, msg *Message) {
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgListError), err))
		return
	}
	if len(agentSessions) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgListEmpty))
		return
	}

	agentName := e.agent.Name()
	activeSession := e.sessions.GetOrCreateActive(msg.SessionKey)
	activeAgentID := activeSession.AgentSessionID

	limit := 20
	if len(agentSessions) < limit {
		limit = len(agentSessions)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(e.i18n.T(MsgListTitle), agentName, len(agentSessions)))
	for i := 0; i < limit; i++ {
		s := agentSessions[i]
		marker := "◻"
		if s.ID == activeAgentID {
			marker = "▶"
		}
		shortID := s.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		summary := s.Summary
		if summary == "" {
			summary = "(empty)"
		}
		sb.WriteString(fmt.Sprintf("%s `%s` · %s · **%d** msgs · %s\n",
			marker, shortID, summary, s.MessageCount, s.ModifiedAt.Format("01-02 15:04")))
	}
	if len(agentSessions) > limit {
		sb.WriteString(fmt.Sprintf(e.i18n.T(MsgListMore), len(agentSessions)-limit))
	}
	sb.WriteString(e.i18n.T(MsgListSwitchHint))
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdSwitch(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, "Usage: /switch <session_id_prefix>")
		return
	}
	prefix := strings.TrimSpace(args[0])

	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

	var matched *AgentSessionInfo
	for i := range agentSessions {
		if strings.HasPrefix(agentSessions[i].ID, prefix) {
			matched = &agentSessions[i]
			break
		}
	}
	if matched == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ No session matching prefix %q", prefix))
		return
	}

	e.cleanupInteractiveState(msg.SessionKey)

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	session.AgentSessionID = matched.ID
	session.Name = matched.Summary
	session.ClearHistory()
	e.sessions.Save()

	shortID := matched.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	e.reply(p, msg.ReplyCtx,
		fmt.Sprintf("✅ Switched to: %s (%s, %d msgs)", matched.Summary, shortID, matched.MessageCount))
}

func (e *Engine) cmdCurrent(p Platform, msg *Message) {
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	agentID := s.AgentSessionID
	if agentID == "" {
		agentID = "(new — not yet started)"
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(
		"📌 Current session\nName: %s\nClaude Session: %s\nLocal messages: %d",
		s.Name, agentID, len(s.History)))
}

func (e *Engine) cmdHistory(p Platform, msg *Message, args []string) {
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	n := 10
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}

	entries := s.GetHistory(n)

	// Fallback: load from agent backend if in-memory history is empty
	if len(entries) == 0 && s.AgentSessionID != "" {
		if hp, ok := e.agent.(HistoryProvider); ok {
			if agentEntries, err := hp.GetSessionHistory(e.ctx, s.AgentSessionID, n); err == nil {
				entries = agentEntries
			}
		}
	}

	if len(entries) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHistoryEmpty))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📜 History (last %d):\n\n", len(entries)))
	for _, h := range entries {
		icon := "👤"
		if h.Role == "assistant" {
			icon = "🤖"
		}
		content := h.Content
		if len([]rune(content)) > 200 {
			content = string([]rune(content)[:200]) + "..."
		}
		sb.WriteString(fmt.Sprintf("%s [%s]\n%s\n\n", icon, h.Timestamp.Format("15:04:05"), content))
	}
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdLang(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		cur := e.i18n.CurrentLang()
		name := langDisplayName(cur)
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgLangCurrent, name))
		return
	}

	target := strings.ToLower(strings.TrimSpace(args[0]))
	var lang Language
	switch target {
	case "en", "english":
		lang = LangEnglish
	case "zh", "cn", "chinese", "中文":
		lang = LangChinese
	case "auto":
		lang = LangAuto
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgLangInvalid))
		return
	}

	e.i18n.SetLang(lang)
	name := langDisplayName(lang)
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgLangChanged, name))
}

func langDisplayName(lang Language) string {
	switch lang {
	case LangEnglish:
		return "English"
	case LangChinese:
		return "中文"
	default:
		return "Auto"
	}
}

func (e *Engine) cmdOutput(p Platform, msg *Message, args []string) {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()

	if !ok || state == nil {
		state = newInteractiveState(p, msg.ReplyCtx, progressOutputConcise)
		e.interactiveMu.Lock()
		e.interactiveStates[msg.SessionKey] = state
		e.interactiveMu.Unlock()
	}

	if len(args) == 0 {
		state.mu.Lock()
		current := normalizeProgressOutputMode(state.progressMode)
		state.mu.Unlock()

		isZh := e.i18n.CurrentLang() == LangChinese
		var sb strings.Builder
		if isZh {
			sb.WriteString("📣 当前输出模式\n\n")
			sb.WriteString(e.renderOutputModeLine(progressOutputConcise, current, "简洁总结", "将思考/工具进度聚合到一条消息中持续更新"))
			sb.WriteString(e.renderOutputModeLine(progressOutputVerbose, current, "啰嗦", "每条思考/工具调用都单独推送"))
			sb.WriteString(e.renderOutputModeLine(progressOutputQuiet, current, "quiet", "不推送进度消息，仅发送最终回复"))
			sb.WriteString("\n使用 `/output <concise|verbose|quiet>` 切换。")
		} else {
			sb.WriteString("📣 Current output mode\n\n")
			sb.WriteString(e.renderOutputModeLine(progressOutputConcise, current, "Concise", "Update one draft message with thinking/tool summary"))
			sb.WriteString(e.renderOutputModeLine(progressOutputVerbose, current, "Verbose", "Send each thinking/tool event as a new message"))
			sb.WriteString(e.renderOutputModeLine(progressOutputQuiet, current, "Quiet", "Hide progress messages and only send final reply"))
			sb.WriteString("\nUse `/output <concise|verbose|quiet>` to switch.")
		}

		e.replyWithButtons(p, msg.ReplyCtx, sb.String(), []Button{
			{Text: "Concise", Data: outputConciseButtonData},
			{Text: "Verbose", Data: outputVerboseButtonData},
			{Text: "Quiet", Data: outputQuietButtonData},
		})
		return
	}

	target, ok := parseProgressOutputMode(args[0])
	if !ok {
		if e.i18n.CurrentLang() == LangChinese {
			e.reply(p, msg.ReplyCtx, "未知输出模式。可用值: `concise` / `verbose` / `quiet`")
		} else {
			e.reply(p, msg.ReplyCtx, "Unknown output mode. Available: `concise` / `verbose` / `quiet`")
		}
		return
	}

	state.mu.Lock()
	state.progressMode = target
	state.platform = p
	state.replyCtx = msg.ReplyCtx
	state.mu.Unlock()

	if e.i18n.CurrentLang() == LangChinese {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("✅ 输出模式已切换为 **%s**。", e.outputModeDisplayName(target, true)))
		return
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf("✅ Output mode switched to **%s**.", e.outputModeDisplayName(target, false)))
}

func (e *Engine) renderOutputModeLine(mode, current progressOutputMode, name, desc string) string {
	marker := "  "
	if mode == current {
		marker = "▶ "
	}
	return fmt.Sprintf("%s**%s** — %s\n", marker, name, desc)
}

func (e *Engine) outputModeDisplayName(mode progressOutputMode, zh bool) string {
	switch mode {
	case progressOutputVerbose:
		if zh {
			return "啰嗦"
		}
		return "Verbose"
	case progressOutputQuiet:
		if zh {
			return "quiet"
		}
		return "Quiet"
	default:
		if zh {
			return "简洁总结"
		}
		return "Concise"
	}
}

func (e *Engine) cmdHelp(p Platform, msg *Message) {
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHelp))
}

func (e *Engine) cmdMode(p Platform, msg *Message, args []string) {
	switcher, ok := e.agent.(ModeSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModeNotSupported))
		return
	}

	if len(args) == 0 {
		current := switcher.GetMode()
		modes := switcher.PermissionModes()
		var sb strings.Builder
		isZh := e.i18n.CurrentLang() == LangChinese
		for _, m := range modes {
			marker := "  "
			if m.Key == current {
				marker = "▶ "
			}
			if isZh {
				sb.WriteString(fmt.Sprintf("%s**%s** — %s\n", marker, m.NameZh, m.DescZh))
			} else {
				sb.WriteString(fmt.Sprintf("%s**%s** — %s\n", marker, m.Name, m.Desc))
			}
		}
		if isZh {
			sb.WriteString("\n使用 `/mode <名称>` 切换模式\n可用值: `default` / `edit` / `plan` / `yolo`")
		} else {
			sb.WriteString("\nUse `/mode <name>` to switch.\nAvailable: `default` / `edit` / `plan` / `yolo`")
		}
		e.reply(p, msg.ReplyCtx, sb.String())
		return
	}

	target := strings.ToLower(args[0])
	switcher.SetMode(target)
	newMode := switcher.GetMode()

	e.cleanupInteractiveState(msg.SessionKey)

	modes := switcher.PermissionModes()
	displayName := newMode
	isZh := e.i18n.CurrentLang() == LangChinese
	for _, m := range modes {
		if m.Key == newMode {
			if isZh {
				displayName = m.NameZh
			} else {
				displayName = m.Name
			}
			break
		}
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgModeChanged), displayName))
}

func (e *Engine) cmdQuiet(p Platform, msg *Message) {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()

	if !ok || state == nil {
		// No state yet, create one so the flag persists
		state = newInteractiveState(p, msg.ReplyCtx, progressOutputQuiet)
		e.interactiveMu.Lock()
		e.interactiveStates[msg.SessionKey] = state
		e.interactiveMu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietOn))
		return
	}

	state.mu.Lock()
	cur := normalizeProgressOutputMode(state.progressMode)
	if cur == progressOutputQuiet {
		state.progressMode = progressOutputConcise
	} else {
		state.progressMode = progressOutputQuiet
	}
	quiet := state.progressMode == progressOutputQuiet
	state.mu.Unlock()

	if quiet {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietOn))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietOff))
	}
}

func (e *Engine) cmdStop(p Platform, msg *Message) {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
	if ok {
		delete(e.interactiveStates, msg.SessionKey)
	}
	e.interactiveMu.Unlock()

	if !ok || state == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNoExecution))
		return
	}

	e.closeInteractiveState(state, true)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgExecutionStopped))
}

func (e *Engine) cmdCompress(p Platform, msg *Message) {
	compressor, ok := e.agent.(ContextCompressor)
	if !ok || compressor.CompressCommand() == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCompressNotSupported))
		return
	}

	// Check for an active interactive session
	e.interactiveMu.Lock()
	state, hasState := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()

	if !hasState || state == nil || state.agentSession == nil || !state.agentSession.Alive() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCompressNoSession))
		return
	}

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	e.send(p, msg.ReplyCtx, e.i18n.T(MsgCompressing))

	msg.Content = compressor.CompressCommand()
	go e.processInteractiveMessage(p, msg, session)
}

func (e *Engine) cmdAllow(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		if auth, ok := e.agent.(ToolAuthorizer); ok {
			tools := auth.GetAllowedTools()
			if len(tools) == 0 {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNoToolsAllowed))
			} else {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCurrentTools), strings.Join(tools, ", ")))
			}
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgToolAuthNotSupported))
		}
		return
	}

	toolName := strings.TrimSpace(args[0])
	if auth, ok := e.agent.(ToolAuthorizer); ok {
		if err := auth.AddAllowedTools(toolName); err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgToolAllowFailed), err))
			return
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgToolAllowedNew), toolName))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgToolAuthNotSupported))
	}
}

func (e *Engine) cmdProvider(p Platform, msg *Message, args []string) {
	switcher, ok := e.agent.(ProviderSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderNotSupported))
		return
	}

	if len(args) == 0 {
		current := switcher.GetActiveProvider()
		if current == nil {
			providers := switcher.ListProviders()
			if len(providers) == 0 {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderNone))
			} else {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderNone))
			}
			return
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderCurrent), current.Name))
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "list":
		providers := switcher.ListProviders()
		if len(providers) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderListEmpty))
			return
		}
		current := switcher.GetActiveProvider()
		var sb strings.Builder
		sb.WriteString(e.i18n.T(MsgProviderListTitle))
		for _, prov := range providers {
			marker := "  "
			if current != nil && prov.Name == current.Name {
				marker = "▶ "
			}
			detail := prov.Name
			if prov.BaseURL != "" {
				detail += " (" + prov.BaseURL + ")"
			}
			if prov.Model != "" {
				detail += " [" + prov.Model + "]"
			}
			sb.WriteString(fmt.Sprintf("%s**%s**\n", marker, detail))
		}
		sb.WriteString("\n" + e.i18n.T(MsgProviderSwitchHint))
		e.reply(p, msg.ReplyCtx, sb.String())

	case "add":
		e.cmdProviderAdd(p, msg, switcher, args[1:])

	case "remove", "rm", "delete":
		e.cmdProviderRemove(p, msg, switcher, args[1:])

	case "switch":
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, "Usage: /provider switch <name>")
			return
		}
		e.switchProvider(p, msg, switcher, args[1])

	case "current":
		current := switcher.GetActiveProvider()
		if current == nil {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderNone))
			return
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderCurrent), current.Name))

	default:
		e.switchProvider(p, msg, switcher, args[0])
	}
}

func (e *Engine) cmdProviderAdd(p Platform, msg *Message, switcher ProviderSwitcher, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderAddUsage))
		return
	}

	var prov ProviderConfig

	// Join args back; detect JSON (starts with '{') vs positional
	raw := strings.Join(args, " ")
	raw = strings.TrimSpace(raw)

	if strings.HasPrefix(raw, "{") {
		// JSON format: /provider add {"name":"relay","api_key":"sk-xxx",...}
		var jp struct {
			Name    string            `json:"name"`
			APIKey  string            `json:"api_key"`
			BaseURL string            `json:"base_url"`
			Model   string            `json:"model"`
			Env     map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(raw), &jp); err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAddFailed), "invalid JSON: "+err.Error()))
			return
		}
		if jp.Name == "" {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAddFailed), "\"name\" is required"))
			return
		}
		prov = ProviderConfig{Name: jp.Name, APIKey: jp.APIKey, BaseURL: jp.BaseURL, Model: jp.Model, Env: jp.Env}
	} else {
		// Positional: /provider add <name> <api_key> [base_url] [model]
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderAddUsage))
			return
		}
		prov.Name = args[0]
		prov.APIKey = args[1]
		if len(args) > 2 {
			prov.BaseURL = args[2]
		}
		if len(args) > 3 {
			prov.Model = args[3]
		}
	}

	// Check for duplicates
	for _, existing := range switcher.ListProviders() {
		if existing.Name == prov.Name {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAddFailed), fmt.Sprintf("provider %q already exists", prov.Name)))
			return
		}
	}

	// Add to runtime
	updated := append(switcher.ListProviders(), prov)
	switcher.SetProviders(updated)

	// Persist to config
	if e.providerAddSaveFunc != nil {
		if err := e.providerAddSaveFunc(prov); err != nil {
			slog.Error("failed to persist provider", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAdded), prov.Name, prov.Name))
}

func (e *Engine) cmdProviderRemove(p Platform, msg *Message, switcher ProviderSwitcher, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, "Usage: /provider remove <name>")
		return
	}
	name := args[0]

	providers := switcher.ListProviders()
	found := false
	var remaining []ProviderConfig
	for _, prov := range providers {
		if prov.Name == name {
			found = true
		} else {
			remaining = append(remaining, prov)
		}
	}

	if !found {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderNotFound), name))
		return
	}

	// If removing the active provider, clear it
	active := switcher.GetActiveProvider()
	switcher.SetProviders(remaining)
	if active != nil && active.Name == name {
		// No active provider after removal
		slog.Info("removed active provider, clearing selection", "name", name)
	}

	// Persist
	if e.providerRemoveSaveFunc != nil {
		if err := e.providerRemoveSaveFunc(name); err != nil {
			slog.Error("failed to persist provider removal", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderRemoved), name))
}

func (e *Engine) switchProvider(p Platform, msg *Message, switcher ProviderSwitcher, name string) {
	if !switcher.SetActiveProvider(name) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderNotFound), name))
		return
	}
	e.cleanupInteractiveState(msg.SessionKey)

	if e.providerSaveFunc != nil {
		if err := e.providerSaveFunc(name); err != nil {
			slog.Error("failed to save provider", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderSwitched), name))
}

// ──────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────

// SendToSession sends a message to an active session from an external caller (API/CLI).
// If sessionKey is empty, it picks the first active session.
func (e *Engine) SendToSession(sessionKey, message string) error {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()

	var state *interactiveState
	if sessionKey != "" {
		state = e.interactiveStates[sessionKey]
	} else {
		// Pick the first active session
		for _, s := range e.interactiveStates {
			state = s
			break
		}
	}

	if state == nil || state.platform == nil {
		return fmt.Errorf("no active session found (key=%q)", sessionKey)
	}

	state.mu.Lock()
	p := state.platform
	replyCtx := state.replyCtx
	state.mu.Unlock()

	return p.Send(e.ctx, replyCtx, message)
}

// send wraps p.Send with error logging.
func (e *Engine) send(p Platform, replyCtx any, content string) {
	if err := p.Send(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform send failed", "platform", p.Name(), "error", err, "content_len", len(content))
	}
}

// reply wraps p.Reply with error logging.
func (e *Engine) reply(p Platform, replyCtx any, content string) {
	if err := p.Reply(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform reply failed", "platform", p.Name(), "error", err, "content_len", len(content))
	}
}

// replyWithButtons wraps p.ReplyWithButtons, falls back to regular reply if not supported.
func (e *Engine) replyWithButtons(p Platform, replyCtx any, content string, buttons []Button) {
	if err := p.ReplyWithButtons(e.ctx, replyCtx, content, buttons); err != nil {
		if err == ErrNotSupported {
			// Fall back to regular reply
			e.reply(p, replyCtx, content)
		} else {
			slog.Error("platform reply with buttons failed", "platform", p.Name(), "error", err)
			// Fall back to regular reply on error
			e.reply(p, replyCtx, content)
		}
	}
}

func (e *Engine) upsertDraft(p Platform, baseReplyCtx any, draftCtx *any, content string) bool {
	if draftCtx == nil {
		return false
	}
	if *draftCtx == nil {
		starter, okStarter := p.(DraftStarter)
		updater, okUpdater := p.(MessageUpdater)
		if !okStarter || !okUpdater {
			return false
		}
		ctx, err := starter.StartDraft(e.ctx, baseReplyCtx, content)
		if err != nil {
			slog.Warn("failed to start draft", "platform", p.Name(), "error", err)
			return false
		}
		*draftCtx = ctx
		// Ensure updater is referenced so both interfaces are required.
		_ = updater
		return true
	}
	return e.updateDraft(p, *draftCtx, content)
}

func (e *Engine) updateDraft(p Platform, draftCtx any, content string) bool {
	updater, ok := p.(MessageUpdater)
	if !ok {
		return false
	}
	if err := updater.UpdateMessage(e.ctx, draftCtx, content); err != nil {
		slog.Warn("failed to update draft", "platform", p.Name(), "error", err)
		return false
	}
	return true
}

func (e *Engine) setPendingNewSession(sessionKey, name string) {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	e.pendingNewSession[sessionKey] = &pendingNewSessionState{Name: name}
}

func (e *Engine) clearPendingNewSession(sessionKey string) {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	delete(e.pendingNewSession, sessionKey)
}

func (e *Engine) resolveSessionWorkDir(sessionName, input string) (string, string, error) {
	workDir := strings.TrimSpace(input)
	if workDir == "" {
		return "", "", fmt.Errorf("working directory cannot be empty")
	}

	if isDefaultSessionWorkDirInput(workDir) {
		abs := e.defaultSessionAbsDir(sessionName)
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", "", fmt.Errorf("create default work dir %q: %w", abs, err)
		}
		return abs, e.formatDisplayPath(abs), nil
	}

	if strings.HasPrefix(workDir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("resolve home directory: %w", err)
		}
		if workDir == "~" {
			workDir = home
		} else if strings.HasPrefix(workDir, "~/") {
			workDir = filepath.Join(home, strings.TrimPrefix(workDir, "~/"))
		}
	}

	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory %q: %w", workDir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", fmt.Errorf("work directory not found: %s", abs)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("work directory is not a directory: %s", abs)
	}

	return abs, e.formatDisplayPath(abs), nil
}

func isDefaultSessionWorkDirInput(input string) bool {
	v := strings.ToLower(strings.TrimSpace(input))
	switch v {
	case "default", "use default", "默认", "默认目录", newSessionDefaultButtonData:
		return true
	default:
		return false
	}
}

func (e *Engine) defaultSessionAbsDir(sessionName string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".cx-connect", "workspace", sanitizeSessionNameForPath(sessionName))
}

func (e *Engine) defaultSessionDisplayDirName(sessionName string) string {
	return filepath.Join("~", ".cx-connect", "workspace", sanitizeSessionNameForPath(sessionName))
}

func sanitizeSessionNameForPath(name string) string {
	safe := strings.TrimSpace(name)
	if safe == "" || safe == "." || safe == ".." {
		return "session"
	}

	safe = strings.ReplaceAll(safe, "/", "_")
	safe = strings.ReplaceAll(safe, "\\", "_")
	safe = strings.ReplaceAll(safe, ":", "_")
	if safe == "" {
		return "session"
	}
	return safe
}

func newRandomSessionName() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "session-" + hex.EncodeToString(b[:])
	}
	return "session-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
}

func (e *Engine) formatDisplayPath(abs string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return abs
	}
	if abs == home {
		return "~"
	}
	prefix := home + string(filepath.Separator)
	if strings.HasPrefix(abs, prefix) {
		return "~" + string(filepath.Separator) + strings.TrimPrefix(abs, prefix)
	}
	return abs
}

// ──────────────────────────────────────────────────────────────
// /memory command
// ──────────────────────────────────────────────────────────────

func (e *Engine) cmdMemory(p Platform, msg *Message, args []string) {
	mp, ok := e.agent.(MemoryFileProvider)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryNotSupported))
		return
	}

	if len(args) == 0 {
		// /memory — show project memory
		e.showMemoryFile(p, msg, mp.ProjectMemoryFile(), false)
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "add":
		text := strings.TrimSpace(strings.Join(args[1:], " "))
		if text == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
			return
		}
		e.appendMemoryFile(p, msg, mp.ProjectMemoryFile(), text)

	case "global":
		if len(args) == 1 {
			// /memory global — show global memory
			e.showMemoryFile(p, msg, mp.GlobalMemoryFile(), true)
			return
		}
		if strings.ToLower(args[1]) == "add" {
			text := strings.TrimSpace(strings.Join(args[2:], " "))
			if text == "" {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
				return
			}
			e.appendMemoryFile(p, msg, mp.GlobalMemoryFile(), text)
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
		}

	case "show":
		e.showMemoryFile(p, msg, mp.ProjectMemoryFile(), false)

	case "help", "--help", "-h":
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))

	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
	}
}

func (e *Engine) showMemoryFile(p Platform, msg *Message, filePath string, isGlobal bool) {
	if filePath == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryNotSupported))
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryEmpty), filePath))
		return
	}

	content := string(data)
	if len([]rune(content)) > 2000 {
		content = string([]rune(content)[:2000]) + "\n\n... (truncated)"
	}

	if isGlobal {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryShowGlobal), filePath, content))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryShowProject), filePath, content))
	}
}

func (e *Engine) appendMemoryFile(p Platform, msg *Message, filePath, text string) {
	if filePath == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryNotSupported))
		return
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAddFailed), err))
		return
	}

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAddFailed), err))
		return
	}
	defer f.Close()

	entry := "\n- " + text + "\n"
	if _, err := f.WriteString(entry); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAddFailed), err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAdded), filePath))
}

// ──────────────────────────────────────────────────────────────
// /cron command
// ──────────────────────────────────────────────────────────────

func (e *Engine) cmdCron(p Platform, msg *Message, args []string) {
	if e.cronScheduler == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronNotAvailable))
		return
	}

	if len(args) == 0 {
		e.cmdCronList(p, msg)
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "add":
		e.cmdCronAdd(p, msg, args[1:])
	case "list":
		e.cmdCronList(p, msg)
	case "del", "delete", "rm", "remove":
		e.cmdCronDel(p, msg, args[1:])
	case "enable":
		e.cmdCronToggle(p, msg, args[1:], true)
	case "disable":
		e.cmdCronToggle(p, msg, args[1:], false)
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronUsage))
	}
}

func (e *Engine) cmdCronAdd(p Platform, msg *Message, args []string) {
	// /cron add <min> <hour> <day> <month> <weekday> <prompt...>
	if len(args) < 6 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronAddUsage))
		return
	}

	cronExpr := strings.Join(args[:5], " ")
	prompt := strings.Join(args[5:], " ")

	job := &CronJob{
		ID:         GenerateCronID(),
		Project:    e.name,
		SessionKey: msg.SessionKey,
		CronExpr:   cronExpr,
		Prompt:     prompt,
		Enabled:    true,
		CreatedAt:  time.Now(),
	}

	if err := e.cronScheduler.AddJob(job); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronAdded), job.ID, cronExpr, truncateStr(prompt, 60)))
}

func (e *Engine) cmdCronList(p Platform, msg *Message) {
	jobs := e.cronScheduler.Store().ListBySessionKey(msg.SessionKey)
	if len(jobs) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronEmpty))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(e.i18n.T(MsgCronListTitle), len(jobs)))
	for _, j := range jobs {
		status := "✅"
		if !j.Enabled {
			status = "⏸"
		}
		desc := j.Description
		if desc == "" {
			desc = truncateStr(j.Prompt, 40)
		}
		sb.WriteString(fmt.Sprintf("\n%s `%s` · %s · %s", status, j.ID, j.CronExpr, desc))
		if !j.LastRun.IsZero() {
			sb.WriteString(fmt.Sprintf(" · last: %s", j.LastRun.Format("01-02 15:04")))
		}
		if j.LastError != "" {
			sb.WriteString(fmt.Sprintf(" · ❌ %s", truncateStr(j.LastError, 30)))
		}
	}
	sb.WriteString(fmt.Sprintf("\n\n%s", e.i18n.T(MsgCronListFooter)))
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdCronDel(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronDelUsage))
		return
	}
	id := args[0]
	if e.cronScheduler.RemoveJob(id) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronDeleted), id))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronNotFound), id))
	}
}

func (e *Engine) cmdCronToggle(p Platform, msg *Message, args []string, enable bool) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronDelUsage))
		return
	}
	id := args[0]
	var err error
	if enable {
		err = e.cronScheduler.EnableJob(id)
	} else {
		err = e.cronScheduler.DisableJob(id)
	}
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}
	if enable {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronEnabled), id))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronDisabled), id))
	}
}

func truncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return s
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		end := maxLen
		if end > len(text) {
			end = len(text)
		}
		if end < len(text) {
			if idx := strings.LastIndex(text[:end], "\n"); idx > 0 {
				end = idx + 1
			}
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	return chunks
}
