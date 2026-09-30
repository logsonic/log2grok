"use strict";

const EXAMPLES = {
  nginx: [
    '10.0.0.1 - alice [15/Jan/2025:10:23:45 +0000] "GET /index.html HTTP/1.1" 200 1024',
    '10.0.0.2 - bob [15/Jan/2025:10:23:46 +0000] "POST /api/v1/items HTTP/1.1" 201 512',
    '10.0.0.3 - carol [15/Jan/2025:10:23:47 +0000] "GET /health HTTP/1.1" 200 3',
  ].join("\n"),
  json: [
    '{"ts":"2025-01-15T10:23:45Z","level":"info","msg":"server started","port":8080}',
    '{"ts":"2025-01-15T10:23:46Z","level":"warn","msg":"slow request","ms":812}',
    '{"ts":"2025-01-15T10:23:47Z","level":"info","msg":"request done","status":200}',
  ].join("\n"),
  syslog: [
    "Jan 15 10:23:45 web01 sshd[1234]: Accepted publickey for alice from 10.0.0.1 port 51234 ssh2",
    "Jan 15 10:23:46 web01 sshd[1235]: Failed password for bob from 10.0.0.2 port 51235 ssh2",
    "Jan 15 10:23:47 web01 sshd[1236]: Accepted publickey for carol from 10.0.0.3 port 51236 ssh2",
  ].join("\n"),
};

const el = {
  logs: document.getElementById("logs"),
  logLines: document.getElementById("logLines"),
  lineCount: document.getElementById("lineCount"),
  legend: document.getElementById("legend"),
  clear: document.getElementById("clear"),
  discover: document.getElementById("discover"),
  examples: document.getElementById("examples"),
  feedback: document.getElementById("feedback"),
  result: document.getElementById("result"),
  pattern: document.querySelector("#pattern code"),
  badges: document.getElementById("badges"),
  coverageFill: document.getElementById("coverageFill"),
  coverageText: document.getElementById("coverageText"),
  notes: document.getElementById("notes"),
  copy: document.getElementById("copy"),
};

function countLines(text) {
  return text.split("\n").filter((line) => line.trim() !== "").length;
}

// Per-line highlighting is done with a transparent backdrop layer aligned
// under the textarea. Bounded so a huge paste cannot create a huge DOM.
const MAX_HIGHLIGHT_LINES = 2000;
const MAX_HIGHLIGHT_CHARS = 500000;
const EDITOR_MAX_PX = 420;

let currentLines = [];
let lastMatches = null;

// splitLogLines mirrors the server's splitLines: split on \n, drop one
// trailing \r per line, drop trailing empty lines. Its length must equal the
// server's matches array so line i maps to matches[i].
function splitLogLines(text) {
  if (text === "") return [];
  const lines = text.split("\n").map((line) => line.replace(/\r$/, ""));
  while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

function renderLogLines(text) {
  el.legend.hidden = true;
  if (text.length > MAX_HIGHLIGHT_CHARS) {
    currentLines = [];
    el.logLines.replaceChildren();
    return;
  }
  const lines = splitLogLines(text);
  if (lines.length > MAX_HIGHLIGHT_LINES) {
    currentLines = [];
    el.logLines.replaceChildren();
    return;
  }
  currentLines = lines;
  const frag = document.createDocumentFragment();
  for (const line of lines) {
    const div = document.createElement("div");
    div.className = "logline";
    div.textContent = line === "" ? "\u200b" : line;
    frag.appendChild(div);
  }
  el.logLines.replaceChildren(frag);
  el.logLines.scrollTop = el.logs.scrollTop;
}

function clearHighlights() {
  lastMatches = null;
  for (const line of el.logLines.children) line.classList.remove("is-match", "is-miss");
  el.legend.hidden = true;
}

function applyMatches(matches) {
  const lines = el.logLines.children;
  if (!matches || matches.length !== lines.length) {
    clearHighlights();
    return;
  }
  for (let i = 0; i < lines.length; i++) {
    const blank = currentLines[i] === "";
    lines[i].classList.toggle("is-match", !blank && matches[i]);
    lines[i].classList.toggle("is-miss", !blank && !matches[i]);
  }
  el.legend.hidden = lines.length === 0;
}

function autosize() {
  el.logs.style.height = "auto";
  const full = el.logs.scrollHeight;
  el.logs.style.height = Math.min(full, EDITOR_MAX_PX) + "px";
  el.logs.style.overflowY = full > EDITOR_MAX_PX ? "auto" : "hidden";
  el.logLines.scrollTop = el.logs.scrollTop;
}

function updateCount() {
  const n = countLines(el.logs.value);
  el.lineCount.textContent = n + (n === 1 ? " line" : " lines");
  el.discover.disabled = n === 0 || el.discover.dataset.busy === "1";
}

function setBusy(busy) {
  el.discover.dataset.busy = busy ? "1" : "0";
  el.discover.textContent = busy ? "Discovering…" : "Discover pattern";
  updateCount();
}

function showError(message) {
  el.result.hidden = true;
  el.feedback.hidden = false;
  el.feedback.className = "feedback feedback--error";
  el.feedback.textContent = message;
}

function clearFeedback() {
  el.feedback.hidden = true;
  el.feedback.textContent = "";
}

function render(data) {
  clearFeedback();
  const p = data.pattern;
  el.pattern.textContent = p.grok;
  el.result.hidden = false;

  el.badges.replaceChildren();
  for (const [kind, text] of [["source", p.source], ["family", p.sourceFamily]]) {
    if (!text) continue;
    const badge = document.createElement("span");
    badge.className = "badge badge--" + kind;
    badge.textContent = text;
    el.badges.appendChild(badge);
  }

  const pct = Math.round(p.coverage * 100);
  el.coverageFill.style.width = Math.max(0, Math.min(100, pct)) + "%";
  el.coverageFill.dataset.level = pct < 50 ? "low" : pct < 90 ? "mid" : "high";
  el.coverageText.textContent =
    pct + "% matched (" + p.matched.toLocaleString() + " / " +
    p.total.toLocaleString() + " lines) in " + data.meta.elapsedMs + " ms";

  el.notes.replaceChildren();
  const notes = [];
  if (p.coverage < 0.5) {
    notes.push("Only " + pct + "% of lines matched — the input may be mixed or an unrecognized format.");
  }
  if (p.estimated) notes.push("Coverage estimated from a sample of the input.");
  if (p.truncated) notes.push("Input was truncated before discovery.");
  for (const text of notes) {
    const li = document.createElement("li");
    li.textContent = text;
    el.notes.appendChild(li);
  }

  lastMatches = data.matches || null;
  applyMatches(lastMatches);
}

async function discover() {
  if (countLines(el.logs.value) === 0) {
    showError("Paste at least one log line.");
    el.logs.focus();
    return;
  }
  setBusy(true);
  clearFeedback();
  try {
    const res = await fetch("/api/discover", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ logs: el.logs.value }),
    });
    const data = await res.json().catch(() => null);
    if (!res.ok || !data || data.ok !== true) {
      showError((data && data.error && data.error.message) || "Discovery failed. Please try again.");
      return;
    }
    render(data);
  } catch (err) {
    showError("Could not reach the server. Is it still running?");
  } finally {
    setBusy(false);
  }
}

el.discover.addEventListener("click", discover);
el.clear.addEventListener("click", () => {
  el.logs.value = "";
  clearFeedback();
  el.result.hidden = true;
  renderLogLines("");
  autosize();
  updateCount();
  el.logs.focus();
});
el.logs.addEventListener("input", () => {
  clearHighlights();
  renderLogLines(el.logs.value);
  autosize();
  updateCount();
});
el.logs.addEventListener("scroll", () => {
  el.logLines.scrollTop = el.logs.scrollTop;
});
el.logs.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
    event.preventDefault();
    discover();
  }
});
el.examples.addEventListener("click", (event) => {
  const chip = event.target.closest("[data-example]");
  if (!chip) return;
  el.logs.value = EXAMPLES[chip.dataset.example] || "";
  clearHighlights();
  renderLogLines(el.logs.value);
  autosize();
  updateCount();
  discover();
});
el.copy.addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText(el.pattern.textContent);
    el.copy.textContent = "Copied";
  } catch (err) {
    el.copy.textContent = "Select & copy";
  }
  setTimeout(() => { el.copy.textContent = "Copy"; }, 1500);
});

let resizeTimer = null;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    renderLogLines(el.logs.value);
    if (lastMatches) applyMatches(lastMatches);
    autosize();
  }, 150);
});

renderLogLines("");
autosize();
updateCount();
