# Serving the web UI without a server: JS rewrite vs. WASM

**Verdict:** don't write a JavaScript engine. Compile the existing Go engine to
WebAssembly instead. `cmd/log2grok-wasm` is a working proof; it is not yet wired
into the page.

## Why not a JS rewrite

- It would be a second engine (~7.7k lines under `internal/pattern` and
  `pkg/log2grok`, the tiler alone 1.3k) that must be kept in lockstep with the Go
  one. The project rule is one engine per job, proven by `TestFieldRecoveryHarness`
  (floor 0.95) and the benchmark corpus.
- Grok compiles to Go RE2 regexps. JS `RegExp` is a backtracking engine with
  different named-group syntax (`(?P<n>…)` is a syntax error) and no linear-time
  guarantee, so the same pattern can match differently and a hostile paste can
  hang the tab.
- The CLI and the Go library still need the Go engine, so a JS port is pure addition.

## What WASM costs (measured, Go 1.25.7, Apple silicon, node 25)

| | |
|---|---|
| `log2grok.wasm` raw / gzip -9 / brotli -9 | 4.4 MB / 1.2 MB / 1.0 MB |
| `wasm_exec.js` | 17 KB |
| Instantiate (cold start, node) | ~80 ms |
| 31 lines | 6–50 ms wasm vs 1–5 ms server round trip |
| 4,110 mixed benchmark lines | 700 ms wasm vs 91 ms server |
| 18,900 apache lines | 983 ms wasm vs 179 ms server |

Output is byte-identical to `/api/discover` (modulo `elapsedMs`) on every input
tried, because both call `internal/webapi.Run`.

## How it is wired

- `internal/webapi.Run` is the one discovery entry point; `cmd/log2grok-web`
  calls it over HTTP and `cmd/log2grok-wasm` exposes it as `log2grokDiscover`.
- `static/worker.js` loads the WASM in a Web Worker, so a large paste never
  freezes the page. `static/app.js` prefers the worker and falls back to
  `POST /api/discover` when the WASM files are absent (the bare Go server).
- `make site` builds the standalone site into `dist/` (WASM, `wasm_exec.js`,
  hashed asset URLs, `_headers`). No server is needed.

## Deploying to Cloudflare Pages

Build command `make site`, output directory `dist`. `deploy/cloudflare/_headers`
carries the CSP (`'wasm-unsafe-eval'`, `worker-src 'self'`) and marks `/static/*`
immutable; a test keeps its CSP identical to the Go server's. Pages needs Go
installed in the build image (set `GO_VERSION=1.25.7`), or build locally and
deploy `dist/` with `wrangler pages deploy dist`.

## Limits

- Only the embedded pattern library; `-config-dir` custom patterns need the server.
- Browser timing varies a lot: the same 20,000-line paste took 0.75 s in node,
  about 2.7 s in a warm Chrome tab, and 0.13 s natively on the server. Expect
  several seconds for very large pastes; the worker keeps the page responsive.
