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
		WebURL:            "http://10.0.0.7:6380",
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
	progressMessage := strings.Repeat("progress-message-", 80)
	reporter.ObserveEvent("feishu:u1", "thinking", "planning", "")
	reporter.ObserveOutbound("feishu:u1", "draft", progressMessage)
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
			if latest.WebURL != "http://10.0.0.7:6380" || latest.WebHost != "10.0.0.7" || latest.WebPort != "6380" {
				t.Fatalf("unexpected web endpoint: url=%q host=%q port=%q", latest.WebURL, latest.WebHost, latest.WebPort)
			}
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
			if group.Runtime.RunningMessage != progressMessage {
				t.Fatalf("unexpected running message: %q", group.Runtime.RunningMessage)
			}
			if len(group.Runtime.FinalMessages) != 1 || group.Runtime.FinalMessages[0].Content != "done" {
				t.Fatalf("expected final message to be reported, got %#v", group.Runtime.FinalMessages)
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

func TestReporterDoesNotUseToolEventsAsRunningMessage(t *testing.T) {
	reporter, err := NewReporter(ReporterConfig{
		Enabled:      true,
		Endpoint:     "http://127.0.0.1:1",
		InstanceID:   "worker-a",
		InstanceName: "worker-a",
	}, "default", "codex", "dev")
	if err != nil {
		t.Fatalf("new reporter: %v", err)
	}

	reporter.SetSnapshotBuilder(func() []SessionGroupReport {
		return []SessionGroupReport{{SessionKey: "telegram:u1"}}
	})

	reporter.ObserveEvent("telegram:u1", "tool_use", `{"cmd":"go test ./..."}`, "shell")
	reporter.ObserveOutbound("telegram:u1", "draft", "状态: 运行中\n工具调用: 3\n最近工具: shell\n最近思考: 正在整理结果")

	payload := reporter.buildPayload()
	if len(payload.Groups) != 1 {
		t.Fatalf("expected one group, got %d", len(payload.Groups))
	}
	got := payload.Groups[0].Runtime.RunningMessage
	if strings.Contains(got, "工具调用") || strings.Contains(got, "最近工具") || strings.Contains(got, "shell") {
		t.Fatalf("running message contains tool details: %q", got)
	}
	if !strings.Contains(got, "最近思考") {
		t.Fatalf("running message lost readable progress: %q", got)
	}
}
