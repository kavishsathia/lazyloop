"use strict";

const DEFAULT_SCRIPT = `goal {
  limit attempts 6;
  limit traps 2;

  allow bash;
  allow apply_patch;

  verify exists = bash.check("test -f NOTES.md");
  trap "Do these notes read well?";
  return exists;
}`;

const $ = (id) => document.getElementById(id);
const state = { runId: null, after: 0, timer: null, tab: "timeline", events: [] };

function esc(text) {
  return String(text ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

async function api(path, options) {
  const response = await fetch(path, options);
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || response.statusText);
  return body;
}

// ---- config -------------------------------------------------------------

let compileTimer = null;
function scheduleCompile() {
  clearTimeout(compileTimer);
  compileTimer = setTimeout(async () => {
    const status = $("compile-status");
    try {
      const result = await api("/api/compile", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ script: $("script").value }),
      });
      status.className = result.ok ? "ok" : "bad";
      status.textContent = result.ok ? `${result.states} states` : result.error.split("\n")[0];
    } catch (err) {
      status.className = "bad";
      status.textContent = err.message;
    }
  }, 300);
}

function addTasteRow(clause = { name: "", clause: "" }) {
  const row = document.createElement("div");
  row.className = "taste-row";
  row.innerHTML =
    `<input class="t-name" placeholder="name" value="${esc(clause.name)}" />` +
    `<input class="t-clause" placeholder="the standard, in your words" value="${esc(clause.clause)}" />` +
    `<button type="button" class="ghost t-del">×</button>`;
  row.querySelector(".t-del").onclick = () => row.remove();
  $("taste").appendChild(row);
}

function readTaste() {
  return [...document.querySelectorAll(".taste-row")]
    .map((row) => ({
      name: row.querySelector(".t-name").value.trim(),
      clause: row.querySelector(".t-clause").value.trim(),
    }))
    .filter((clause) => clause.name && clause.clause);
}

// ---- rendering ----------------------------------------------------------

function verdictOf(decision) {
  if (!decision) return null;
  return decision.decision || decision.permission || null;
}

function renderTimeline(detail) {
  const rows = [];
  for (const event of state.events) {
    if (event.stream !== "codex") continue;
    const payload = event.payload || {};
    const item = payload.item || {};
    let text = item.text || item.command || payload.error || "";
    if (Array.isArray(item.changes)) text = item.changes.map((c) => c.path).join("\n");
    if (!text && payload.type) text = JSON.stringify(payload).slice(0, 400);
    rows.push({ at: event.created_at, kind: payload.type || event.stream, text, verdict: null, why: "" });
  }
  for (const decision of detail.decisions || []) {
    const payload = decision.payload || {};
    const verdict = verdictOf(decision.decision);
    rows.push({
      at: decision.created_at,
      kind: `hamster ${payload.tool_name || decision.kind}`,
      text: (payload.tool_input && payload.tool_input.command) || payload.hook_event_name || "",
      verdict,
      why: (decision.decision && (decision.decision.reason || decision.decision.context)) || "",
    });
  }
  rows.sort((a, b) => new Date(a.at) - new Date(b.at));
  if (!rows.length) return `<p class="empty">Nothing yet.</p>`;
  return rows
    .map(
      (row) => `<div class="entry ${row.verdict || ""}">
        <div class="head">
          <span class="kind">${esc(row.kind)}</span>
          ${row.verdict ? `<span class="verdict">${esc(row.verdict)}</span>` : ""}
        </div>
        ${row.text ? `<pre>${esc(row.text)}</pre>` : ""}
        ${row.why ? `<p class="why">${esc(row.why)}</p>` : ""}
      </div>`
    )
    .join("");
}

function renderLoops(detail) {
  const loops = detail.loops || [];
  const traps = detail.open_traps || [];
  let html = "";
  if (traps.length) {
    html += `<h3>Waiting on you</h3>` + traps.map((trap) =>
      `<div class="entry deny"><div class="head"><span class="kind">${esc(trap.name)}</span></div>
       <pre>${esc(trap.prompt)}</pre>
       <p class="why">hamster resolve ${esc(trap.id)} --approve</p></div>`).join("");
  }
  if (!loops.length) return html || `<p class="empty">No Hamster loop started yet.</p>`;
  html += `<table><thead><tr><th>machine</th><th>status</th><th>state</th><th>iter</th><th>result</th></tr></thead><tbody>`;
  for (const loop of loops) {
    html += `<tr><td>${esc(loop.machine)}</td><td>${esc(loop.status)}</td>
             <td class="mono">${esc(loop.state)}</td><td>${loop.iteration}</td>
             <td class="mono">${esc(loop.result ? JSON.stringify(loop.result) : loop.termination_reason || "")}</td></tr>`;
  }
  return html + `</tbody></table>`;
}

function renderAgent() {
  const lines = state.events.map((event) => {
    const payload = event.payload;
    const text = typeof payload === "string" ? payload : JSON.stringify(payload);
    return `<div class="entry ${event.stream === "stderr" ? "deny" : ""}"><pre>${esc(text)}</pre></div>`;
  });
  return lines.length ? lines.join("") : `<p class="empty">No output yet.</p>`;
}

function renderSummary(detail) {
  const run = detail.run;
  const decisions = detail.decisions || [];
  const denied = decisions.filter((d) => verdictOf(d.decision) === "deny").length;
  const loops = detail.loops || [];
  const iterations = loops.reduce((total, loop) => total + loop.iteration, 0);
  const stat = (label, value) => `<div class="stat"><b>${esc(value)}</b><span>${label}</span></div>`;
  $("summary").innerHTML =
    stat("status", run.status) +
    stat("tool calls", decisions.length) +
    stat("denied", denied) +
    stat("iterations", iterations) +
    stat("traps", (detail.open_traps || []).length);
  $("cancel").hidden = run.status !== "running" && run.status !== "starting";
}

function paint(detail) {
  renderSummary(detail);
  $("timeline").innerHTML = renderTimeline(detail);
  $("loops").innerHTML = renderLoops(detail);
  $("agent").innerHTML = renderAgent();
}

// ---- polling ------------------------------------------------------------

async function poll() {
  if (!state.runId) return;
  try {
    const detail = await api(`/api/runs/${state.runId}?after=${state.after}`);
    for (const event of detail.events || []) {
      state.events.push(event);
      state.after = Math.max(state.after, event.sequence);
    }
    paint(detail);
    const live = detail.run.status === "running" || detail.run.status === "starting";
    if (live) state.timer = setTimeout(poll, 1000);
    else loadHistory();
  } catch (err) {
    $("launch-error").textContent = err.message;
  }
}

function open(runId) {
  clearTimeout(state.timer);
  state.runId = runId;
  state.after = 0;
  state.events = [];
  poll();
}

async function loadHistory() {
  const { runs } = await api("/api/runs?limit=30");
  $("history").innerHTML = runs
    .map(
      (run) => `<li data-id="${run.id}" class="${run.id === state.runId ? "active" : ""}">
        <span>${esc(run.name || run.prompt.slice(0, 40) || run.id)}</span>
        <span class="when">${esc(run.status)}</span></li>`
    )
    .join("");
  for (const item of document.querySelectorAll("#history li")) {
    item.onclick = () => {
      const run = runs.find((entry) => entry.id === item.dataset.id);
      if (run) {
        $("script").value = run.script;
        $("prompt").value = run.prompt;
        $("taste").innerHTML = "";
        (run.taste || []).forEach(addTasteRow);
        scheduleCompile();
      }
      open(item.dataset.id);
    };
  }
}

// ---- wiring -------------------------------------------------------------

$("script").value = DEFAULT_SCRIPT;
$("prompt").value = "Write a short NOTES.md explaining what this repository is for.";
$("script").addEventListener("input", scheduleCompile);
$("add-taste").onclick = () => addTasteRow();

$("run").onclick = async () => {
  $("launch-error").textContent = "";
  $("run").disabled = true;
  try {
    const run = await api("/api/runs", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        script: $("script").value, prompt: $("prompt").value, taste: readTaste(),
      }),
    });
    open(run.id);
    loadHistory();
  } catch (err) {
    $("launch-error").textContent = err.message;
  } finally {
    $("run").disabled = false;
  }
};

$("cancel").onclick = async () => {
  if (state.runId) await api(`/api/runs/${state.runId}/cancel`, { method: "POST" });
};

for (const tab of document.querySelectorAll(".tabs button")) {
  tab.onclick = () => {
    for (const other of document.querySelectorAll(".tabs button")) other.classList.remove("active");
    tab.classList.add("active");
    for (const pane of ["timeline", "loops", "agent"]) $(pane).hidden = pane !== tab.dataset.tab;
  };
}

scheduleCompile();
loadHistory();
