package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ZacharyJia/cx-connect/config"
)

func TestAPIServerAdminCreateSessionAcceptsSessionKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine := NewEngine("default", &testAgent{}, []Platform{&testPlatform{name: "telegram"}}, "", LangEnglish, []config.AllowUser{})
	server := &APIServer{
		mux:     http.NewServeMux(),
		engines: map[string]*Engine{"default": engine},
	}

	payload, err := json.Marshal(AdminCreateSessionRequest{
		Project:    "default",
		SessionKey: "telegram:chat:user",
		Name:       "issue-12",
		WorkDir:    "default",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/session/create", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handleAdminCreateSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var result AdminCreateSessionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if result.SessionKey != "telegram:chat:user" {
		t.Fatalf("unexpected session key: %s", result.SessionKey)
	}
	if got := engine.sessions.ActiveSessionID("telegram:chat:user"); got != result.Session.ID {
		t.Fatalf("unexpected active session: %s", got)
	}
}
