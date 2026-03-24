package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZacharyJia/cx-connect/config"
)

func TestEngineSubmitPromptActivatesRequestedSession(t *testing.T) {
	agent := &testAgent{}
	platform := &testPlatform{name: "telegram"}
	engine := NewEngine("default", agent, []Platform{platform}, "", LangEnglish, []config.AllowUser{})

	sessionKey := "telegram:chat:user"
	target := engine.sessions.NewSession(sessionKey, "target", "/tmp/target")
	other := engine.sessions.NewSession(sessionKey, "other", "/tmp/other")

	if got := engine.sessions.ActiveSessionID(sessionKey); got != other.ID {
		t.Fatalf("unexpected initial active session: %s", got)
	}

	if err := engine.SubmitPrompt(sessionKey, target.ID, "review current branch"); err != nil {
		t.Fatalf("SubmitPrompt: %v", err)
	}

	waitForCondition(t, func() bool {
		snap, ok := engine.sessions.SessionSnapshot(sessionKey, target.ID, true)
		return ok && len(snap.History) >= 2
	})

	if got := engine.sessions.ActiveSessionID(sessionKey); got != target.ID {
		t.Fatalf("active session mismatch: want %s, got %s", target.ID, got)
	}

	detail, err := engine.AdminSessionDetail(sessionKey, target.ID)
	if err != nil {
		t.Fatalf("AdminSessionDetail: %v", err)
	}
	if len(detail.Session.History) != 2 {
		t.Fatalf("unexpected history length: %d", len(detail.Session.History))
	}
	if detail.Session.History[0].Role != "user" || detail.Session.History[0].Content != "review current branch" {
		t.Fatalf("unexpected user history entry: %+v", detail.Session.History[0])
	}
	if detail.Session.History[1].Role != "assistant" || detail.Session.History[1].Content != "ack: review current branch" {
		t.Fatalf("unexpected assistant history entry: %+v", detail.Session.History[1])
	}
	if detail.Session.AgentSessionID == "" {
		t.Fatal("expected agent session id to be captured")
	}

	if history := other.GetHistory(0); len(history) != 0 {
		t.Fatalf("unexpected history on untouched session: %+v", history)
	}
	sent := platform.messages()
	if len(sent) == 0 || sent[len(sent)-1] != "ack: review current branch" {
		t.Fatalf("unexpected platform output: %+v", sent)
	}
}

func TestHandleMessageDoesNotReuseAgentSessionAcrossLocalSessions(t *testing.T) {
	agent := &testAgent{}
	platform := &testPlatform{name: "telegram"}
	engine := NewEngine("default", agent, []Platform{platform}, "", LangEnglish, []config.AllowUser{})

	sessionKey := "telegram:chat:user"
	first := engine.sessions.NewSession(sessionKey, "first", "/tmp/first")
	second := engine.sessions.NewSession(sessionKey, "second", "/tmp/second")

	if _, err := engine.sessions.SetActiveSession(sessionKey, first.ID); err != nil {
		t.Fatalf("SetActiveSession(first): %v", err)
	}

	engine.handleMessage(platform, &Message{
		SessionKey: sessionKey,
		Platform:   "telegram",
		UserID:     "user",
		UserName:   "tester",
		Content:    "prompt for first",
		ReplyCtx:   "reply",
	})

	waitForCondition(t, func() bool {
		snap, ok := engine.sessions.SessionSnapshot(sessionKey, first.ID, true)
		return ok && snap.AgentSessionID != "" && len(snap.History) >= 2
	})

	firstSnap, ok := engine.sessions.SessionSnapshot(sessionKey, first.ID, true)
	if !ok {
		t.Fatalf("missing snapshot for %s", first.ID)
	}

	if _, err := engine.sessions.SetActiveSession(sessionKey, second.ID); err != nil {
		t.Fatalf("SetActiveSession(second): %v", err)
	}

	engine.handleMessage(platform, &Message{
		SessionKey: sessionKey,
		Platform:   "telegram",
		UserID:     "user",
		UserName:   "tester",
		Content:    "prompt for second",
		ReplyCtx:   "reply",
	})

	waitForCondition(t, func() bool {
		snap, ok := engine.sessions.SessionSnapshot(sessionKey, second.ID, true)
		return ok && snap.AgentSessionID != "" && len(snap.History) >= 2
	})

	secondSnap, ok := engine.sessions.SessionSnapshot(sessionKey, second.ID, true)
	if !ok {
		t.Fatalf("missing snapshot for %s", second.ID)
	}

	if firstSnap.AgentSessionID == secondSnap.AgentSessionID {
		t.Fatalf("expected different agent session ids, got %q for both", firstSnap.AgentSessionID)
	}
}

func TestEngineAdminSessionGroupsExposeActiveAndHistoryCounts(t *testing.T) {
	engine := NewEngine("default", &testAgent{}, []Platform{&testPlatform{name: "telegram"}}, "", LangEnglish, []config.AllowUser{})

	sessionKey := "telegram:chat:user"
	first := engine.sessions.NewSession(sessionKey, "first", "/tmp/first")
	second := engine.sessions.NewSession(sessionKey, "second", "/tmp/second")

	first.AddHistory("user", "one")
	first.AddHistory("assistant", "two")
	second.AddHistory("user", "three")

	groups := engine.AdminSessionGroups()
	if len(groups) != 1 {
		t.Fatalf("unexpected groups length: %d", len(groups))
	}

	group := groups[0]
	if group.SessionKey != sessionKey {
		t.Fatalf("unexpected session key: %s", group.SessionKey)
	}
	if group.ActiveSessionID != second.ID {
		t.Fatalf("unexpected active session id: %s", group.ActiveSessionID)
	}
	if len(group.Sessions) != 2 {
		t.Fatalf("unexpected session count: %d", len(group.Sessions))
	}

	var foundFirst, foundSecond bool
	for _, session := range group.Sessions {
		switch session.ID {
		case first.ID:
			foundFirst = true
			if session.HistoryCount != 2 || session.Active {
				t.Fatalf("unexpected first summary: %+v", session)
			}
		case second.ID:
			foundSecond = true
			if session.HistoryCount != 1 || !session.Active {
				t.Fatalf("unexpected second summary: %+v", session)
			}
		}
	}
	if !foundFirst || !foundSecond {
		t.Fatalf("did not find expected sessions: %+v", group.Sessions)
	}
}

func TestEngineCreateSessionUsesDefaultWorkDirAndActivatesIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	engine := NewEngine("default", &testAgent{}, []Platform{&testPlatform{name: "telegram"}}, "", LangEnglish, []config.AllowUser{})

	result, err := engine.CreateSession("", "web-created", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if !strings.HasPrefix(result.SessionKey, "web:") {
		t.Fatalf("expected web session key, got %s", result.SessionKey)
	}
	if result.Platform != "web" {
		t.Fatalf("expected web platform, got %s", result.Platform)
	}
	if result.Session.ID == "" {
		t.Fatal("expected created session id")
	}
	if result.ActiveSessionID != result.Session.ID {
		t.Fatalf("active session mismatch: %+v", result)
	}
	if result.Session.WorkDir == "" {
		t.Fatal("expected default work dir to be created")
	}
	if result.DisplayWorkDir == "" {
		t.Fatal("expected display work dir")
	}

	detail, err := engine.AdminSessionDetail(result.SessionKey, result.Session.ID)
	if err != nil {
		t.Fatalf("AdminSessionDetail: %v", err)
	}
	if detail.Session.Name != "web-created" {
		t.Fatalf("unexpected session name: %s", detail.Session.Name)
	}
	if got := engine.sessions.ActiveSessionID(result.SessionKey); got != result.Session.ID {
		t.Fatalf("unexpected active session id: %s", got)
	}
}

func TestEngineSubmitPromptToWebSessionUsesBuiltinPlatform(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	engine := NewEngine("default", &testAgent{}, nil, "", LangEnglish, []config.AllowUser{})

	created, err := engine.CreateSession("", "web-only", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := engine.SubmitPrompt(created.SessionKey, created.Session.ID, "hello from web"); err != nil {
		t.Fatalf("SubmitPrompt: %v", err)
	}

	waitForCondition(t, func() bool {
		snap, ok := engine.sessions.SessionSnapshot(created.SessionKey, created.Session.ID, true)
		return ok && len(snap.History) >= 2
	})

	detail, err := engine.AdminSessionDetail(created.SessionKey, created.Session.ID)
	if err != nil {
		t.Fatalf("AdminSessionDetail: %v", err)
	}
	if detail.Platform != "web" {
		t.Fatalf("unexpected platform: %s", detail.Platform)
	}
	if detail.Session.History[0].Content != "hello from web" {
		t.Fatalf("unexpected user history: %+v", detail.Session.History)
	}
}

type testPlatform struct {
	name string
	mu   sync.Mutex
	sent []string
}

func (p *testPlatform) Name() string { return p.name }

func (p *testPlatform) Start(handler MessageHandler) error { return nil }

func (p *testPlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	p.record(content)
	return nil
}

func (p *testPlatform) Send(ctx context.Context, replyCtx any, content string) error {
	p.record(content)
	return nil
}

func (p *testPlatform) ReplyWithButtons(ctx context.Context, replyCtx any, content string, buttons []Button) error {
	p.record(content)
	return nil
}

func (p *testPlatform) Stop() error { return nil }

func (p *testPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	return sessionKey, nil
}

func (p *testPlatform) record(content string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, content)
}

func (p *testPlatform) messages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sent...)
}

type testAgent struct {
	mu      sync.Mutex
	counter int
}

func (a *testAgent) Name() string { return "test-agent" }

func (a *testAgent) StartSession(ctx context.Context, sessionID string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.counter++
	if sessionID == "" {
		sessionID = "agent-session-" + time.Now().Format("150405.000000") + "-" + string(rune('a'+a.counter))
	}
	return &testAgentSession{
		id:     sessionID,
		events: make(chan Event, 4),
		alive:  true,
	}, nil
}

func (a *testAgent) ListSessions(ctx context.Context) ([]AgentSessionInfo, error) { return nil, nil }

func (a *testAgent) Stop() error { return nil }

type testAgentSession struct {
	id     string
	events chan Event
	alive  bool
}

func (s *testAgentSession) Send(prompt string, images []ImageAttachment) error {
	s.events <- Event{Type: EventText, Content: "ack: " + prompt, SessionID: s.id}
	s.events <- Event{Type: EventResult, SessionID: s.id, Done: true}
	return nil
}

func (s *testAgentSession) RespondPermission(requestID string, result PermissionResult) error {
	return nil
}

func (s *testAgentSession) Events() <-chan Event { return s.events }

func (s *testAgentSession) CurrentSessionID() string { return s.id }

func (s *testAgentSession) Alive() bool { return s.alive }

func (s *testAgentSession) Close() error {
	if s.alive {
		s.alive = false
		close(s.events)
	}
	return nil
}

func waitForCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
