package dashboard

import "time"

type SessionReport struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	WorkDir        string    `json:"work_dir"`
	AgentSessionID string    `json:"agent_session_id"`
	HistoryCount   int       `json:"history_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Busy           bool      `json:"busy"`
	Active         bool      `json:"active"`
}

type RuntimeEvent struct {
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	ToolName  string    `json:"tool_name,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type OutboundMessage struct {
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type RuntimeState struct {
	Status               string            `json:"status"`
	LastUserMessage      string            `json:"last_user_message,omitempty"`
	LastAssistantMessage string            `json:"last_assistant_message,omitempty"`
	LastEventType        string            `json:"last_event_type,omitempty"`
	LastEventText        string            `json:"last_event_text,omitempty"`
	UpdatedAt            time.Time         `json:"updated_at"`
	RecentEvents         []RuntimeEvent    `json:"recent_events,omitempty"`
	RecentOutbound       []OutboundMessage `json:"recent_outbound,omitempty"`
}

type SessionGroupReport struct {
	SessionKey      string          `json:"session_key"`
	Platform        string          `json:"platform"`
	ActiveSessionID string          `json:"active_session_id"`
	Interactive     bool            `json:"interactive"`
	Sessions        []SessionReport `json:"sessions"`
	Runtime         RuntimeState    `json:"runtime"`
}

type InstanceReport struct {
	InstanceID   string               `json:"instance_id"`
	InstanceName string               `json:"instance_name"`
	Project      string               `json:"project"`
	Agent        string               `json:"agent"`
	Version      string               `json:"version"`
	Hostname     string               `json:"hostname"`
	PID          int                  `json:"pid"`
	StartedAt    time.Time            `json:"started_at"`
	ReportedAt   time.Time            `json:"reported_at"`
	Groups       []SessionGroupReport `json:"groups"`
}

type InstanceView struct {
	InstanceReport
	Online     bool      `json:"online"`
	LastSeenAt time.Time `json:"last_seen_at"`
}
