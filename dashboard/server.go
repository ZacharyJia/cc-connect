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
		if instances[i].Online != instances[j].Online {
			return instances[i].Online
		}
		return instances[i].LastSeenAt.After(instances[j].LastSeenAt)
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

    @media (max-width: 900px) {
      .hero { align-items: start; flex-direction: column; }
      .stats { width: 100%; min-width: 0; }
      .shell { width: min(100vw - 20px, 1480px); margin-top: 14px; }
    }
  </style>
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
    <section id="instances" class="instance-list"></section>
  </div>
  <script>
    const state = { timer: null };

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

    function renderGroup(group) {
      const runtime = group.runtime || {};
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
        +   '<div>' + (sessions || '<div class="muted">暂无 session</div>') + '</div>'
        +   '<div class="runtime">'
        +     '<div class="muted">运行状态: ' + esc(runtime.status || "idle") + ' · 更新时间: ' + esc(fmtTime(runtime.updated_at)) + '</div>'
        +     '<div class="runtime-grid">'
        +       runtimeBox("最近用户输入", runtime.last_user_message)
        +       runtimeBox("最近助手输出", runtime.last_assistant_message)
        +       runtimeBox("最近 agent 事件", events)
        +       runtimeBox("最近外发消息", outbound)
        +     '</div>'
        +   '</div>'
        + '</article>';
    }

    function renderInstance(instance) {
      const statusClass = instance.online ? "pill online" : "pill offline";
      const statusLabel = instance.online ? "online" : "offline";
      const groups = (instance.groups || []).map(renderGroup).join("");
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

    async function refresh() {
      const res = await fetch("/api/instances", { cache: "no-store" });
      if (!res.ok) throw new Error("load failed");
      const data = await res.json();
      const instances = data.instances || [];
      const online = instances.filter((item) => item.online).length;

      document.getElementById("onlineCount").textContent = String(online);
      document.getElementById("totalCount").textContent = String(instances.length);
      document.getElementById("serverTime").textContent = fmtTime(data.server_time);

      const root = document.getElementById("instances");
      if (!instances.length) {
        root.innerHTML = '<div class="empty">等待实例上报到这个看板。</div>';
        return;
      }
      root.innerHTML = instances.map(renderInstance).join("");
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
  </script>
</body>
</html>`
