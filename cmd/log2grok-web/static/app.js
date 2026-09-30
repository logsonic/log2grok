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

// Token highlighting uses a transparent backdrop layer aligned under the
// textarea; each token span gets a background tint. Bounded so a huge paste
// cannot produce a huge DOM.
const MAX_HIGHLIGHT_LINES = 2000;
const MAX_HIGHLIGHT_CHARS = 500000;
const EDITOR_MAX_PX = 420;

let currentLines = [];
let serverLines = null; // per-line {matched, segments} from the last discovery

function countLines(text) {
  return text.split("\n").filter((line) => line.trim() !== "").length;
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

// splitLogLines mirrors the server's splitLines: split on \n, drop one
// trailing \r per line, drop trailing empty lines. Its length must equal the
// server's per-line array so line i maps to serverLines[i].
function splitLogLines(text) {
  if (text === "") return [];
  const lines = text.split("\n").map((line) => line.replace(/\r$/, ""));
  while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

// tokenFamily maps a Grok primitive name to a semantic color family.
function tokenFamily(token) {
  const t = String(token || "").toUpperCase();
  if (!t) return "other";
  if (/TIME|DATE|MONTH|YEAR|DAY|HOUR|MINUTE|SECOND|TZ$/.test(t)) return "time";
  if (/LEVEL|SEVERITY/.test(t)) return "level";
  if (/IP$|^IP|HOST|PORT/.test(t)) return "ip";
  if (/INT|NUM|FLOAT|DURATION|DECIMAL/.test(t)) return "number";
  if (/URI|URL|PATH|URN/.test(t)) return "uri";
  if (/QUOTED|QS$/.test(t)) return "quoted";
  if (/UUID|MAC$|EMAIL/.test(t)) return "id";
  if (/WORD|NOTSPACE|DATA|SPACE|USER|GREEDY|PROG/.test(t)) return "word";
  return "other";
}

// renderLines paints the backdrop: token segments when a discovery result is
// available and aligned, otherwise plain text. Token text is transparent (the
// textarea shows the real text); only the backgrounds are visible.
function renderLines() {
  el.legend.hidden = true;
  const text = el.logs.value;
  if (text.length > MAX_HIGHLIGHT_CHARS) {
    currentLines = [];
    el.logLines.replaceChildren();
    return;
  }
  const raw = splitLogLines(text);
  if (raw.length > MAX_HIGHLIGHT_LINES) {
    currentLines = [];
    el.logLines.replaceChildren();
    return;
  }
  currentLines = raw;

  const useTokens =
    Array.isArray(serverLines) &&
    serverLines.length === raw.length &&
    serverLines.some((l) => l && Array.isArray(l.segments) && l.segments.some((s) => s.token));

  const frag = document.createDocumentFragment();
  for (let i = 0; i < raw.length; i++) {
    const div = document.createElement("div");
    div.className = "logline";
    if (useTokens) {
      const line = serverLines[i];
      const blank = raw[i] === "";
      div.classList.toggle("is-miss", !blank && line.matched === false);
      let content = false;
      for (const s of line.segments || []) {
        if (s.token) {
          const span = document.createElement("span");
          span.className = "tok";
          span.dataset.k = tokenFamily(s.token);
          span.title = (s.field ? s.field + ": " : "") + "%{" + s.token + "}";
          span.textContent = s.text;
          div.appendChild(span);
          content = true;
        } else if (s.text !== "") {
          div.appendChild(document.createTextNode(s.text));
          content = true;
        }
      }
      if (!content) div.textContent = "\u200b";
    } else {
      div.textContent = raw[i] === "" ? "\u200b" : raw[i];
    }
    frag.appendChild(div);
  }
  el.logLines.replaceChildren(frag);
  el.logLines.scrollTop = el.logs.scrollTop;
  if (useTokens) buildLegend();
}

// buildLegend lists the distinct tokens in the current pattern (each with its
// color swatch) plus an "unmatched line" chip when any line failed to match.
function buildLegend() {
  const seen = new Map();
  let hasMiss = false;
  for (let i = 0; i < currentLines.length; i++) {
    const line = serverLines[i];
    if (!line) continue;
    if (currentLines[i] !== "" && line.matched === false) hasMiss = true;
    for (const s of line.segments || []) {
      if (s.token && !seen.has(s.token)) seen.set(s.token, tokenFamily(s.token));
    }
  }
  if (seen.size === 0 && !hasMiss) {
    el.legend.hidden = true;
    return;
  }
  const frag = document.createDocumentFragment();
  const label = document.createElement("span");
  label.className = "legend__label";
  label.textContent = "tokens:";
  frag.appendChild(label);
  const chip = (family, text, miss) => {
    const item = document.createElement("span");
    item.className = "legend__item";
    const sw = document.createElement("span");
    sw.className = miss ? "swatch swatch--miss" : "swatch";
    if (!miss) sw.dataset.k = family;
    const name = document.createElement("span");
    name.textContent = text;
    item.append(sw, name);
    frag.appendChild(item);
  };
  for (const [tok, family] of seen) chip(family, tok, false);
  if (hasMiss) chip(null, "unmatched line", true);
  el.legend.replaceChildren(frag);
  el.legend.hidden = false;
}

function autosize() {
  el.logs.style.height = "auto";
  const full = el.logs.scrollHeight;
  el.logs.style.height = Math.min(full, EDITOR_MAX_PX) + "px";
  el.logs.style.overflowY = full > EDITOR_MAX_PX ? "auto" : "hidden";
  el.logLines.scrollTop = el.logs.scrollTop;
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

  serverLines = Array.isArray(data.lines) ? data.lines : null;
  renderLines();
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
  serverLines = null;
  clearFeedback();
  el.result.hidden = true;
  renderLines();
  autosize();
  updateCount();
  el.logs.focus();
});
el.logs.addEventListener("input", () => {
  serverLines = null; // highlights are stale as soon as the text changes
  renderLines();
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
  serverLines = null;
  renderLines();
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
    renderLines();
    autosize();
  }, 150);
});

renderLines();
autosize();
updateCount();
