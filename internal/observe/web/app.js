"use strict";

const ui = {
  connection: document.querySelector("#connection"),
  connectionLabel: document.querySelector("#connection-label"),
  sessionCount: document.querySelector("#session-count"),
  sessionList: document.querySelector("#session-list"),
  lastSync: document.querySelector("#last-sync"),
  empty: document.querySelector("#empty-state"),
  runtime: document.querySelector("#runtime-view"),
  workspacePath: document.querySelector("#workspace-path"),
  sessionTitle: document.querySelector("#session-title"),
  sessionIdentity: document.querySelector("#session-identity"),
  processList: document.querySelector("#process-list"),
  detailPlaceholder: document.querySelector("#detail-placeholder"),
  detailContent: document.querySelector("#detail-content"),
  toast: document.querySelector("#toast"),
};

const model = {
  sessions: [],
  activeSessionID: "",
  activeRunID: "",
  detail: null,
  online: false,
  detailRequest: 0,
};

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function shortID(value, size = 9) {
  if (!value) return "—";
  const parts = String(value).split("_");
  const tail = parts.at(-1);
  return tail.length > size ? tail.slice(0, size) : tail;
}

function projectName(path) {
  const clean = String(path || "Workspace").replaceAll("\\", "/").replace(/\/$/, "");
  return clean.split("/").at(-1) || "Workspace";
}

function relativeTime(input) {
  const elapsed = Date.now() - new Date(input).getTime();
  if (!Number.isFinite(elapsed)) return "—";
  if (elapsed < 5000) return "now";
  if (elapsed < 60000) return `${Math.floor(elapsed / 1000)}s`;
  if (elapsed < 3600000) return `${Math.floor(elapsed / 60000)}m`;
  if (elapsed < 86400000) return `${Math.floor(elapsed / 3600000)}h`;
  return `${Math.floor(elapsed / 86400000)}d`;
}

function clockTime(input) {
  const date = new Date(input);
  if (!Number.isFinite(date.getTime())) return "—";
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(date);
}

function duration(ms) {
  const value = Number(ms || 0);
  if (value < 1000) return `${value}ms`;
  if (value < 60000) return `${(value / 1000).toFixed(value < 10000 ? 1 : 0)}s`;
  return `${Math.floor(value / 60000)}m ${Math.floor((value % 60000) / 1000)}s`;
}

function statusClass(status) {
  return ["running", "waiting", "complete", "failed", "killed"].includes(status) ? status : "complete";
}

async function fetchJSON(path) {
  const response = await fetch(path, { headers: { Accept: "application/json" }, cache: "no-store" });
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
  return response.json();
}

function setOnline(online) {
  model.online = online;
  ui.connection.classList.toggle("is-online", online);
  ui.connection.classList.toggle("is-offline", !online);
  ui.connectionLabel.textContent = online ? "Connected" : "Daemon unavailable";
}

let toastTimer;
function showToast(message) {
  ui.toast.textContent = message;
  ui.toast.classList.add("is-visible");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => ui.toast.classList.remove("is-visible"), 3500);
}

function allRuns() {
  return model.sessions.flatMap((session) => session.processes.map((process) => process.run).filter(Boolean));
}

function activeSession() {
  return model.sessions.find((session) => session.id === model.activeSessionID) || null;
}

function normalizeSelection() {
  if (!model.sessions.some((session) => session.id === model.activeSessionID)) {
    model.activeSessionID = model.sessions[0]?.id || "";
  }
  const session = activeSession();
  const available = session?.processes.map((process) => process.run?.id).filter(Boolean) || [];
  if (!available.includes(model.activeRunID)) {
    model.activeRunID = available[0] || "";
    model.detail = null;
  }
}

function renderSessions() {
  ui.sessionCount.textContent = String(model.sessions.length);
  ui.sessionList.innerHTML = model.sessions.map((session) => {
    const name = projectName(session.workspace_root);
    const active = session.id === model.activeSessionID;
    return `<button class="session-item${active ? " is-active" : ""}" data-session-id="${escapeHTML(session.id)}" aria-current="${active ? "page" : "false"}">
      <span class="session-monogram">${escapeHTML(name.slice(0, 2))}</span>
      <span class="session-copy">
        <span class="session-project">${escapeHTML(name)}</span>
        <span class="session-meta">${escapeHTML(shortID(session.codex_session_id))} · ${session.processes.length} process${session.processes.length === 1 ? "" : "es"}</span>
      </span>
    </button>`;
  }).join("");
}

function orderedProcesses(processes) {
  const children = new Map();
  const known = new Set(processes.map((process) => process.id));
  for (const process of processes) {
    const parent = known.has(process.parent_process_id) ? process.parent_process_id : "root";
    if (!children.has(parent)) children.set(parent, []);
    children.get(parent).push(process);
  }
  const result = [];
  function visit(parent, depth) {
    for (const process of children.get(parent) || []) {
      result.push({ process, depth });
      visit(process.id, depth + 1);
    }
  }
  visit("root", 0);
  return result;
}

function renderRuntime() {
  const session = activeSession();
  const hasData = Boolean(session);
  ui.empty.classList.toggle("is-hidden", hasData);
  ui.runtime.classList.toggle("is-hidden", !hasData);
  if (!session) return;

  const name = projectName(session.workspace_root);
  ui.workspacePath.textContent = session.workspace_root;
  ui.sessionTitle.textContent = name;
  ui.sessionIdentity.textContent = `session ${shortID(session.codex_session_id, 12)}`;
  const statuses = session.processes.map((process) => process.run?.status || "unknown");
  document.querySelector("#metric-processes").textContent = String(session.processes.length);
  document.querySelector("#metric-running").textContent = String(statuses.filter((status) => status === "running").length);
  document.querySelector("#metric-waiting").textContent = String(statuses.filter((status) => status === "waiting").length);
  document.querySelector("#metric-terminal").textContent = String(statuses.filter((status) => ["complete", "failed", "killed"].includes(status)).length);

  ui.processList.innerHTML = orderedProcesses(session.processes).map(({ process, depth }) => {
    const run = process.run;
    if (!run) return "";
    const agentName = process.agent_id === "ROOT" ? "Root agent" : `Subagent ${shortID(process.agent_id, 7)}`;
    const machine = run.machine_name || `${run.machine_kind} loop`;
    const state = run.state_name || run.state_id;
    const selected = run.id === model.activeRunID;
    return `<button class="process-row${selected ? " is-selected" : ""}" data-run-id="${escapeHTML(run.id)}" data-status="${escapeHTML(run.status)}" aria-pressed="${selected}" style="--depth:${depth}">
      <span class="agent-cell"><span class="agent-copy"><span class="agent-name">${escapeHTML(agentName)}</span><span class="agent-id">${escapeHTML(shortID(run.id, 12))}</span></span></span>
      <span class="cell-stack"><span class="cell-primary">${escapeHTML(machine)}</span><span class="cell-secondary">${escapeHTML(run.machine_kind)}</span></span>
      <span class="cell-stack"><span class="cell-primary">${escapeHTML(state)}</span><span class="cell-secondary">${escapeHTML(run.state_kind)} · iteration ${run.iteration}</span></span>
      <span class="status-pill ${statusClass(run.status)}">${escapeHTML(run.status)}</span>
      <span class="relative-time">${escapeHTML(relativeTime(run.updated_at))}</span>
    </button>`;
  }).join("");
}

function renderDetail() {
  const detail = model.detail;
  const visible = detail && detail.run.id === model.activeRunID;
  ui.detailPlaceholder.classList.toggle("is-hidden", visible);
  ui.detailContent.classList.toggle("is-hidden", !visible);
  if (!visible) return;

  const run = detail.run;
  const summary = detail.summary || {};
  const states = (detail.states || []).map((state, index) => `<div class="state-node${state.current ? " is-current" : ""}" title="${escapeHTML(state.id)}">
    <div class="state-index">${String(index + 1).padStart(2, "0")}</div>
    <div class="state-name">${escapeHTML(state.name || state.id)}</div>
    <div class="state-kind">${escapeHTML(state.kind)} · ${escapeHTML(state.executor)}</div>
  </div>`).join("");

  const limits = (detail.limits || []).length ? detail.limits.map((limit) => `<div class="limit-row">
    <span class="limit-kind">${escapeHTML(limit.kind)}</span>
    <span class="limit-key">${escapeHTML(limit.key)}</span>
    <span class="limit-value">${escapeHTML(typeof limit.value === "string" ? limit.value : JSON.stringify(limit.value))}</span>
  </div>`).join("") : `<div class="empty-inline">No active limits in this machine.</div>`;

  const events = [...(detail.events || [])].reverse().slice(0, 14);
  const eventRows = events.length ? events.map((event) => {
    const evidence = (event.verifications || []).map((verification) => `<div class="evidence${verification.passed ? " is-passed" : ""}">
      <span>${escapeHTML(verification.name)}${verification.error ? ` · ${escapeHTML(verification.error)}` : ""}</span>
    </div>`).join("");
    return `<div class="event-row">
      <span class="event-time">${escapeHTML(clockTime(event.created_at))}</span>
      <span class="event-body">
        <span class="event-title"><span class="event-tool">${escapeHTML(event.tool_name || "Runtime")}</span><span class="event-kind">${escapeHTML(event.event_name)}</span>${event.decision === "deny" ? `<span class="status-pill failed">denied</span>` : ""}</span>
        ${event.command ? `<span class="event-command" title="${escapeHTML(event.command)}">${escapeHTML(event.command)}</span>` : ""}
        ${evidence ? `<span class="event-evidence">${evidence}</span>` : ""}
      </span>
      <span class="event-duration">${event.duration_ms ? escapeHTML(duration(event.duration_ms)) : ""}</span>
    </div>`;
  }).join("") : `<div class="empty-inline">No hook events have been recorded for this run.</div>`;

  ui.detailContent.innerHTML = `<div class="detail-head">
    <div class="detail-title-row">
      <div><div class="detail-kicker">${escapeHTML(detail.agent_id === "ROOT" ? "root agent" : "subagent")} · ${escapeHTML(shortID(detail.process_id, 12))}</div>
      <h2>${escapeHTML(run.machine_name || `${run.machine_kind} loop`)}</h2>
      <p class="detail-subtitle">${escapeHTML(run.id)}</p></div>
      <span class="status-pill ${statusClass(run.status)}">${escapeHTML(run.status)}</span>
    </div>
    <div class="detail-metrics">
      <div class="detail-metric"><span>Iteration</span><strong>${run.iteration}</strong></div>
      <div class="detail-metric"><span>Stack</span><strong>${run.stack_depth}</strong></div>
      <div class="detail-metric"><span>Tool calls</span><strong>${summary.tool_calls || 0}</strong></div>
      <div class="detail-metric"><span>Tool time</span><strong>${escapeHTML(duration(summary.tool_time_ms || 0))}</strong></div>
    </div>
  </div>
  <section class="detail-section"><div class="section-title"><h3>Machine position</h3><span>${escapeHTML(run.state_name || run.state_id)}</span></div><div class="state-track">${states}</div></section>
  <section class="detail-section"><div class="section-title"><h3>Limits</h3><span>${(detail.limits || []).length} active</span></div><div class="limit-list">${limits}</div></section>
  <section class="detail-section"><div class="section-title"><h3>Recent activity</h3><span>${summary.events || 0} events · ${summary.verifications || 0} checks</span></div><div class="event-list">${eventRows}</div></section>`;
}

async function loadDetail() {
  if (!model.activeRunID) {
    model.detail = null;
    renderDetail();
    return;
  }
  const request = ++model.detailRequest;
  try {
    const detail = await fetchJSON(`/api/v1/runs/${encodeURIComponent(model.activeRunID)}`);
    if (request !== model.detailRequest) return;
    model.detail = detail;
    renderDetail();
  } catch (error) {
    if (request !== model.detailRequest) return;
    model.detail = null;
    renderDetail();
    showToast(`Could not load run detail: ${error.message}`);
  }
}

async function refresh() {
  try {
    const snapshot = await fetchJSON("/api/v1/snapshot");
    model.sessions = snapshot.sessions || [];
    normalizeSelection();
    setOnline(true);
    ui.lastSync.textContent = `Synced ${clockTime(snapshot.generated_at)}`;
    renderSessions();
    renderRuntime();
    await loadDetail();
  } catch (error) {
    setOnline(false);
    showToast(`Runtime connection lost: ${error.message}`);
  }
}

ui.sessionList.addEventListener("click", (event) => {
  const target = event.target.closest("[data-session-id]");
  if (!target) return;
  model.activeSessionID = target.dataset.sessionId;
  model.activeRunID = "";
  model.detail = null;
  normalizeSelection();
  renderSessions();
  renderRuntime();
  loadDetail();
});

ui.processList.addEventListener("click", (event) => {
  const target = event.target.closest("[data-run-id]");
  if (!target) return;
  model.activeRunID = target.dataset.runId;
  model.detail = null;
  renderRuntime();
  renderDetail();
  loadDetail();
});

refresh();
setInterval(refresh, 1800);
