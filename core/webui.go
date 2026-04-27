package core

const adminWebUIHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>cx-connect 会话管理</title>
  <style>
    :root {
      --bg: #f4efe6;
      --panel: rgba(255, 252, 247, 0.88);
      --panel-strong: rgba(255, 252, 247, 0.96);
      --ink: #1b1d21;
      --muted: #5e6472;
      --line: rgba(27, 29, 33, 0.12);
      --brand: #c8553d;
      --brand-deep: #7f2f1d;
      --ok: #287271;
      --warn: #9a3412;
      --shadow: 0 20px 60px rgba(69, 51, 31, 0.14);
      --radius: 22px;
    }

    * { box-sizing: border-box; }

    body {
      margin: 0;
      min-height: 100vh;
      font-family: "IBM Plex Sans", "PingFang SC", "Noto Sans SC", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(circle at top left, rgba(200, 85, 61, 0.18), transparent 28%),
        radial-gradient(circle at right 20%, rgba(40, 114, 113, 0.18), transparent 24%),
        linear-gradient(180deg, #fbf6ee 0%, #f2ebdf 100%);
    }

    body::before {
      content: "";
      position: fixed;
      inset: 0;
      pointer-events: none;
      background-image:
        linear-gradient(rgba(27, 29, 33, 0.035) 1px, transparent 1px),
        linear-gradient(90deg, rgba(27, 29, 33, 0.035) 1px, transparent 1px);
      background-size: 28px 28px;
      mask-image: linear-gradient(180deg, rgba(0,0,0,0.75), transparent 85%);
    }

    .shell {
      width: min(1440px, calc(100vw - 32px));
      margin: 24px auto;
      display: grid;
      gap: 18px;
    }

    .hero, .panel {
      background: var(--panel);
      border: 1px solid rgba(255,255,255,0.7);
      border-radius: var(--radius);
      box-shadow: var(--shadow);
      backdrop-filter: blur(16px);
    }

    .hero {
      padding: 22px 24px;
      display: flex;
      justify-content: space-between;
      gap: 16px;
      align-items: end;
    }

    .hero h1 {
      margin: 0;
      font-size: clamp(28px, 4vw, 44px);
      line-height: 0.95;
      letter-spacing: -0.05em;
      font-weight: 700;
    }

    .hero p {
      margin: 10px 0 0;
      color: var(--muted);
      max-width: 760px;
    }

    .toolbar {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 10px;
      justify-content: flex-end;
    }

    .layout {
      display: grid;
      grid-template-columns: minmax(340px, 420px) minmax(0, 1fr);
      gap: 18px;
    }

    .panel { overflow: hidden; }

    .panel-header {
      padding: 18px 20px 0;
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
    }

    .panel-body {
      padding: 18px 20px 20px;
      display: grid;
      gap: 18px;
    }

    h2 {
      margin: 0;
      font-size: 15px;
      text-transform: uppercase;
      letter-spacing: 0.18em;
      color: var(--muted);
    }

    label {
      display: flex;
      flex-direction: column;
      gap: 6px;
      font-size: 12px;
      color: var(--muted);
      letter-spacing: 0.04em;
      text-transform: uppercase;
    }

    input, textarea, button {
      font: inherit;
    }

    input, textarea {
      width: 100%;
      border-radius: 14px;
      border: 1px solid var(--line);
      background: var(--panel-strong);
      color: var(--ink);
      padding: 12px 14px;
      outline: none;
    }

    textarea {
      min-height: 140px;
      resize: vertical;
      line-height: 1.5;
    }

    button, .button-link {
      border: 0;
      border-radius: 999px;
      padding: 11px 16px;
      background: var(--ink);
      color: white;
      cursor: pointer;
      transition: transform 120ms ease, opacity 120ms ease;
    }

    button.secondary, .button-link.secondary {
      background: rgba(27, 29, 33, 0.08);
      color: var(--ink);
    }

    a.button-link {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      text-decoration: none;
    }

    button.accent, .button-link.accent {
      background: linear-gradient(135deg, var(--brand) 0%, var(--brand-deep) 100%);
    }

    button:hover, .button-link:hover { transform: translateY(-1px); }
    button:disabled { cursor: wait; opacity: 0.65; transform: none; }

    .session-groups {
      display: grid;
      gap: 12px;
      max-height: calc(100vh - 235px);
      min-height: 0;
      overflow-x: hidden;
      overflow-y: auto;
      padding-right: 4px;
    }

    .group {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      border: 1px solid var(--line);
      border-radius: 18px;
      background: rgba(255,255,255,0.42);
      overflow: hidden;
    }

    .group-head {
      padding: 14px 16px;
      border-bottom: 1px solid rgba(27, 29, 33, 0.07);
      background: rgba(255,255,255,0.35);
    }

    .group-head strong {
      display: block;
      font-size: 14px;
      overflow-wrap: anywhere;
    }

    .group-meta {
      margin-top: 8px;
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
    }

    .group-sessions {
      display: grid;
      min-height: 0;
      max-height: min(52vh, 420px);
      overflow-x: hidden;
      overflow-y: auto;
    }

    .badge {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 4px 10px;
      border-radius: 999px;
      font-size: 12px;
      background: rgba(27, 29, 33, 0.06);
      color: var(--muted);
    }

    .badge.ok {
      background: rgba(40, 114, 113, 0.12);
      color: var(--ok);
    }

    .badge.warn {
      background: rgba(154, 52, 18, 0.12);
      color: var(--warn);
    }

    .session-item {
      width: 100%;
      border: 0;
      background: transparent;
      color: inherit;
      text-align: left;
      border-radius: 0;
      padding: 14px 16px;
      display: grid;
      gap: 6px;
      border-top: 1px solid rgba(27, 29, 33, 0.06);
    }

    .session-item:hover, .session-item.active {
      background: rgba(200, 85, 61, 0.12);
    }

    .session-title {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: baseline;
    }

    .session-title strong { font-size: 15px; }
    .session-title span, .session-sub {
      font-size: 12px;
      color: var(--muted);
      overflow-wrap: anywhere;
    }

    .detail-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 12px;
    }

    .detail-card, .section-card {
      padding: 14px;
      border-radius: 18px;
      border: 1px solid var(--line);
      background: rgba(255,255,255,0.5);
    }

    .detail-card small, .section-card small {
      display: block;
      color: var(--muted);
      text-transform: uppercase;
      letter-spacing: 0.08em;
      margin-bottom: 8px;
    }

    .detail-card strong, .detail-card span {
      display: block;
      overflow-wrap: anywhere;
    }

    .form-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 12px;
    }

    .history {
      display: grid;
      gap: 12px;
      max-height: 420px;
      overflow: auto;
      padding-right: 4px;
    }

    .entry {
      padding: 14px;
      border-radius: 18px;
      border: 1px solid var(--line);
      background: rgba(255,255,255,0.5);
    }

    .entry.user {
      border-color: rgba(200, 85, 61, 0.2);
      background: rgba(200, 85, 61, 0.08);
    }

    .entry.assistant {
      border-color: rgba(40, 114, 113, 0.18);
      background: rgba(40, 114, 113, 0.07);
    }

    .entry-head {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      margin-bottom: 8px;
      font-size: 12px;
      color: var(--muted);
      text-transform: uppercase;
      letter-spacing: 0.08em;
    }

    pre {
      margin: 0;
      white-space: pre-wrap;
      word-break: break-word;
      font-family: "IBM Plex Mono", "SFMono-Regular", monospace;
      font-size: 13px;
      line-height: 1.56;
    }

    .status {
      min-height: 20px;
      font-size: 13px;
      color: var(--muted);
    }

    .status.error { color: var(--warn); }
    .status.ok { color: var(--ok); }

    .empty {
      padding: 24px;
      border-radius: 18px;
      border: 1px dashed var(--line);
      color: var(--muted);
      text-align: center;
      background: rgba(255,255,255,0.28);
    }

    @media (max-width: 960px) {
      .layout { grid-template-columns: 1fr; }
      .session-groups { max-height: none; }
      .detail-grid, .form-grid { grid-template-columns: 1fr; }
      .hero { align-items: start; flex-direction: column; }
      .toolbar { justify-content: flex-start; }
    }
  </style>
</head>
<body>
  <div class="shell">
    <section class="hero">
      <div>
        <h1>会话管理台</h1>
        <p>查看 session 列表、历史详情，并向指定 session 追加新 prompt。也可以基于已有的 session_key 直接创建新的本地会话。</p>
      </div>
      <div class="toolbar">
        <a class="button-link secondary" href="/docs/api">API 文档</a>
        <button id="autoRefreshButton" class="secondary" type="button">开启自动刷新</button>
        <button id="refreshButton" class="secondary" type="button">刷新</button>
      </div>
    </section>

    <section class="layout">
      <article class="panel">
        <div class="panel-header">
          <h2>会话列表</h2>
          <span id="listMeta" class="badge">0 组</span>
        </div>
        <div class="panel-body">
          <div id="sessionGroups" class="session-groups"></div>
        </div>
      </article>

      <article class="panel">
        <div class="panel-header">
          <h2>会话详情</h2>
          <span id="detailMeta" class="badge">未选择</span>
        </div>
        <div class="panel-body">
          <section class="section-card">
            <small>新建 Session</small>
            <div class="form-grid">
              <label>
                Session 名称
                <input id="createName" type="text" placeholder="留空则自动生成">
              </label>
              <label>
                平台
                <input type="text" value="web（自动生成 session_key）" disabled>
              </label>
            </div>
            <div style="height:12px"></div>
            <label>
              工作目录
              <input id="createWorkDir" type="text" placeholder="留空则使用默认目录 ~/.cx-connect/workspace/...">
            </label>
            <div class="toolbar" style="justify-content:flex-start; margin-top:12px">
              <button id="createButton" class="accent" type="button">创建 Session</button>
            </div>
            <div id="createStatus" class="status"></div>
          </section>

          <div id="detailRoot" class="empty">从左侧选择一个 session 查看详情，或者先创建一个新的 session。</div>

          <section class="section-card">
            <small>追加 Prompt</small>
            <label>
              新 Prompt
              <textarea id="promptInput" placeholder="输入要发送给当前 session 的内容..."></textarea>
            </label>
            <div class="toolbar" style="justify-content:flex-start; margin-top:12px">
              <button id="sendPromptButton" class="accent" type="button">发送 Prompt</button>
            </div>
            <div id="promptStatus" class="status"></div>
          </section>
        </div>
      </article>
    </section>
  </div>

  <script>
    const state = {
      groups: [],
      selectedSessionKey: "",
      selectedSessionID: "",
      detail: null,
      refreshTimer: null,
      autoRefreshEnabled: false,
    };

    const initialParams = new URLSearchParams(window.location.search);
    state.selectedSessionKey = initialParams.get("session_key") || "";
    state.selectedSessionID = initialParams.get("session_id") || "";

    const els = {
      autoRefreshButton: document.getElementById("autoRefreshButton"),
      refreshButton: document.getElementById("refreshButton"),
      sessionGroups: document.getElementById("sessionGroups"),
      listMeta: document.getElementById("listMeta"),
      detailMeta: document.getElementById("detailMeta"),
      detailRoot: document.getElementById("detailRoot"),
      createName: document.getElementById("createName"),
      createWorkDir: document.getElementById("createWorkDir"),
      createButton: document.getElementById("createButton"),
      createStatus: document.getElementById("createStatus"),
      promptInput: document.getElementById("promptInput"),
      sendPromptButton: document.getElementById("sendPromptButton"),
      promptStatus: document.getElementById("promptStatus"),
    };

    function setStatus(element, message, tone = "") {
      element.textContent = message || "";
      element.className = tone ? "status " + tone : "status";
    }

    function formatDate(value) {
      if (!value) return "-";
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
    }

    function escapeHTML(value) {
      return String(value || "")
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;");
    }

    async function requestJSON(url, init) {
      const response = await fetch(url, init);
      const text = await response.text();
      let data = null;
      if (text) {
        try {
          data = JSON.parse(text);
        } catch (_) {
          data = text;
        }
      }
      if (!response.ok) {
        throw new Error(typeof data === "string" ? data : response.statusText);
      }
      return data;
    }

    async function loadSessions() {
      const data = await requestJSON("/api/admin/sessions");
      state.groups = data.groups || [];

      if (state.selectedSessionKey && state.selectedSessionID) {
        const exists = state.groups.some((group) =>
          group.session_key === state.selectedSessionKey &&
          group.sessions.some((session) => session.id === state.selectedSessionID)
        );
        if (!exists) {
          state.selectedSessionKey = "";
          state.selectedSessionID = "";
          state.detail = null;
        }
      }

      if (!state.selectedSessionKey && state.groups.length) {
        const group = state.groups[0];
        const active = group.sessions.find((session) => session.active) || group.sessions[0];
        if (active) {
          state.selectedSessionKey = group.session_key;
          state.selectedSessionID = active.id;
        }
      }

      renderGroups();

      if (state.selectedSessionKey && state.selectedSessionID) {
        await loadDetail();
      } else {
        renderDetail();
      }
    }

    function renderGroups() {
      els.listMeta.textContent = state.groups.length + " 组";
      if (!state.groups.length) {
        els.sessionGroups.innerHTML = '<div class="empty">还没有可展示的 session。至少先让机器人收到一次消息，或者手动创建新的 session。</div>';
        return;
      }

      els.sessionGroups.innerHTML = state.groups.map((group) => {
        const badges = [
          '<span class="badge">' + escapeHTML(group.platform || "unknown") + "</span>",
          '<span class="badge">' + group.sessions.length + " 个 session</span>",
          group.interactive ? '<span class="badge ok">运行中</span>' : '<span class="badge warn">空闲</span>',
        ].join("");

        const sessions = group.sessions.map((session) => {
          const classes = ["session-item"];
          if (group.session_key === state.selectedSessionKey && session.id === state.selectedSessionID) {
            classes.push("active");
          }
          const labels = [];
          if (session.active) labels.push("当前");
          if (session.busy) labels.push("忙碌");
          return '<button class="' + classes.join(" ") + '" type="button" data-session-key="' + escapeHTML(group.session_key) + '" data-session-id="' + escapeHTML(session.id) + '">' +
            '<div class="session-title"><strong>' + escapeHTML(session.name || session.id) + '</strong><span>' + escapeHTML(labels.join(" · ") || formatDate(session.updated_at)) + '</span></div>' +
            '<div class="session-sub">ID: ' + escapeHTML(session.id) + '</div>' +
            '<div class="session-sub">历史: ' + escapeHTML(String(session.history_count)) + ' · Agent: ' + escapeHTML(session.agent_session_id || "-") + '</div>' +
            '<div class="session-sub">' + escapeHTML(session.work_dir || "未设置工作目录") + '</div>' +
          '</button>';
        }).join("");

        return '<section class="group">' +
          '<div class="group-head"><strong>' + escapeHTML(group.session_key) + '</strong><div class="group-meta">' + badges + '</div></div>' +
          '<div class="group-sessions">' + sessions + '</div>' +
        '</section>';
      }).join("");

      for (const button of els.sessionGroups.querySelectorAll("button[data-session-key]")) {
        button.addEventListener("click", async () => {
          state.selectedSessionKey = button.dataset.sessionKey;
          state.selectedSessionID = button.dataset.sessionId;
          updateSelectionURL();
          setStatus(els.promptStatus, "");
          renderGroups();
          await loadDetail();
        });
      }
    }

    async function loadDetail() {
      if (!state.selectedSessionKey || !state.selectedSessionID) {
        state.detail = null;
        renderDetail();
        return;
      }
      const params = new URLSearchParams({
        session_key: state.selectedSessionKey,
        session_id: state.selectedSessionID,
      });
      state.detail = await requestJSON("/api/admin/session?" + params.toString());
      renderDetail();
    }

    function renderDetail() {
      if (!state.detail) {
        els.detailMeta.textContent = "未选择";
        els.detailRoot.className = "empty";
        els.detailRoot.innerHTML = "从左侧选择一个 session 查看详情，或者先创建一个新的 session。";
        return;
      }

      const detail = state.detail;
      const session = detail.session;
      els.detailMeta.textContent = session.id;

      const cards = [
        ["名称", session.name || "-"],
        ["状态", [session.busy ? "忙碌" : "空闲", detail.interactive ? "已连接" : "未连接"].join(" / ")],
        ["Agent Session ID", session.agent_session_id || "-"],
        ["工作目录", session.work_dir || "-"],
        ["创建时间", formatDate(session.created_at)],
        ["更新时间", formatDate(session.updated_at)],
      ].map(([label, value]) =>
        '<div class="detail-card"><small>' + escapeHTML(label) + '</small><strong>' + escapeHTML(value) + '</strong></div>'
      ).join("");

      const history = (session.history || []).length
        ? session.history.map((entry) => {
            const role = entry.role === "assistant" ? "assistant" : "user";
            const roleLabel = role === "assistant" ? "assistant" : "user";
            return '<article class="entry ' + role + '">' +
              '<div class="entry-head"><span>' + escapeHTML(roleLabel) + '</span><span>' + escapeHTML(formatDate(entry.timestamp)) + '</span></div>' +
              '<pre>' + escapeHTML(entry.content || "") + '</pre>' +
            '</article>';
          }).join("")
        : '<div class="empty">当前 session 还没有保存历史记录。</div>';

      els.detailRoot.className = "";
      els.detailRoot.innerHTML =
        '<div class="detail-grid">' + cards + '</div>' +
        '<div class="detail-card"><small>Session Key</small><span>' + escapeHTML(detail.session_key) + '</span></div>' +
        '<div class="history">' + history + '</div>';
    }

    async function refreshAll() {
      setStatus(els.createStatus, "刷新中...");
      setStatus(els.promptStatus, "");
      try {
        await loadSessions();
        setStatus(els.createStatus, "数据已刷新。", "ok");
      } catch (error) {
        setStatus(els.createStatus, error.message || String(error), "error");
      }
    }

    function renderAutoRefreshButton() {
      els.autoRefreshButton.textContent = state.autoRefreshEnabled ? "关闭自动刷新" : "开启自动刷新";
    }

    function updateSelectionURL() {
      if (!state.selectedSessionKey || !state.selectedSessionID) return;
      const params = new URLSearchParams(window.location.search);
      params.set("session_key", state.selectedSessionKey);
      params.set("session_id", state.selectedSessionID);
      window.history.replaceState(null, "", window.location.pathname + "?" + params.toString());
    }

    async function createSession() {
      const name = els.createName.value;
      const workDir = els.createWorkDir.value;

      els.createButton.disabled = true;
      setStatus(els.createStatus, "正在创建 web session...");
      try {
        const data = await requestJSON("/api/admin/session/create", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name,
            work_dir: workDir,
          }),
        });
        state.selectedSessionKey = data.session_key;
        state.selectedSessionID = data.session.id;
        updateSelectionURL();
        els.createName.value = "";
        els.createWorkDir.value = "";
        setStatus(els.createStatus, "Web Session 已创建，Session Key: " + data.session_key + "，工作目录: " + (data.display_work_dir || data.session.work_dir || "-"), "ok");
        await loadSessions();
      } catch (error) {
        setStatus(els.createStatus, error.message || String(error), "error");
      } finally {
        els.createButton.disabled = false;
      }
    }

    async function submitPrompt() {
      if (!state.selectedSessionKey || !state.selectedSessionID) {
        setStatus(els.promptStatus, "请先选择一个 session。", "error");
        return;
      }
      const prompt = els.promptInput.value;
      if (!prompt.trim()) {
        setStatus(els.promptStatus, "Prompt 不能为空。", "error");
        return;
      }

      els.sendPromptButton.disabled = true;
      setStatus(els.promptStatus, "正在发送 prompt...");
      try {
        await requestJSON("/api/admin/prompt", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            session_key: state.selectedSessionKey,
            session_id: state.selectedSessionID,
            prompt,
          }),
        });
        els.promptInput.value = "";
        setStatus(els.promptStatus, "Prompt 已提交。", "ok");
        await loadSessions();
      } catch (error) {
        setStatus(els.promptStatus, error.message || String(error), "error");
      } finally {
        els.sendPromptButton.disabled = false;
      }
    }

    function startAutoRefresh() {
      window.clearInterval(state.refreshTimer);
      state.refreshTimer = window.setInterval(async () => {
        try {
          await loadSessions();
        } catch (error) {
          setStatus(els.createStatus, error.message || String(error), "error");
        }
      }, 5000);
    }

    function stopAutoRefresh() {
      window.clearInterval(state.refreshTimer);
      state.refreshTimer = null;
    }

    function toggleAutoRefresh() {
      state.autoRefreshEnabled = !state.autoRefreshEnabled;
      if (state.autoRefreshEnabled) {
        startAutoRefresh();
        setStatus(els.createStatus, "自动刷新已开启。", "ok");
      } else {
        stopAutoRefresh();
        setStatus(els.createStatus, "自动刷新已关闭。");
      }
      renderAutoRefreshButton();
    }

    els.autoRefreshButton.addEventListener("click", toggleAutoRefresh);
    els.refreshButton.addEventListener("click", refreshAll);
    els.createButton.addEventListener("click", createSession);
    els.sendPromptButton.addEventListener("click", submitPrompt);

    renderAutoRefreshButton();
    refreshAll();
  </script>
</body>
</html>
`
