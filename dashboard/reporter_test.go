package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReporterFlushesRuntimeState(t *testing.T) {
	var (
		mu      sync.Mutex
		reports []InstanceReport
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/report" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		defer r.Body.Close()

		var report InstanceReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Fatalf("decode report: %v", err)
		}

		mu.Lock()
		reports = append(reports, report)
		mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	reporter, err := NewReporter(ReporterConfig{
		Enabled:           true,
		Endpoint:          srv.URL,
		InstanceID:        "worker-a",
		InstanceName:      "worker-a",
		HeartbeatInterval: 20 * time.Millisecond,
	}, "default", "codex", "dev")
	if err != nil {
		t.Fatalf("new reporter: %v", err)
	}

	reporter.SetSnapshotBuilder(func() []SessionGroupReport {
		return []SessionGroupReport{
			{
				SessionKey:      "feishu:u1",
				Platform:        "feishu",
				ActiveSessionID: "s1",
				Interactive:     true,
				Sessions: []SessionReport{
					{ID: "s1", Name: "default", Active: true, Busy: true},
				},
			},
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter.Start(ctx)
	reporter.ObserveInbound("feishu:u1", "build dashboard")
	reporter.ObserveEvent("feishu:u1", "thinking", "planning", "")
	reporter.ObserveOutbound("feishu:u1", "send", "progress message")
	reporter.ObserveTurnFinished("feishu:u1", "idle", "done")

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(reports)
		var latest InstanceReport
		if count > 0 {
			latest = reports[count-1]
		}
		mu.Unlock()

		if count > 0 && len(latest.Groups) == 1 && latest.Groups[0].Runtime.LastAssistantMessage == "done" {
			group := latest.Groups[0]
			if group.Runtime.Status != "idle" {
				t.Fatalf("unexpected runtime status: %s", group.Runtime.Status)
			}
			if group.Runtime.LastUserMessage != "build dashboard" {
				t.Fatalf("unexpected last user message: %q", group.Runtime.LastUserMessage)
			}
			if len(group.Runtime.RecentEvents) == 0 {
				t.Fatalf("expected runtime events to be reported")
			}
			if len(group.Runtime.RecentOutbound) == 0 {
				t.Fatalf("expected outbound messages to be reported")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("reporter did not flush expected payload")
}

func TestReporterKeepsFullLastAssistantMessage(t *testing.T) {
	var (
		mu      sync.Mutex
		reports []InstanceReport
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var report InstanceReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Fatalf("decode report: %v", err)
		}

		mu.Lock()
		reports = append(reports, report)
		mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	reporter, err := NewReporter(ReporterConfig{
		Enabled:           true,
		Endpoint:          srv.URL,
		InstanceID:        "worker-a",
		InstanceName:      "worker-a",
		HeartbeatInterval: 20 * time.Millisecond,
	}, "default", "codex", "dev")
	if err != nil {
		t.Fatalf("new reporter: %v", err)
	}

	reporter.SetSnapshotBuilder(func() []SessionGroupReport {
		return []SessionGroupReport{{SessionKey: "feishu:u1"}}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter.Start(ctx)

	fullMessage := strings.Repeat("assistant-output-", 50)
	reporter.ObserveTurnFinished("feishu:u1", "idle", fullMessage)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(reports)
		var latest InstanceReport
		if count > 0 {
			latest = reports[count-1]
		}
		mu.Unlock()

		if count > 0 && len(latest.Groups) == 1 && latest.Groups[0].Runtime.LastAssistantMessage == fullMessage {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("reporter did not preserve full assistant message")
}
