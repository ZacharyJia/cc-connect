package dashboard

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type ServerConfig struct {
	Listen      string
	Token       string
	InstanceTTL time.Duration
}

type storedInstance struct {
	Report     InstanceReport
	LastSeenAt time.Time
}

type Server struct {
	config    ServerConfig
	mux       *http.ServeMux
	httpSrv   *http.Server
	mu        sync.RWMutex
	instances map[string]storedInstance
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:7390"
	}
	if cfg.InstanceTTL <= 0 {
		cfg.InstanceTTL = 20 * time.Second
	}

	s := &Server{
		config:    cfg,
		mux:       http.NewServeMux(),
		instances: make(map[string]storedInstance),
	}
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/api/report", s.handleReport)
	s.mux.HandleFunc("/api/instances", s.handleInstances)
	s.mux.HandleFunc("/", s.handleIndex)
	s.httpSrv = &http.Server{Addr: cfg.Listen, Handler: s.mux}
	return s
}

func (s *Server) ListenAndServe() error {
	slog.Info("dashboard server started", "addr", s.config.Listen)
	return s.httpSrv.ListenAndServe()
}

func (s *Server) Close() error {
	return s.httpSrv.Close()
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var report InstanceReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(report.InstanceID) == "" {
		http.Error(w, "instance_id is required", http.StatusBadRequest)
		return
	}
	if report.ReportedAt.IsZero() {
		report.ReportedAt = time.Now()
	}

	now := time.Now()
	s.mu.Lock()
	s.instances[report.InstanceID] = storedInstance{
		Report:     report,
		LastSeenAt: now,
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	now := time.Now()
	s.mu.RLock()
	instances := make([]InstanceView, 0, len(s.instances))
	for _, stored := range s.instances {
		instances = append(instances, InstanceView{
			InstanceReport: stored.Report,
			Online:         now.Sub(stored.LastSeenAt) <= s.config.InstanceTTL,
			LastSeenAt:     stored.LastSeenAt,
		})
	}
	s.mu.RUnlock()

	sort.Slice(instances, func(i, j int) bool {
		left := strings.ToLower(instances[i].InstanceID)
		right := strings.ToLower(instances[j].InstanceID)
		if left == right {
			return instances[i].LastSeenAt.After(instances[j].LastSeenAt)
		}
		return left < right
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"instances":      instances,
		"instance_ttl":   s.config.InstanceTTL.String(),
		"server_time":    now,
		"instance_count": len(instances),
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(dashboardHTML))
}

func (s *Server) authorized(r *http.Request) bool {
	if strings.TrimSpace(s.config.Token) == "" {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	return auth == "Bearer "+s.config.Token
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const dashboardHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>CX Connect Dashboard</title>
  <style>
    :root {
      --bg: #f3f0ea;
      --panel: rgba(255,255,255,0.82);
      --ink: #182027;
      --muted: #5c6874;
      --line: rgba(24,32,39,0.11);
      --brand: #0f766e;
      --accent: #b45309;
      --danger: #b91c1c;
      --shadow: 0 24px 60px rgba(21, 32, 43, 0.12);
      --radius: 22px;
    }

    * { box-sizing: border-box; }

    body {
      margin: 0;
      font-family: "IBM Plex Sans", "PingFang SC", "Noto Sans SC", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(circle at top left, rgba(15, 118, 110, 0.16), transparent 24%),
        radial-gradient(circle at right top, rgba(180, 83, 9, 0.18), transparent 24%),
        linear-gradient(180deg, #fbfaf7 0%, var(--bg) 100%);
      min-height: 100vh;
    }

    body::before {
      content: "";
      position: fixed;
      inset: 0;
      pointer-events: none;
      background-image:
        linear-gradient(rgba(24,32,39,0.03) 1px, transparent 1px),
        linear-gradient(90deg, rgba(24,32,39,0.03) 1px, transparent 1px);
      background-size: 24px 24px;
      mask-image: linear-gradient(180deg, rgba(0,0,0,0.9), transparent 92%);
    }

    .shell {
      width: min(1480px, calc(100vw - 32px));
      margin: 24px auto 40px;
      display: grid;
      gap: 18px;
    }

    .hero, .card, .instance {
      background: var(--panel);
      backdrop-filter: blur(18px);
      border: 1px solid rgba(255,255,255,0.7);
      border-radius: var(--radius);
      box-shadow: var(--shadow);
    }

    .hero {
      padding: 24px 26px;
      display: flex;
      gap: 18px;
      justify-content: space-between;
      align-items: end;
    }

    h1 {
      margin: 0;
      font-size: clamp(30px, 4vw, 48px);
      line-height: 0.95;
      letter-spacing: -0.06em;
    }

    .hero p, .muted {
      margin: 8px 0 0;
      color: var(--muted);
    }

    .stats {
      display: grid;
      grid-template-columns: repeat(3, minmax(120px, 1fr));
      gap: 12px;
      min-width: min(460px, 100%);
    }

    .card {
      padding: 16px 18px;
    }

    .card strong {
      display: block;
      font-size: 28px;
      margin-top: 6px;
    }

    .instance-list {
      display: grid;
      gap: 16px;
    }

    .view-tabs {
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
    }

    .tab-button {
      border: 0;
      border-radius: 999px;
      padding: 12px 18px;
      background: rgba(24,32,39,0.08);
      color: var(--ink);
      font: inherit;
      cursor: pointer;
      transition: transform 160ms ease, background 160ms ease, color 160ms ease;
    }

    .tab-button:hover {
      transform: translateY(-1px);
      background: rgba(24,32,39,0.12);
    }

    .tab-button.active {
      background: linear-gradient(135deg, #0f766e 0%, #0b4f6c 100%);
      color: white;
    }

    .view-pane {
      display: none;
    }

    .view-pane.active {
      display: block;
    }

    .instance {
      padding: 18px;
      display: grid;
      gap: 16px;
    }

    .instance-head {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: start;
    }

    .title-row {
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
      align-items: center;
    }

    .instance h2 {
      margin: 0;
      font-size: 24px;
      letter-spacing: -0.04em;
    }

    .pill {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 4px 10px;
      border-radius: 999px;
      font-size: 12px;
      color: var(--muted);
      background: rgba(24,32,39,0.06);
    }

    .pill.online {
      color: var(--brand);
      background: rgba(15,118,110,0.12);
    }

    .pill.offline {
      color: var(--danger);
      background: rgba(185,28,28,0.12);
    }

    .group-list {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
      gap: 14px;
    }

    .group {
      border-radius: 18px;
      border: 1px solid var(--line);
      background: rgba(255,255,255,0.52);
      padding: 14px;
      display: grid;
      gap: 10px;
    }

    .group strong {
      display: block;
      overflow-wrap: anywhere;
    }

    .current-session {
      padding: 12px 14px;
      border-radius: 14px;
      background: rgba(24,32,39,0.04);
      border: 1px solid rgba(24,32,39,0.06);
      display: grid;
      gap: 6px;
      font-size: 13px;
    }

    .current-session.active {
      border-color: rgba(15,118,110,0.26);
      background: rgba(15,118,110,0.08);
    }

    .session-line {
      padding: 10px 12px;
      border-radius: 14px;
      background: rgba(24,32,39,0.04);
      border: 1px solid rgba(24,32,39,0.05);
      font-size: 13px;
    }

    .session-line.active {
      border-color: rgba(15,118,110,0.26);
      background: rgba(15,118,110,0.08);
    }

    .runtime {
      border-top: 1px solid var(--line);
      padding-top: 10px;
      display: grid;
      gap: 10px;
    }

    .group-actions {
      display: flex;
      justify-content: flex-end;
    }

    .ghost-button {
      border: 0;
      border-radius: 999px;
      padding: 9px 14px;
      background: rgba(24,32,39,0.08);
      color: var(--ink);
      font: inherit;
      cursor: pointer;
    }

    .ghost-button:hover {
      background: rgba(24,32,39,0.12);
    }

    dialog.session-dialog {
      width: min(860px, calc(100vw - 24px));
      border: 0;
      border-radius: 24px;
      padding: 0;
      background: rgba(255,255,255,0.96);
      color: var(--ink);
      box-shadow: 0 28px 90px rgba(21, 32, 43, 0.28);
    }

    dialog.session-dialog::backdrop {
      background: rgba(24,32,39,0.32);
      backdrop-filter: blur(4px);
    }

    .dialog-shell {
      padding: 20px;
      display: grid;
      gap: 14px;
    }

    .dialog-head {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: start;
    }

    .dialog-title {
      margin: 0;
      font-size: 24px;
      letter-spacing: -0.04em;
    }

    .dialog-close {
      border: 0;
      border-radius: 999px;
      padding: 8px 12px;
      background: rgba(24,32,39,0.08);
      color: var(--ink);
      font: inherit;
      cursor: pointer;
    }

    .dialog-sessions {
      display: grid;
      gap: 10px;
      max-height: min(68vh, 640px);
      overflow-y: auto;
    }

    .runtime-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      gap: 10px;
    }

    .runtime-box {
      padding: 12px;
      border-radius: 14px;
      background: rgba(24,32,39,0.045);
      min-height: 88px;
    }

    .runtime-box h3 {
      margin: 0 0 8px;
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.14em;
      color: var(--muted);
    }

    .runtime-box pre {
      margin: 0;
      white-space: pre-wrap;
      word-break: break-word;
      font: inherit;
      line-height: 1.5;
    }

    .empty {
      text-align: center;
      padding: 64px 18px;
      color: var(--muted);
      border: 1px dashed var(--line);
      border-radius: var(--radius);
    }

    .scene-shell {
      display: grid;
      gap: 16px;
    }

    .scene-stage {
      position: relative;
      min-height: 760px;
      overflow: hidden;
      border-radius: calc(var(--radius) + 4px);
      background:
        radial-gradient(circle at top, rgba(15, 118, 110, 0.24), transparent 24%),
        radial-gradient(circle at 20% 20%, rgba(180, 83, 9, 0.2), transparent 20%),
        linear-gradient(180deg, #f5f0e6 0%, #ebe2d2 40%, #d8d4c7 100%);
      box-shadow: var(--shadow);
      border: 1px solid rgba(255,255,255,0.65);
    }

    .scene-canvas,
    .scene-labels {
      position: absolute;
      inset: 0;
    }

    .scene-labels {
      pointer-events: none;
      overflow: hidden;
    }

    .scene-hud {
      position: absolute;
      top: 18px;
      left: 18px;
      right: 18px;
      display: grid;
      grid-template-columns: minmax(0, 420px) minmax(0, 360px);
      justify-content: space-between;
      gap: 14px;
      pointer-events: none;
    }

    .hud-card {
      pointer-events: auto;
      background: rgba(255,255,255,0.74);
      backdrop-filter: blur(14px);
      border: 1px solid rgba(255,255,255,0.72);
      border-radius: 18px;
      padding: 14px 16px;
      box-shadow: 0 18px 45px rgba(21, 32, 43, 0.12);
    }

    .hud-card h3 {
      margin: 0 0 8px;
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.16em;
      color: var(--muted);
    }

    .hud-card p {
      margin: 0;
      line-height: 1.55;
      color: var(--ink);
    }

    .scene-legend {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-top: 10px;
    }

    .legend-item {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      padding: 5px 10px;
      border-radius: 999px;
      background: rgba(24,32,39,0.06);
      color: var(--muted);
      font-size: 12px;
    }

    .legend-swatch {
      width: 10px;
      height: 10px;
      border-radius: 999px;
      display: inline-block;
    }

    .worker-bubble,
    .worker-tag {
      font-family: "IBM Plex Sans", "PingFang SC", "Noto Sans SC", sans-serif;
      color: var(--ink);
      transform: translate(-50%, -50%);
    }

    .worker-bubble {
      max-width: 240px;
      padding: 10px 12px;
      border-radius: 16px;
      background: rgba(255,255,255,0.92);
      box-shadow: 0 16px 28px rgba(21, 32, 43, 0.18);
      border: 1px solid rgba(255,255,255,0.9);
      line-height: 1.45;
      font-size: 12px;
      text-align: left;
    }

    .worker-tag {
      padding: 5px 10px;
      border-radius: 999px;
      background: rgba(24,32,39,0.74);
      color: white;
      font-size: 11px;
      box-shadow: 0 10px 22px rgba(21, 32, 43, 0.18);
      white-space: nowrap;
    }

    @media (max-width: 900px) {
      .hero { align-items: start; flex-direction: column; }
      .stats { width: 100%; min-width: 0; }
      .shell { width: min(100vw - 20px, 1480px); margin-top: 14px; }
      .scene-hud { grid-template-columns: 1fr; }
      .scene-stage { min-height: 620px; }
    }
  </style>
  <script type="importmap">
  {
    "imports": {
      "three": "https://cdn.jsdelivr.net/npm/three@0.180.0/build/three.module.js",
      "three/addons/": "https://cdn.jsdelivr.net/npm/three@0.180.0/examples/jsm/"
    }
  }
  </script>
</head>
<body>
  <div class="shell">
    <section class="hero">
      <div>
        <h1>CX Connect Fleet</h1>
        <p>汇总多个实例的活跃 session、agent 事件和外发进度消息。</p>
      </div>
      <div class="stats">
        <div class="card">
          <div class="muted">在线实例</div>
          <strong id="onlineCount">0</strong>
        </div>
        <div class="card">
          <div class="muted">总实例</div>
          <strong id="totalCount">0</strong>
        </div>
        <div class="card">
          <div class="muted">服务时间</div>
          <strong id="serverTime">-</strong>
        </div>
      </div>
    </section>
    <nav class="view-tabs" aria-label="Dashboard Views">
      <button class="tab-button active" type="button" data-view-tab="classic">默认视图</button>
      <button class="tab-button" type="button" data-view-tab="three">3D 视图</button>
    </nav>
    <section id="classicPane" class="view-pane active">
      <section id="instances" class="instance-list"></section>
    </section>
    <section id="threePane" class="view-pane">
      <div class="scene-shell">
        <div class="scene-stage">
          <div id="threeViewport" class="scene-canvas"></div>
          <div id="threeLabels" class="scene-labels"></div>
          <div class="scene-hud">
            <div class="hud-card">
              <h3>3D Overview</h3>
              <p id="sceneSummary">准备中。切换到 3D 视图后，实例会以角色的形式进入仓库房间，空闲实例会回到休息室。</p>
              <div class="scene-legend">
                <span class="legend-item"><span class="legend-swatch" style="background:#0f766e"></span>工作中</span>
                <span class="legend-item"><span class="legend-swatch" style="background:#b45309"></span>等待/阻塞</span>
                <span class="legend-item"><span class="legend-swatch" style="background:#475569"></span>空闲</span>
                <span class="legend-item"><span class="legend-swatch" style="background:#b91c1c"></span>异常/离线</span>
              </div>
            </div>
            <div class="hud-card">
              <h3>操作说明</h3>
              <p>拖动旋转镜头，滚轮缩放。房间会按最近活跃仓库聚合，角色头顶的气泡会显示最近进度。</p>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
  <script>
    const state = { timer: null, openDialogId: null };

    function esc(value) {
      return String(value ?? "")
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;");
    }

    function fmtTime(value) {
      if (!value) return "-";
      return new Date(value).toLocaleString();
    }

    function runtimeBox(title, content) {
      return '<div class="runtime-box"><h3>' + esc(title) + '</h3><pre>' + esc(content || "-") + '</pre></div>';
    }

    function dialogId(instance, group) {
      return "dlg_" + String(instance.instance_id || "") + "__" + String(group.session_key || "").replace(/[^a-zA-Z0-9_-]/g, "_");
    }

    function findCurrentSession(group) {
      const sessions = group.sessions || [];
      return sessions.find((session) => session.active || session.id === group.active_session_id) || sessions[0] || null;
    }

    function renderGroup(instance, group) {
      const runtime = group.runtime || {};
      const current = findCurrentSession(group);
      const sessions = (group.sessions || []).map((session) => {
        const cls = session.active ? "session-line active" : "session-line";
        const busy = session.busy ? "处理中" : "空闲";
        return ''
          + '<div class="' + cls + '">'
          +   '<strong>' + esc(session.name) + ' <span class="pill">' + esc(session.id) + '</span></strong>'
          +   '<div class="muted">workdir: ' + esc(session.work_dir || "-") + '</div>'
          +   '<div class="muted">agent_session: ' + esc(session.agent_session_id || "-") + '</div>'
          +   '<div class="muted">history: ' + esc(session.history_count) + ' · ' + esc(busy) + '</div>'
          + '</div>';
      }).join("");
      const currentBusy = current && current.busy ? "处理中" : "空闲";
      const currentCls = current && (current.active || current.id === group.active_session_id) ? "current-session active" : "current-session";
      const dlgID = dialogId(instance, group);

      const events = (runtime.recent_events || [])
        .slice(-4)
        .map((item) => "[" + item.type + "] " + (item.tool_name ? item.tool_name + " · " : "") + (item.content || "-"))
        .join("\n\n");
      const outbound = (runtime.recent_outbound || [])
        .slice(-4)
        .map((item) => "[" + item.kind + "] " + (item.content || "-"))
        .join("\n\n");

      return ''
        + '<article class="group">'
        +   '<div>'
        +     '<strong>' + esc(group.session_key) + '</strong>'
        +     '<div class="muted">platform: ' + esc(group.platform) + ' · active: ' + esc(group.active_session_id || "-") + ' · interactive: ' + (group.interactive ? "yes" : "no") + '</div>'
        +   '</div>'
        +   (current
              ? '<div class="' + currentCls + '">'
                + '<strong>当前 Session: ' + esc(current.name) + ' <span class="pill">' + esc(current.id) + '</span></strong>'
                + '<div class="muted">workdir: ' + esc(current.work_dir || "-") + '</div>'
                + '<div class="muted">agent_session: ' + esc(current.agent_session_id || "-") + '</div>'
                + '<div class="muted">history: ' + esc(current.history_count) + ' · ' + esc(currentBusy) + '</div>'
                + '</div>'
              : '<div class="muted">暂无 session</div>')
        +   ((group.sessions || []).length
              ? '<div class="group-actions"><button class="ghost-button" type="button" data-dialog-open="' + esc(dlgID) + '">查看全部 Sessions（' + esc((group.sessions || []).length) + '）</button></div>'
              : '')
        +   '<div class="runtime">'
        +     '<div class="muted">运行状态: ' + esc(runtime.status || "idle") + ' · 更新时间: ' + esc(fmtTime(runtime.updated_at)) + '</div>'
        +     '<div class="runtime-grid">'
        +       runtimeBox("最近用户输入", runtime.last_user_message)
        +       runtimeBox("最近助手输出", runtime.last_assistant_message)
        +       runtimeBox("最近 agent 事件", events)
        +       runtimeBox("最近外发消息", outbound)
        +     '</div>'
        +   '</div>'
        +   '<dialog class="session-dialog" id="' + esc(dlgID) + '">'
        +     '<div class="dialog-shell">'
        +       '<div class="dialog-head">'
        +         '<div>'
        +           '<h3 class="dialog-title">Sessions</h3>'
        +           '<div class="muted">' + esc(group.session_key) + ' · ' + esc(group.platform) + '</div>'
        +         '</div>'
        +         '<button class="dialog-close" type="button" data-dialog-close="' + esc(dlgID) + '">关闭</button>'
        +       '</div>'
        +       '<div class="dialog-sessions">' + (sessions || '<div class="muted">暂无 session</div>') + '</div>'
        +     '</div>'
        +   '</dialog>'
        + '</article>';
    }

    function renderInstance(instance) {
      const statusClass = instance.online ? "pill online" : "pill offline";
      const statusLabel = instance.online ? "online" : "offline";
      const groups = (instance.groups || []).map((group) => renderGroup(instance, group)).join("");
      return ''
        + '<section class="instance">'
        +   '<div class="instance-head">'
        +     '<div>'
        +       '<div class="title-row">'
        +         '<h2>' + esc(instance.instance_name || instance.instance_id) + '</h2>'
        +         '<span class="' + statusClass + '">' + statusLabel + '</span>'
        +         '<span class="pill">' + esc(instance.project) + '</span>'
        +         '<span class="pill">' + esc(instance.agent) + '</span>'
        +       '</div>'
        +       '<div class="muted">instance_id: ' + esc(instance.instance_id) + ' · host: ' + esc(instance.hostname) + ' · pid: ' + esc(instance.pid) + '</div>'
        +     '</div>'
        +     '<div class="muted">last seen: ' + esc(fmtTime(instance.last_seen_at)) + '</div>'
        +   '</div>'
        +   '<div class="group-list">' + (groups || '<div class="empty">这个实例还没有上报任何 session。</div>') + '</div>'
        + '</section>';
    }

    function restoreDialog() {
      if (!state.openDialogId) return;
      const dialog = document.getElementById(state.openDialogId);
      if (!dialog || dialog.open) return;
      dialog.showModal();
    }

    async function refresh() {
      const res = await fetch("/api/instances", { cache: "no-store" });
      if (!res.ok) throw new Error("load failed");
      const data = await res.json();
      const instances = data.instances || [];
      const online = instances.filter((item) => item.online).length;

      document.getElementById("onlineCount").textContent = String(online);
      document.getElementById("totalCount").textContent = String(instances.length);
      document.getElementById("serverTime").textContent = fmtTime(data.server_time);
      window.__dashboardData = data;
      window.dispatchEvent(new CustomEvent("dashboard-data", { detail: data }));

      const root = document.getElementById("instances");
      if (!instances.length) {
        root.innerHTML = '<div class="empty">等待实例上报到这个看板。</div>';
        return;
      }
      root.innerHTML = instances.map(renderInstance).join("");
      restoreDialog();
    }

    async function tick() {
      try {
        await refresh();
      } catch (err) {
        console.error(err);
      } finally {
        clearTimeout(state.timer);
        state.timer = setTimeout(tick, 2000);
      }
    }

    tick();

    document.addEventListener("click", (event) => {
      const openID = event.target.closest("[data-dialog-open]")?.getAttribute("data-dialog-open");
      if (openID) {
        const dialog = document.getElementById(openID);
        if (dialog) {
          state.openDialogId = openID;
          dialog.showModal();
        }
        return;
      }

      const closeID = event.target.closest("[data-dialog-close]")?.getAttribute("data-dialog-close");
      if (closeID) {
        const dialog = document.getElementById(closeID);
        if (dialog) {
          dialog.close();
        }
        if (state.openDialogId === closeID) {
          state.openDialogId = null;
        }
      }
    });

    document.addEventListener("close", (event) => {
      if (event.target.matches && event.target.matches("dialog.session-dialog") && state.openDialogId === event.target.id) {
        state.openDialogId = null;
      }
    }, true);
  </script>
  <script type="module">
    import * as THREE from "three";
    import { OrbitControls } from "three/addons/controls/OrbitControls.js";

    const ROOM_LIMIT = 8;
    const viewState = {
      active: "classic",
      app: null
    };

    const roomPalette = [
      { floor: 0x91c7b1, wall: 0xd9f3e8 },
      { floor: 0xf3bf7a, wall: 0xfff1d6 },
      { floor: 0x8db4d9, wall: 0xe4f1ff },
      { floor: 0xd3a6f5, wall: 0xf2e4ff },
      { floor: 0x9fd3c7, wall: 0xe5faf6 },
      { floor: 0xe8a598, wall: 0xffece6 },
      { floor: 0xc4d96f, wall: 0xf4f9d8 },
      { floor: 0x95b4aa, wall: 0xe7f0ed }
    ];

    function findCurrentSession(group) {
      const sessions = group.sessions || [];
      return sessions.find((session) => session.active || session.id === group.active_session_id) || sessions[0] || null;
    }

    function normalizeRepoName(sessionName) {
      const raw = String(sessionName || "").trim();
      if (!raw) return "";
      const match = raw.match(/^(.*)-\d+$/);
      if (match && match[1]) return match[1];
      return raw;
    }

    function asTime(value) {
      const ts = Date.parse(value || "");
      return Number.isFinite(ts) ? ts : 0;
    }

    function pickPrimaryGroup(instance) {
      const groups = (instance.groups || []).slice();
      groups.sort((left, right) => {
        const leftSession = findCurrentSession(left);
        const rightSession = findCurrentSession(right);
        const leftStatus = rankGroup(instance, left, leftSession);
        const rightStatus = rankGroup(instance, right, rightSession);
        if (leftStatus !== rightStatus) return rightStatus - leftStatus;
        const leftTime = Math.max(asTime((left.runtime || {}).updated_at), asTime((leftSession || {}).updated_at));
        const rightTime = Math.max(asTime((right.runtime || {}).updated_at), asTime((rightSession || {}).updated_at));
        return rightTime - leftTime;
      });
      return groups[0] || null;
    }

    function rankGroup(instance, group, currentSession) {
      if (!instance.online) return 0;
      const runtime = group.runtime || {};
      if (runtime.status === "waiting_permission") return 5;
      if ((currentSession && currentSession.busy) || runtime.status === "running") return 4;
      if (runtime.status === "error") return 3;
      if (runtime.status === "completed") return 2;
      return 1;
    }

    function deriveWorkerStatus(instance, group, currentSession) {
      if (!instance.online) return "offline";
      const runtime = group.runtime || {};
      if (runtime.status === "error") return "error";
      if (runtime.status === "waiting_permission") return "blocked";
      if ((currentSession && currentSession.busy) || runtime.status === "running") return "working";
      return "idle";
    }

    function pickBubble(runtime, currentSession, status) {
      const recentEvent = ((runtime || {}).recent_events || []).slice(-1)[0];
      const recentOutbound = ((runtime || {}).recent_outbound || []).slice(-1)[0];
      const parts = [];
      if (status === "idle") return "休息中";
      if (status === "offline") return "实例离线";
      if (status === "error") parts.push("异常处理中");
      if (status === "blocked") parts.push("等待权限");
      if (recentEvent && recentEvent.content) parts.push(recentEvent.content);
      else if (recentOutbound && recentOutbound.content) parts.push(recentOutbound.content);
      else if ((runtime || {}).last_event_text) parts.push(runtime.last_event_text);
      else if ((runtime || {}).last_assistant_message) parts.push(runtime.last_assistant_message);
      else if (currentSession && currentSession.name) parts.push(currentSession.name);
      return parts.join(" · ").slice(0, 160);
    }

    function buildWorldData(payload) {
      const instances = (payload && payload.instances) || [];
      const workers = instances.map((instance) => {
        const group = pickPrimaryGroup(instance);
        const currentSession = group ? findCurrentSession(group) : null;
        const runtime = group ? (group.runtime || {}) : {};
        const status = group ? deriveWorkerStatus(instance, group, currentSession) : (instance.online ? "idle" : "offline");
        const repo = currentSession ? normalizeRepoName(currentSession.name) : "";
        const updatedAt = Math.max(
          asTime(instance.last_seen_at),
          asTime((runtime || {}).updated_at),
          asTime((currentSession || {}).updated_at)
        );
        return {
          instanceId: String(instance.instance_id || ""),
          instanceName: String(instance.instance_name || instance.instance_id || ""),
          project: String(instance.project || ""),
          online: !!instance.online,
          lastSeenAt: instance.last_seen_at || "",
          status: status,
          repo: repo,
          bubble: pickBubble(runtime, currentSession, status),
          currentSession: currentSession,
          group: group,
          updatedAt: updatedAt
        };
      });

      const recentRepos = [];
      const seenRepos = new Set();
      workers
        .filter((worker) => worker.repo)
        .sort((left, right) => right.updatedAt - left.updatedAt)
        .forEach((worker) => {
          if (seenRepos.has(worker.repo)) return;
          seenRepos.add(worker.repo);
          recentRepos.push(worker.repo);
        });

      const selectedRepos = recentRepos.slice(0, ROOM_LIMIT);
      const omittedWorking = workers.some((worker) => worker.status !== "idle" && worker.status !== "offline" && worker.repo && selectedRepos.indexOf(worker.repo) === -1);
      if (omittedWorking) selectedRepos.push("其他仓库");

      return {
        workers: workers,
        repos: selectedRepos
      };
    }

    function createThreeApp() {
      const viewport = document.getElementById("threeViewport");
      const summary = document.getElementById("sceneSummary");

      const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
      renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
      renderer.shadowMap.enabled = true;
      renderer.shadowMap.type = THREE.PCFSoftShadowMap;
      viewport.appendChild(renderer.domElement);

      const scene = new THREE.Scene();
      scene.background = new THREE.Color(0xf0e6d7);
      scene.fog = new THREE.Fog(0xf0e6d7, 30, 70);

      const camera = new THREE.PerspectiveCamera(48, 1, 0.1, 160);
      camera.position.set(0, 18, 26);

      const controls = new OrbitControls(camera, renderer.domElement);
      controls.enableDamping = true;
      controls.target.set(0, 2, 0);
      controls.minDistance = 12;
      controls.maxDistance = 52;
      controls.maxPolarAngle = Math.PI * 0.48;

      const ambient = new THREE.HemisphereLight(0xfff6e8, 0x7c8a8f, 1.15);
      scene.add(ambient);

      const sun = new THREE.DirectionalLight(0xffffff, 1.45);
      sun.position.set(14, 24, 12);
      sun.castShadow = true;
      sun.shadow.mapSize.width = 2048;
      sun.shadow.mapSize.height = 2048;
      sun.shadow.camera.near = 1;
      sun.shadow.camera.far = 80;
      sun.shadow.camera.left = -28;
      sun.shadow.camera.right = 28;
      sun.shadow.camera.top = 28;
      sun.shadow.camera.bottom = -28;
      scene.add(sun);

      const floor = new THREE.Mesh(
        new THREE.CylinderGeometry(26, 28, 0.8, 64),
        new THREE.MeshStandardMaterial({ color: 0xddd2bf, roughness: 0.95, metalness: 0.02 })
      );
      floor.receiveShadow = true;
      floor.position.y = -0.5;
      scene.add(floor);

      const grid = new THREE.GridHelper(56, 28, 0xffffff, 0xcfc2ae);
      grid.position.y = -0.08;
      grid.material.opacity = 0.28;
      grid.material.transparent = true;
      scene.add(grid);

      const loungeLight = new THREE.PointLight(0xffd27f, 2.2, 18, 2);
      loungeLight.position.set(0, 7, 0);
      scene.add(loungeLight);

      const decorativeRings = new THREE.Group();
      for (let i = 0; i < 3; i++) {
        const ring = new THREE.Mesh(
          new THREE.TorusGeometry(7.5 + i * 0.8, 0.05, 12, 64),
          new THREE.MeshBasicMaterial({ color: i === 1 ? 0xb45309 : 0x0f766e, transparent: true, opacity: 0.18 - i * 0.03 })
        );
        ring.rotation.x = Math.PI / 2;
        ring.position.y = 0.05 + i * 0.03;
        decorativeRings.add(ring);
      }
      scene.add(decorativeRings);

      const roomLayer = new THREE.Group();
      const workerLayer = new THREE.Group();
      scene.add(roomLayer);
      scene.add(workerLayer);

      const app = {
        renderer: renderer,
        scene: scene,
        camera: camera,
        controls: controls,
        roomLayer: roomLayer,
        workerLayer: workerLayer,
        summary: summary,
        rooms: new Map(),
        workers: new Map(),
        clock: new THREE.Clock(),
        resize: () => {
          const bounds = viewport.getBoundingClientRect();
          const width = Math.max(bounds.width, 1);
          const height = Math.max(bounds.height, 1);
          camera.aspect = width / height;
          camera.updateProjectionMatrix();
          renderer.setSize(width, height, false);
        },
        render: () => {
          renderer.render(scene, camera);
        }
      };

      app.resize();
      window.addEventListener("resize", app.resize);

      return app;
    }

    function roomLayout(repos) {
      const rooms = [];
      rooms.push({ key: "休息室", title: "休息室", type: "lounge", position: new THREE.Vector3(0, 0, 0), size: { width: 10, depth: 8 } });
      const radius = 18;
      repos.forEach((repo, index) => {
        const angle = (index / Math.max(repos.length, 1)) * Math.PI * 2 - Math.PI / 2;
        rooms.push({
          key: repo,
          title: repo,
          type: repo === "其他仓库" ? "overflow" : "repo",
          position: new THREE.Vector3(Math.cos(angle) * radius, 0, Math.sin(angle) * radius),
          size: { width: 8.5, depth: 6.5 }
        });
      });
      return rooms;
    }

    function roomColors(room, index) {
      return room.type === "lounge"
        ? { floor: 0xc4b08c, wall: 0xf6ebd7 }
        : room.type === "overflow"
          ? { floor: 0xb5b0c8, wall: 0xefecff }
          : roomPalette[index % roomPalette.length];
    }

    function createGroundLabelTexture(title) {
      const canvas = document.createElement("canvas");
      canvas.width = 768;
      canvas.height = 192;
      const ctx = canvas.getContext("2d");
      const texture = new THREE.CanvasTexture(canvas);
      texture.colorSpace = THREE.SRGBColorSpace;
      texture.anisotropy = 8;
      drawGroundLabelTexture(ctx, canvas, texture, title);
      return { canvas, ctx, texture };
    }

    function drawGroundLabelTexture(ctx, canvas, texture, title) {
      ctx.clearRect(0, 0, canvas.width, canvas.height);

      const radius = 64;
      ctx.fillStyle = "rgba(255,255,255,0.96)";
      ctx.strokeStyle = "rgba(24,32,39,0.12)";
      ctx.lineWidth = 6;

      ctx.beginPath();
      ctx.moveTo(radius, 12);
      ctx.lineTo(canvas.width - radius, 12);
      ctx.quadraticCurveTo(canvas.width - 12, 12, canvas.width - 12, radius);
      ctx.lineTo(canvas.width - 12, canvas.height - radius);
      ctx.quadraticCurveTo(canvas.width - 12, canvas.height - 12, canvas.width - radius, canvas.height - 12);
      ctx.lineTo(radius, canvas.height - 12);
      ctx.quadraticCurveTo(12, canvas.height - 12, 12, canvas.height - radius);
      ctx.lineTo(12, radius);
      ctx.quadraticCurveTo(12, 12, radius, 12);
      ctx.closePath();
      ctx.fill();
      ctx.stroke();

      ctx.fillStyle = "#182027";
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      ctx.font = "700 72px IBM Plex Sans, PingFang SC, Noto Sans SC, sans-serif";
      ctx.fillText(title, canvas.width / 2, canvas.height / 2);

      texture.needsUpdate = true;
    }

    function createBillboardTexture(width, height, draw) {
      const canvas = document.createElement("canvas");
      canvas.width = width;
      canvas.height = height;
      const ctx = canvas.getContext("2d");
      const texture = new THREE.CanvasTexture(canvas);
      texture.colorSpace = THREE.SRGBColorSpace;
      texture.anisotropy = 8;
      draw(ctx, canvas, texture);
      return { canvas, ctx, texture };
    }

    function drawRoundedPanel(ctx, x, y, width, height, radius, fill, stroke, lineWidth) {
      ctx.beginPath();
      ctx.moveTo(x + radius, y);
      ctx.lineTo(x + width - radius, y);
      ctx.quadraticCurveTo(x + width, y, x + width, y + radius);
      ctx.lineTo(x + width, y + height - radius);
      ctx.quadraticCurveTo(x + width, y + height, x + width - radius, y + height);
      ctx.lineTo(x + radius, y + height);
      ctx.quadraticCurveTo(x, y + height, x, y + height - radius);
      ctx.lineTo(x, y + radius);
      ctx.quadraticCurveTo(x, y, x + radius, y);
      ctx.closePath();
      ctx.fillStyle = fill;
      ctx.fill();
      if (stroke) {
        ctx.strokeStyle = stroke;
        ctx.lineWidth = lineWidth || 1;
        ctx.stroke();
      }
    }

    function wrapText(ctx, text, maxWidth) {
      const words = String(text || "").split(/\s+/).filter(Boolean);
      if (!words.length) return [""];
      const lines = [];
      let line = words[0];
      for (let i = 1; i < words.length; i++) {
        const next = line + " " + words[i];
        if (ctx.measureText(next).width <= maxWidth) {
          line = next;
        } else {
          lines.push(line);
          line = words[i];
        }
      }
      lines.push(line);
      return lines;
    }

    function drawTagTexture(ctx, canvas, texture, text) {
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      drawRoundedPanel(ctx, 8, 8, canvas.width - 16, canvas.height - 16, 46, "rgba(24,32,39,0.86)", "rgba(255,255,255,0.22)", 4);
      ctx.fillStyle = "#ffffff";
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      ctx.font = "700 46px IBM Plex Sans, PingFang SC, Noto Sans SC, sans-serif";
      ctx.fillText(String(text || ""), canvas.width / 2, canvas.height / 2 + 1);
      texture.needsUpdate = true;
    }

    function drawBubbleTexture(ctx, canvas, texture, text) {
      ctx.clearRect(0, 0, canvas.width, canvas.height);

      const tailHeight = 28;
      drawRoundedPanel(ctx, 12, 12, canvas.width - 24, canvas.height - 24 - tailHeight, 36, "rgba(255,255,255,0.96)", "rgba(24,32,39,0.12)", 4);
      ctx.beginPath();
      ctx.moveTo(canvas.width / 2 - 24, canvas.height - 24 - tailHeight);
      ctx.lineTo(canvas.width / 2, canvas.height - 8);
      ctx.lineTo(canvas.width / 2 + 24, canvas.height - 24 - tailHeight);
      ctx.closePath();
      ctx.fillStyle = "rgba(255,255,255,0.96)";
      ctx.fill();
      ctx.strokeStyle = "rgba(24,32,39,0.12)";
      ctx.lineWidth = 4;
      ctx.stroke();

      ctx.fillStyle = "#182027";
      ctx.textAlign = "center";
      ctx.textBaseline = "top";
      ctx.font = "600 34px IBM Plex Sans, PingFang SC, Noto Sans SC, sans-serif";

      const lines = wrapText(ctx, String(text || ""), canvas.width - 72).slice(0, 4);
      const lineHeight = 44;
      const startY = 38;
      lines.forEach((line, index) => {
        ctx.fillText(line, canvas.width / 2, startY + index * lineHeight);
      });

      texture.needsUpdate = true;
    }

    function createTextSprite(width, height, worldWidth, worldHeight, draw, renderOrder) {
      const tex = createBillboardTexture(width, height, draw);
      const material = new THREE.SpriteMaterial({
        map: tex.texture,
        transparent: true,
        depthWrite: false,
        sizeAttenuation: true
      });
      const sprite = new THREE.Sprite(material);
      sprite.scale.set(worldWidth, worldHeight, 1);
      sprite.renderOrder = renderOrder || 0;
      return {
        sprite,
        material,
        texture: tex.texture,
        canvas: tex.canvas,
        ctx: tex.ctx
      };
    }

    function createRoomRecord(room, index) {
      const colors = roomColors(room, index);
      const wallHeight = room.type === "lounge" ? 2.4 : 2.1;
      const wallThickness = 0.28;

      const group = new THREE.Group();

      const floorMaterial = new THREE.MeshStandardMaterial({ color: colors.floor, roughness: 0.86, metalness: 0.04 });
      const floor = new THREE.Mesh(new THREE.BoxGeometry(room.size.width, 0.6, room.size.depth), floorMaterial);
      floor.receiveShadow = true;
      floor.position.y = -0.2;
      group.add(floor);

      const wallMaterial = new THREE.MeshStandardMaterial({ color: colors.wall, roughness: 0.74, metalness: 0.02 });
      const leftWall = new THREE.Mesh(new THREE.BoxGeometry(wallThickness, wallHeight, room.size.depth), wallMaterial);
      leftWall.castShadow = true;
      group.add(leftWall);

      const rightWall = leftWall.clone();
      group.add(rightWall);

      const backWall = new THREE.Mesh(new THREE.BoxGeometry(room.size.width, wallHeight, wallThickness), wallMaterial);
      backWall.castShadow = true;
      group.add(backWall);

      const labelData = createGroundLabelTexture(room.title);
      const labelMaterial = new THREE.MeshBasicMaterial({
        map: labelData.texture,
        transparent: true,
        depthWrite: false,
        polygonOffset: true,
        polygonOffsetFactor: -2,
        polygonOffsetUnits: -2
      });
      const labelPlane = new THREE.Mesh(new THREE.PlaneGeometry(Math.min(room.size.width * 0.72, 6.2), 1.6), labelMaterial);
      labelPlane.rotation.x = -Math.PI / 2;
      labelPlane.position.y = 0.11;
      group.add(labelPlane);

      return {
        key: room.key,
        title: room.title,
        type: room.type,
        size: room.size,
        position: room.position.clone(),
        wallHeight: wallHeight,
        group: group,
        floor: floor,
        floorMaterial: floorMaterial,
        wallMaterial: wallMaterial,
        leftWall: leftWall,
        rightWall: rightWall,
        backWall: backWall,
        labelPlane: labelPlane,
        labelMaterial: labelMaterial,
        labelTexture: labelData.texture,
        labelCanvas: labelData.canvas,
        labelCtx: labelData.ctx
      };
    }

    function updateRoomRecord(record, room, index) {
      const colors = roomColors(room, index);
      const wallHeight = room.type === "lounge" ? 2.4 : 2.1;
      const wallThickness = 0.28;

      record.key = room.key;
      record.type = room.type;
      record.size = room.size;
      record.wallHeight = wallHeight;
      record.group.position.copy(room.position);
      record.position = room.position.clone();

      record.floor.material.color.setHex(colors.floor);
      record.floor.position.y = -0.2;

      record.leftWall.position.set(-room.size.width / 2, wallHeight / 2 - 0.1, 0);
      record.rightWall.position.set(room.size.width / 2, wallHeight / 2 - 0.1, 0);
      record.backWall.position.set(0, wallHeight / 2 - 0.1, -room.size.depth / 2);
      record.wallMaterial.color.setHex(colors.wall);

      if (record.title !== room.title) {
        record.title = room.title;
        drawGroundLabelTexture(record.labelCtx, record.labelCanvas, record.labelTexture, room.title);
      }

      record.labelPlane.position.set(0, 0.11, room.size.depth * 0.18);
      record.labelPlane.scale.set(1, 1, 1);
      record.labelPlane.material.map = record.labelTexture;

      const desiredWidth = Math.min(room.size.width * 0.72, 6.2);
      const desiredHeight = 1.6;
      record.labelPlane.geometry.dispose();
      record.labelPlane.geometry = new THREE.PlaneGeometry(desiredWidth, desiredHeight);
      record.labelPlane.rotation.x = -Math.PI / 2;

      if (record.leftWall.geometry.parameters.depth !== room.size.depth || record.leftWall.geometry.parameters.height !== wallHeight) {
        record.leftWall.geometry.dispose();
        record.leftWall.geometry = new THREE.BoxGeometry(wallThickness, wallHeight, room.size.depth);
      }
      if (record.rightWall.geometry.parameters.depth !== room.size.depth || record.rightWall.geometry.parameters.height !== wallHeight) {
        record.rightWall.geometry.dispose();
        record.rightWall.geometry = new THREE.BoxGeometry(wallThickness, wallHeight, room.size.depth);
      }
      if (record.backWall.geometry.parameters.width !== room.size.width || record.backWall.geometry.parameters.height !== wallHeight) {
        record.backWall.geometry.dispose();
        record.backWall.geometry = new THREE.BoxGeometry(room.size.width, wallHeight, wallThickness);
      }
      if (record.floor.geometry.parameters.width !== room.size.width || record.floor.geometry.parameters.depth !== room.size.depth) {
        record.floor.geometry.dispose();
        record.floor.geometry = new THREE.BoxGeometry(room.size.width, 0.6, room.size.depth);
      }
    }

    function disposeRoomRecord(record) {
      if (!record) return;
      if (record.floor && record.floor.geometry) record.floor.geometry.dispose();
      if (record.leftWall && record.leftWall.geometry) record.leftWall.geometry.dispose();
      if (record.rightWall && record.rightWall.geometry) record.rightWall.geometry.dispose();
      if (record.backWall && record.backWall.geometry) record.backWall.geometry.dispose();
      if (record.labelPlane && record.labelPlane.geometry) record.labelPlane.geometry.dispose();
      if (record.floorMaterial) record.floorMaterial.dispose();
      if (record.wallMaterial) record.wallMaterial.dispose();
      if (record.labelMaterial) record.labelMaterial.dispose();
      if (record.labelTexture) record.labelTexture.dispose();
      if (record.group && record.group.parent) {
        record.group.parent.remove(record.group);
      }
    }

    function rebuildRooms(app, world) {
      const layout = roomLayout(world.repos);
      const nextKeys = new Set(layout.map((room) => room.key));

      Array.from(app.rooms.entries()).forEach(([key, record]) => {
        if (nextKeys.has(key)) return;
        disposeRoomRecord(record);
        app.rooms.delete(key);
      });

      layout.forEach((room, index) => {
        let record = app.rooms.get(room.key);
        if (!record) {
          record = createRoomRecord(room, index);
          app.rooms.set(room.key, record);
          app.roomLayer.add(record.group);
        }
        updateRoomRecord(record, room, index);
      });
    }

    function workerColors(status) {
      if (status === "working") return { body: 0x0f766e, accent: 0xf5f5f4 };
      if (status === "blocked") return { body: 0xb45309, accent: 0xfffbeb };
      if (status === "error") return { body: 0xb91c1c, accent: 0xfff1f2 };
      if (status === "offline") return { body: 0x64748b, accent: 0xe2e8f0 };
      return { body: 0x475569, accent: 0xf8fafc };
    }

    function createWorkerNode(worker) {
      const root = new THREE.Group();
      root.position.set((Math.random() - 0.5) * 4, 0, (Math.random() - 0.5) * 4);

      const bodyShell = new THREE.Group();
      root.add(bodyShell);

      const bodyMaterial = new THREE.MeshStandardMaterial({ color: 0x0f766e, roughness: 0.62, metalness: 0.08 });
      const accentMaterial = new THREE.MeshStandardMaterial({ color: 0xf5f5f4, roughness: 0.7, metalness: 0.04 });

      const torso = new THREE.Mesh(new THREE.CapsuleGeometry(0.42, 1.05, 6, 12), bodyMaterial);
      torso.position.y = 1.7;
      torso.castShadow = true;
      bodyShell.add(torso);

      const head = new THREE.Mesh(new THREE.SphereGeometry(0.42, 24, 24), accentMaterial);
      head.position.y = 3.05;
      head.castShadow = true;
      bodyShell.add(head);

      const eyeMaterial = new THREE.MeshStandardMaterial({ color: 0x1f2937, roughness: 0.4, metalness: 0.02 });
      const eyeLeft = new THREE.Mesh(new THREE.SphereGeometry(0.045, 12, 12), eyeMaterial);
      eyeLeft.position.set(-0.12, 3.1, 0.37);
      const eyeRight = eyeLeft.clone();
      eyeRight.position.x = 0.12;
      bodyShell.add(eyeLeft);
      bodyShell.add(eyeRight);

      const armGeo = new THREE.CapsuleGeometry(0.12, 0.7, 4, 10);
      const legGeo = new THREE.CapsuleGeometry(0.14, 0.9, 4, 10);

      const leftArm = new THREE.Mesh(armGeo, bodyMaterial);
      leftArm.position.set(-0.58, 1.92, 0);
      leftArm.rotation.z = 0.24;
      leftArm.castShadow = true;
      bodyShell.add(leftArm);

      const rightArm = leftArm.clone();
      rightArm.position.x = 0.58;
      rightArm.rotation.z = -0.24;
      bodyShell.add(rightArm);

      const leftLeg = new THREE.Mesh(legGeo, bodyMaterial);
      leftLeg.position.set(-0.22, 0.72, 0);
      leftLeg.castShadow = true;
      bodyShell.add(leftLeg);

      const rightLeg = leftLeg.clone();
      rightLeg.position.x = 0.22;
      bodyShell.add(rightLeg);

      const shadow = new THREE.Mesh(
        new THREE.CircleGeometry(0.72, 24),
        new THREE.MeshBasicMaterial({ color: 0x000000, transparent: true, opacity: 0.12 })
      );
      shadow.rotation.x = -Math.PI / 2;
      shadow.position.y = 0.01;
      root.add(shadow);

      const bubble = createTextSprite(640, 320, 3.9, 1.95, (ctx, canvas, texture) => {
        drawBubbleTexture(ctx, canvas, texture, worker.bubble || "");
      }, 12);
      bubble.sprite.position.set(0, 4.65, 0);
      bubble.sprite.visible = !!worker.bubble;
      root.add(bubble.sprite);

      const tag = createTextSprite(384, 96, 2.65, 0.68, (ctx, canvas, texture) => {
        drawTagTexture(ctx, canvas, texture, worker.instanceName);
      }, 11);
      tag.sprite.position.set(0, 3.9, 0);
      root.add(tag.sprite);

      return {
        id: worker.instanceId,
        root: root,
        bodyShell: bodyShell,
        torsoMaterial: bodyMaterial,
        accentMaterial: accentMaterial,
        bubbleSprite: bubble.sprite,
        bubbleMaterial: bubble.material,
        bubbleTexture: bubble.texture,
        bubbleCanvas: bubble.canvas,
        bubbleCtx: bubble.ctx,
        tagSprite: tag.sprite,
        tagMaterial: tag.material,
        tagTexture: tag.texture,
        tagCanvas: tag.canvas,
        tagCtx: tag.ctx,
        bubbleText: worker.bubble || "",
        tagText: worker.instanceName,
        arms: [leftArm, rightArm],
        legs: [leftLeg, rightLeg],
        phase: Math.random() * Math.PI * 2,
        target: root.position.clone(),
        info: worker
      };
    }

    function assignRoomTargets(app, world) {
      const occupants = new Map();
      world.workers.forEach((worker) => {
        let roomKey = "休息室";
        if (worker.status !== "idle" && worker.status !== "offline" && worker.repo) {
          roomKey = app.rooms.has(worker.repo) ? worker.repo : (app.rooms.has("其他仓库") ? "其他仓库" : "休息室");
        }
        if (!occupants.has(roomKey)) occupants.set(roomKey, []);
        occupants.get(roomKey).push(worker);
      });

      occupants.forEach((workers, roomKey) => {
        const room = app.rooms.get(roomKey) || app.rooms.get("休息室");
        workers.forEach((worker, index) => {
          const cols = Math.max(1, Math.ceil(Math.sqrt(workers.length)));
          const row = Math.floor(index / cols);
          const col = index % cols;
          const spacingX = room.size.width / (cols + 1);
          const rows = Math.max(1, Math.ceil(workers.length / cols));
          const spacingZ = room.size.depth / (rows + 1);
          const x = -room.size.width / 2 + spacingX * (col + 1);
          const z = -room.size.depth / 2 + spacingZ * (row + 1) + 0.9;
          worker.roomKey = roomKey;
          worker.target = new THREE.Vector3(room.position.x + x, 0, room.position.z + z);
        });
      });
    }

    function syncWorkers(app, world) {
      assignRoomTargets(app, world);
      const activeIds = new Set();

      world.workers.forEach((worker) => {
        activeIds.add(worker.instanceId);
        let node = app.workers.get(worker.instanceId);
        if (!node) {
          node = createWorkerNode(worker);
          app.workers.set(worker.instanceId, node);
          app.workerLayer.add(node.root);
        }

        node.info = worker;
        node.target.copy(worker.target);
        const colors = workerColors(worker.status);
        node.torsoMaterial.color.setHex(colors.body);
        node.accentMaterial.color.setHex(colors.accent);
        if (node.tagText !== worker.instanceName) {
          node.tagText = worker.instanceName;
          drawTagTexture(node.tagCtx, node.tagCanvas, node.tagTexture, worker.instanceName);
        }
        const bubbleText = worker.bubble || "";
        if (node.bubbleText !== bubbleText) {
          node.bubbleText = bubbleText;
          drawBubbleTexture(node.bubbleCtx, node.bubbleCanvas, node.bubbleTexture, bubbleText);
        }
        node.bubbleSprite.visible = !!bubbleText;
      });

      Array.from(app.workers.keys()).forEach((id) => {
        if (activeIds.has(id)) return;
        const node = app.workers.get(id);
        if (!node) return;
        if (node.bubbleTexture) node.bubbleTexture.dispose();
        if (node.bubbleMaterial) node.bubbleMaterial.dispose();
        if (node.tagTexture) node.tagTexture.dispose();
        if (node.tagMaterial) node.tagMaterial.dispose();
        app.workerLayer.remove(node.root);
        app.workers.delete(id);
      });
    }

    function updateSummary(app, world) {
      const total = world.workers.length;
      const working = world.workers.filter((worker) => worker.status === "working").length;
      const blocked = world.workers.filter((worker) => worker.status === "blocked").length;
      const idle = world.workers.filter((worker) => worker.status === "idle").length;
      const offline = world.workers.filter((worker) => worker.status === "offline").length;
      const repos = world.repos.filter((repo) => repo !== "其他仓库").slice(0, ROOM_LIMIT).join("、") || "暂无活跃仓库";
      app.summary.textContent = "当前共有 " + total + " 个实例，其中 " + working + " 个在工作，" + blocked + " 个在等待，" + idle + " 个在休息，" + offline + " 个离线。房间按最近仓库聚合：" + repos + "。";
    }

    function applyWorldData(payload) {
      if (!viewState.app) return;
      const world = buildWorldData(payload);
      rebuildRooms(viewState.app, world);
      syncWorkers(viewState.app, world);
      updateSummary(viewState.app, world);
    }

    function animate(app) {
      const delta = Math.min(app.clock.getDelta(), 0.05);
      const elapsed = app.clock.elapsedTime;

      app.controls.update();

      app.workers.forEach((worker) => {
        const current = worker.root.position;
        const target = worker.target;
        const offset = new THREE.Vector3().subVectors(target, current);
        const distance = offset.length();
        const moving = distance > 0.06;
        if (moving) {
          const step = Math.min(distance, delta * 4.8);
          current.add(offset.normalize().multiplyScalar(step));
          const desiredY = Math.atan2(target.x - current.x, target.z - current.z);
          worker.root.rotation.y += (desiredY - worker.root.rotation.y) * 0.12;
        }

        const bob = moving ? Math.sin(elapsed * 10 + worker.phase) * 0.08 : Math.sin(elapsed * 2 + worker.phase) * 0.03;
        worker.bodyShell.position.y = bob;
        worker.arms[0].rotation.x = moving ? Math.sin(elapsed * 10 + worker.phase) * 0.65 : Math.sin(elapsed * 2 + worker.phase) * 0.08;
        worker.arms[1].rotation.x = moving ? -Math.sin(elapsed * 10 + worker.phase) * 0.65 : -Math.sin(elapsed * 2 + worker.phase) * 0.08;
        worker.legs[0].rotation.x = moving ? -Math.sin(elapsed * 10 + worker.phase) * 0.48 : 0;
        worker.legs[1].rotation.x = moving ? Math.sin(elapsed * 10 + worker.phase) * 0.48 : 0;
        worker.bubbleSprite.position.y = 4.52 + (moving ? 0.04 : 0.08) * Math.sin(elapsed * 2 + worker.phase);
        worker.tagSprite.position.y = 3.9 + (moving ? 0.02 : 0.04) * Math.sin(elapsed * 2 + worker.phase + 0.8);
      });

      app.render();
      requestAnimationFrame(() => animate(app));
    }

    function ensureThreeView() {
      if (viewState.app) {
        viewState.app.resize();
        viewState.app.render();
        return;
      }
      viewState.app = createThreeApp();
      animate(viewState.app);
      if (window.__dashboardData) applyWorldData(window.__dashboardData);
    }

    function setActiveView(view) {
      viewState.active = view;
      document.querySelectorAll("[data-view-tab]").forEach((button) => {
        button.classList.toggle("active", button.getAttribute("data-view-tab") === view);
      });
      document.getElementById("classicPane").classList.toggle("active", view === "classic");
      document.getElementById("threePane").classList.toggle("active", view === "three");
      if (view === "three") {
        ensureThreeView();
      }
    }

    document.querySelectorAll("[data-view-tab]").forEach((button) => {
      button.addEventListener("click", () => {
        setActiveView(button.getAttribute("data-view-tab"));
      });
    });

    window.addEventListener("dashboard-data", (event) => {
      if (!event || !event.detail) return;
      if (viewState.app) applyWorldData(event.detail);
    });
  </script>
</body>
</html>`
