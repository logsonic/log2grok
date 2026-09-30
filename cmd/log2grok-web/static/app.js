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
  lineCount: document.getElementById("lineCount"),
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
  updateCount();
  el.logs.focus();
});
el.logs.addEventListener("input", updateCount);
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

updateCount();
