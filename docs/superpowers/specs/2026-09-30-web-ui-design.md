# log2grok Web UI — Design Spec

**Date:** 2026-09-30
**Status:** Approved design, pending spec review
**Topic:** A minimal in-repo web UI: paste logs → Grok pattern via the `log2grok` library.

## 1. Purpose & goals

Give users a zero-setup way to try `log2grok` without installing the CLI or writing
Go: paste log lines into a web page and see the discovered Grok pattern.

Success criteria:

- One documented command starts the server in-repo; the page loads and works.
- A paste of representative logs yields the pattern produced by the existing
  `pkg/log2grok` library (no reimplementation), plus coverage and source.
- Invalid input and unhelpful detections produce clear, honest feedback rather
  than silent garbage or a crash.
- The page is minimal, elegant, and intentionally designed.

## 2. Non-goals

- No multi-pattern / top-K UI (a future addition; this spec is single best pattern).
- No field decoding / timestamp extraction in the UI.
- No auth, persistence, database, or telemetry.
- No framework, npm, or build step. No new Go dependencies.
- Does not change the CLI contract in `SPEC.md`; this is an additive component.

## 3. Architecture

```
browser ──POST /api/discover {logs}──▶ cmd/log2grok-web (net/http, stdlib)
                                          │  split request text into lines
                                          │  l2g.Discover(lines, Options{})
        ◀──JSON {ok, pattern, meta}───────┘
```

- **Component:** a new binary at `cmd/log2grok-web`, matching the repo's
  `cmd/<binary>/main.go` layout.
- **Server:** Go standard library only (`net/http`, `embed`, `encoding/json`).
- **Frontend:** three static files (`index.html`, `styles.css`, `app.js`) embedded
  with `embed.FS` and served under `/static/`. Vanilla HTML/CSS/JS; no framework.
- **Library loading:** on startup call `l2g.LoadConfig(*configDir, os.Stderr)`
  mirroring `cmd/log2grok`, so the externalized library (`.log2grok` / `-config-dir`)
  behaves the same as the CLI.
- **Lifecycle:** print the listen URL on start; shut down gracefully on `SIGINT`.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:8080` | Listen address (localhost-only by default). |
| `-config-dir` | `""` | Externalized pattern library dir; empty uses the embedded default. |

### Routes

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/` | Serve `index.html`. |
| `GET` | `/static/*` | Serve embedded assets. |
| `POST` | `/api/discover` | Run discovery, return JSON. |
| `GET` | `/healthz` | `200 OK`, body `ok`. |

## 4. HTTP API contract

### Request

`POST /api/discover`, `Content-Type: application/json`:

```json
{ "logs": "<raw pasted text>" }
```

The server splits `logs` on `\n`, strips a single trailing `\r` per line, and
passes the resulting slice to the library. Blank lines are passed through; the
library discards them internally. There is no threshold/knob field: the UI uses
library defaults (`LibraryThreshold: 0.85`).

### Success response (`200 application/json`)

```json
{
  "ok": true,
  "pattern": {
    "grok": "%{IPORHOST:client_ip} ...",
    "source": "library:Nginx Access Combined",
    "sourceFamily": "library",
    "coverage": 0.984,
    "matched": 4823,
    "total": 4900,
    "truncated": false,
    "estimated": false
  },
  "lines": [
    { "matched": true, "segments": [
        { "text": "10.0.0.1", "token": "IPORHOST", "field": "client_ip" },
        { "text": " - alice " }
    ]}
  ],
  "meta": { "lines": 4900, "elapsedMs": 12 }
}
```

`lines` is the count of non-empty input lines; `grok` is always a non-empty
string on success. `lines[]` is aligned 1:1 with the input split on `\n` (one
entry per rendered line, same order). Each entry reports whether the line
matched and carries `segments` that cover the whole line: a token segment names
the Grok primitive it matched (`token`) and the capture field (`field`), and a
literal segment has neither. Concatenating a line's segment texts reproduces the
line exactly. When the pattern names no fields (e.g. a bare CSV/TSV split), a
line is a single literal segment.

### Error response (`4xx`/`5xx`)

```json
{ "ok": false, "error": { "code": "empty_input", "message": "Paste at least one log line." } }
```

| HTTP | code | Trigger |
|---|---|---|
| `400` | `bad_request` | Body is not valid JSON. |
| `400` | `empty_input` | Zero non-empty lines after splitting (`log2grok.ErrEmptyInput`). |
| `413` | `too_large` | Body exceeds the 8 MiB cap (`http.MaxBytesReader`). |
| `405` | `method_not_allowed` | Non-`POST` on `/api/discover`. |
| `500` | `internal` | Any other error from `Discover`. |

All responses set `Content-Type: application/json; charset=utf-8`.

## 5. Error & low-confidence handling

- **Input validation (client, fast path):** the action is disabled and an inline
  hint shown while the textarea has no non-empty content; pre-submit validation
  mirrors the server's `empty_input`.
- **Input validation (server, authoritative):** the error codes in §4. The
  server never trusts the client.
- **Detection:** `Discover` always returns a fallback for non-empty input, so
  there is no hard "no pattern" error. Honesty is surfaced instead:
  - `coverage < 0.5` → warning: "Only N% of lines matched — the input may be
    mixed or an unrecognized format."
  - `estimated === true` → note: "Coverage estimated from a sample of the input."
  - `truncated === true` → note: "Input was truncated."
- **Transport/parse failures (client):** error banner with a retry affordance;
  the loading state disables the action until completion.

## 6. UI / UX

- **Layout:** single centered column, max width ~880px, generous whitespace.
- **Flow:** header → monospace textarea (live line count, Clear) → **Discover
  pattern** button (⌘/Ctrl+Enter) → result card.
- **Empty state:** three one-click example chips (nginx access, JSON app log,
  syslog) that populate the textarea, so the demo is useful immediately.
- **Result card:** the Grok pattern in monospace, prominent, with a **Copy**
  button and copy feedback; a meta row with source/family badges and a coverage
  bar (`matched` / `total`); notes/warning per §5.
- **Token highlighting:** after discovery, each captured token in the input is
  tinted by the Grok primitive it matched (`IPORHOST`, `HTTPDATE`, `INT`, …),
  grouped into semantic families; a legend maps each token to its color.
  Unmatched lines get a subtle red tint. Rendering uses an aligned, `aria-hidden`
  backdrop layer of token spans under the textarea (a `<textarea>` cannot style
  individual tokens); it is cleared while editing and bounded to a few thousand
  lines.
- **Visual direction (frontend-design):** restrained single-accent palette,
  subtle borders and shadows, refined type scale, system UI font stack for
  chrome and a monospace stack for patterns, dark mode via
  `prefers-color-scheme`, tasteful micro-transitions.
- **Accessibility:** labelled controls, visible focus rings, result/error region
  marked `aria-live="polite"`, keyboard-operable, `prefers-reduced-motion`
  respected, WCAG AA contrast.

## 7. Testing & running

- **Unit tests** (`cmd/log2grok-web/main_test.go`, `httptest`):
  - success: POST an nginx sample → `200`, `ok:true`, non-empty `grok`, non-empty `source`;
  - empty/whitespace `logs` → `400 empty_input`;
  - malformed JSON → `400 bad_request`;
  - oversized body → `413 too_large`;
  - `GET /api/discover` → `405 method_not_allowed`;
  - `GET /` → `200` and `text/html`;
  - `GET /static/missing.js` → `404`.
- **Manual/browser verification** of the real page (empty state, success,
  empty-input error, low-coverage warning, copy button, dark mode).
- **Run:**
  - `go run ./cmd/log2grok-web` → http://127.0.0.1:8080
  - `make web-run` (same), `make web` (build `bin/log2grok-web`).
  - `make test` already runs `go test ./...`, picking up the new tests.

## 8. Files

| File | Change |
|---|---|
| `cmd/log2grok-web/main.go` | New: server, routing, `/api/discover` handler, line splitting, JSON, shutdown. |
| `cmd/log2grok-web/static/index.html` | New: page markup. |
| `cmd/log2grok-web/static/styles.css` | New: presentation. |
| `cmd/log2grok-web/static/app.js` | New: fetch, state, rendering, examples, copy. |
| `cmd/log2grok-web/main_test.go` | New: handler tests. |
| `Makefile` | Add `web` and `web-run` targets. |
| `README.md` | Add a short "Web UI" section. |

## 9. Risks & notes

- **Large pastes:** bounded by the 8 MiB body cap; the library samples large
  inputs internally, so latency stays bounded.
- **Localhost default:** binding `127.0.0.1` avoids exposing an unauthenticated
  endpoint on the network; users can override with `-addr`.
- **No new dependencies** keeps CI and the module graph unchanged.
