package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServerAcceptsReportsAndMarksOffline(t *testing.T) {
	server := NewServer(ServerConfig{
		Listen:      "127.0.0.1:0",
		Token:       "secret-token",
		InstanceTTL: 50 * time.Millisecond,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/report", bytes.NewReader(mustJSON(t, InstanceReport{
		InstanceID:   "worker-a",
		InstanceName: "worker-a",
		Project:      "default",
		Agent:        "codex",
		ReportedAt:   time.Now(),
		Groups: []SessionGroupReport{
			{
				SessionKey:      "feishu:u1",
				Platform:        "feishu",
				ActiveSessionID: "s1",
			},
		},
	})))
	req.Header.Set("Authorization", "Bearer secret-token")
	server.handleReport(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unexpected report status: %d", rec.Code)
	}

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	server.handleInstances(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("unexpected list status: %d", listRec.Code)
	}

	var payload struct {
		Instances []InstanceView `json:"instances"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(payload.Instances) != 1 {
		t.Fatalf("expected one instance, got %d", len(payload.Instances))
	}
	if !payload.Instances[0].Online {
		t.Fatalf("expected instance to be online")
	}

	time.Sleep(80 * time.Millisecond)

	listRec = httptest.NewRecorder()
	listReq = httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	server.handleInstances(listRec, listReq)
	if err := json.NewDecoder(listRec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode list response after ttl: %v", err)
	}
	if len(payload.Instances) != 1 {
		t.Fatalf("expected one instance after ttl, got %d", len(payload.Instances))
	}
	if payload.Instances[0].Online {
		t.Fatalf("expected instance to be offline after ttl")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}
