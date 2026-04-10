package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	maxRuntimeEvents   = 20
	maxOutboundEntries = 20
	maxPreviewLen      = 600
)

type ReporterConfig struct {
	Enabled           bool
	Endpoint          string
	Token             string
	InstanceID        string
	InstanceName      string
	HeartbeatInterval time.Duration
}

type runtimeBuffer struct {
	Status               string
	LastUserMessage      string
	LastAssistantMessage string
	LastEventType        string
	LastEventText        string
	UpdatedAt            time.Time
	RecentEvents         []RuntimeEvent
	RecentOutbound       []OutboundMessage
}

type Reporter struct {
	config ReporterConfig
	client *http.Client

	project   string
	agent     string
	version   string
	hostname  string
	pid       int
	startedAt time.Time

	mu              sync.RWMutex
	runtimeByGroup  map[string]*runtimeBuffer
	snapshotBuilder func() []SessionGroupReport

	dirtyCh chan struct{}
}

func NewReporter(cfg ReporterConfig, project, agent, version string) (*Reporter, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("dashboard endpoint is required when dashboard reporting is enabled")
	}

	heartbeat := cfg.HeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = 5 * time.Second
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}

	instanceID := strings.TrimSpace(cfg.InstanceID)
	if instanceID == "" {
		instanceID = fmt.Sprintf("%s-%d-%s", project, os.Getpid(), hostname)
	}

	instanceName := strings.TrimSpace(cfg.InstanceName)
	if instanceName == "" {
		instanceName = hostname
	}

	return &Reporter{
		config: ReporterConfig{
			Enabled:           true,
			Endpoint:          strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"),
			Token:             strings.TrimSpace(cfg.Token),
			InstanceID:        instanceID,
			InstanceName:      instanceName,
			HeartbeatInterval: heartbeat,
		},
		client:         &http.Client{Timeout: 10 * time.Second},
		project:        project,
		agent:          agent,
		version:        version,
		hostname:       hostname,
		pid:            os.Getpid(),
		startedAt:      time.Now(),
		runtimeByGroup: make(map[string]*runtimeBuffer),
		dirtyCh:        make(chan struct{}, 1),
	}, nil
}

func (r *Reporter) SetSnapshotBuilder(fn func() []SessionGroupReport) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshotBuilder = fn
}

func (r *Reporter) Start(ctx context.Context) {
	if r == nil {
		return
	}
	go r.loop(ctx)
	r.markDirty()
}

func (r *Reporter) ObserveInbound(sessionKey, content string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buf := r.ensureBufferLocked(sessionKey)
	buf.Status = "running"
	buf.LastUserMessage = truncatePreview(content)
	buf.UpdatedAt = time.Now()
	r.markDirtyLocked()
}

func (r *Reporter) ObserveEvent(sessionKey string, eventType, content, toolName string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	buf := r.ensureBufferLocked(sessionKey)
	now := time.Now()
	entry := RuntimeEvent{
		Type:      eventType,
		Content:   truncatePreview(content),
		ToolName:  truncatePreview(toolName),
		Timestamp: now,
	}
	buf.RecentEvents = appendBounded(buf.RecentEvents, entry, maxRuntimeEvents)
	buf.LastEventType = entry.Type
	buf.LastEventText = entry.Content
	buf.UpdatedAt = now

	switch eventType {
	case "permission_request":
		buf.Status = "waiting_permission"
	case "error":
		buf.Status = "error"
	case "result":
		buf.Status = "completed"
	case "thinking", "tool_use", "tool_result", "text":
		buf.Status = "running"
	}
	r.markDirtyLocked()
}

func (r *Reporter) ObserveOutbound(sessionKey, kind, content string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	buf := r.ensureBufferLocked(sessionKey)
	now := time.Now()
	buf.RecentOutbound = appendBounded(buf.RecentOutbound, OutboundMessage{
		Kind:      kind,
		Content:   truncatePreview(content),
		Timestamp: now,
	}, maxOutboundEntries)
	buf.UpdatedAt = now
	r.markDirtyLocked()
}

func (r *Reporter) ObserveTurnFinished(sessionKey, status, assistantText string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	buf := r.ensureBufferLocked(sessionKey)
	if trimmed := strings.TrimSpace(assistantText); trimmed != "" {
		buf.LastAssistantMessage = truncatePreview(trimmed)
	}
	if status != "" {
		buf.Status = status
	} else {
		buf.Status = "idle"
	}
	buf.UpdatedAt = time.Now()
	r.markDirtyLocked()
}

func (r *Reporter) loop(ctx context.Context) {
	ticker := time.NewTicker(r.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if err := r.flush(); err != nil {
				slog.Warn("dashboard reporter final flush failed", "error", err)
			}
			return
		case <-ticker.C:
			if err := r.flush(); err != nil {
				slog.Warn("dashboard reporter heartbeat failed", "error", err)
			}
		case <-r.dirtyCh:
			if err := r.flush(); err != nil {
				slog.Warn("dashboard reporter flush failed", "error", err)
			}
		}
	}
}

func (r *Reporter) flush() error {
	payload := r.buildPayload()
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal dashboard payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, r.config.Endpoint+"/api/report", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create dashboard request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.config.Token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("post dashboard report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("dashboard report rejected: %s", resp.Status)
	}
	return nil
}

func (r *Reporter) buildPayload() InstanceReport {
	r.mu.RLock()
	builder := r.snapshotBuilder
	runtimeByGroup := make(map[string]runtimeBuffer, len(r.runtimeByGroup))
	for key, buf := range r.runtimeByGroup {
		if buf == nil {
			continue
		}
		runtimeByGroup[key] = runtimeBuffer{
			Status:               buf.Status,
			LastUserMessage:      buf.LastUserMessage,
			LastAssistantMessage: buf.LastAssistantMessage,
			LastEventType:        buf.LastEventType,
			LastEventText:        buf.LastEventText,
			UpdatedAt:            buf.UpdatedAt,
			RecentEvents:         append([]RuntimeEvent(nil), buf.RecentEvents...),
			RecentOutbound:       append([]OutboundMessage(nil), buf.RecentOutbound...),
		}
	}
	r.mu.RUnlock()

	var groups []SessionGroupReport
	if builder != nil {
		groups = builder()
	}
	for i := range groups {
		if buf, ok := runtimeByGroup[groups[i].SessionKey]; ok {
			groups[i].Runtime = RuntimeState{
				Status:               buf.Status,
				LastUserMessage:      buf.LastUserMessage,
				LastAssistantMessage: buf.LastAssistantMessage,
				LastEventType:        buf.LastEventType,
				LastEventText:        buf.LastEventText,
				UpdatedAt:            buf.UpdatedAt,
				RecentEvents:         buf.RecentEvents,
				RecentOutbound:       buf.RecentOutbound,
			}
		}
	}

	return InstanceReport{
		InstanceID:   r.config.InstanceID,
		InstanceName: r.config.InstanceName,
		Project:      r.project,
		Agent:        r.agent,
		Version:      r.version,
		Hostname:     r.hostname,
		PID:          r.pid,
		StartedAt:    r.startedAt,
		ReportedAt:   time.Now(),
		Groups:       groups,
	}
}

func (r *Reporter) ensureBufferLocked(sessionKey string) *runtimeBuffer {
	buf := r.runtimeByGroup[sessionKey]
	if buf == nil {
		buf = &runtimeBuffer{Status: "idle"}
		r.runtimeByGroup[sessionKey] = buf
	}
	return buf
}

func (r *Reporter) markDirty() {
	if r == nil {
		return
	}
	select {
	case r.dirtyCh <- struct{}{}:
	default:
	}
}

func (r *Reporter) markDirtyLocked() {
	select {
	case r.dirtyCh <- struct{}{}:
	default:
	}
}

func truncatePreview(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxPreviewLen {
		return s
	}
	return s[:maxPreviewLen] + "..."
}

func appendBounded[T any](items []T, item T, limit int) []T {
	items = append(items, item)
	if len(items) <= limit {
		return items
	}
	return append([]T(nil), items[len(items)-limit:]...)
}
