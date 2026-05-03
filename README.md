# log2grok

One log file in, one Grok pattern out.

```sh
$ log2grok /var/log/nginx/access.log
%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"
# matched 4823 / 4900 lines (98.4%) -- library:Nginx Access Combined
```

For the full product spec, see [`SPEC.md`](SPEC.md). For the implementation walkthrough and how to extend the library, see [`design.md`](design.md).

## Discovery Pipeline

`Discover` runs three independent stages and returns the single best Grok pattern across them. The stages are:

| Stage | Source | Auto-accept threshold |
|-------|--------|-----------------------|
| 1. Structured | `tryStructured` — JSON / logfmt / CEF / W3C / CSV / TSV probes | `coverage ≥ LibraryThreshold` *and* a typed capture (skips the keyless JSON skeleton) |
| 2. Library | `tryLibrary` — curated + bundled regex `KnownPatterns`, sample-then-full scoring | `coverage ≥ LibraryThreshold` (default `0.85`) |
| 3. Drain | `deriveFromDrain` — drain3 clustering → token classifier → render | `coverage ≥ 0.85` |

If no stage auto-accepts, `pickBetter` picks the best candidate across stages (matched count → typed-capture count → family rank `library < structured < drain < fallback`). If every stage produced zero matches, `deriveSafeFallback` returns a generic skeleton.

### Stages explained (newcomer guide)

The three stages form a hierarchy of **how much we already knew about your log**:

- **Stage 1 (Structured)** — we know the **format spec** (JSON, CSV, …)
- **Stage 2 (Library)** — we know the **vendor** (Nginx, syslog, AWS ALB, …)
- **Stage 3 (Drain)** — we know **nothing** — figure it out from the data itself

#### Stage 1 — Structured

**Question it asks:** *"Is this file written in a well-known data format with rules?"*

Structured formats follow a mechanical specification — JSON syntax, CSV columns, key=value pairs. Either a line obeys the format's rules or it doesn't, no guessing.

Example inputs Stage 1 catches:

```text
{"timestamp":"2025-01-15T10:23:45Z","level":"info","msg":"server started"}
```
→ JSON

```text
ts=2025-01-15T10:23:45 level=info msg="server started" user_id=42
```
→ logfmt

```text
client_ip,timestamp,status,bytes
10.0.0.1,2025-01-15T10:23:45,200,1024
```
→ CSV with header

```text
CEF:0|Vendor|Product|1.0|100|Login Failed|5|src=10.0.0.1
```
→ CEF (Common Event Format)

**How it decides** (`internal/pattern/identify.go`): a list of `structuredProbe`s, each with a cheap `Likely(sample)` sniff test ("does this start with `{` and end with `}`?") and a `Render(sample)` that produces the Grok pattern when the sniff passes. The first probe whose pattern clears the auto-accept threshold wins.

**Mental model:** a **format detector**. It doesn't care about your specific file — it cares about which serialization format you used.

#### Stage 2 — Library

**Question it asks:** *"Does this file match one of the famous log formats people commonly produce?"*

The library is a curated list of regex patterns for log shapes that **specific tools and services** emit — Nginx, Apache, syslog, MySQL slow query, AWS ALB, Kubernetes audit, Java/log4j, etc. Each has a known shape and a hand-written Grok pattern.

Example inputs Stage 2 catches:

```text
10.0.0.1 - alice [15/Jan/2025:10:23:45 +0000] "GET /index.html HTTP/1.1" 200 1024
```
→ Nginx / Apache combined access log

```text
Jan 15 10:23:45 host01 sshd[1234]: Accepted password for alice from 10.0.0.1
```
→ syslog (RFC 3164)

```text
2025-01-15 10:23:45,123 INFO  [main] com.example.Server - server started
```
→ Java / log4j application log

**How it decides** (`internal/pattern/library.go`, `score.go`):

1. Score every `KnownPattern` against the sample.
2. Keep the top 12 by sample coverage and re-evaluate against the full input.
3. Winner = most lines matched, tie-broken by specificity (a Nginx-specific regex beats a generic "timestamp + message" regex).

The library is layered:

| Tier | Specificity | Role |
|------|-------------|------|
| Golden | 88-99 | Exact matches for shapes in the test corpus |
| Curated | 70-99 | Hand-written for specific vendors |
| Bundled | 25-30 | Auto-imported from upstream Logstash / vjeantet packs |
| Catchall | ≤20 | Last-resort generic shapes |

**Mental model:** a **vendor recognizer**. It knows what Nginx looks like, what AWS ALB looks like, what a Java log line looks like.

#### Stage 1 vs Stage 2 at a glance

|  | Stage 1 (Structured) | Stage 2 (Library) |
|---|----------------------|-------------------|
| What it knows | Generic data formats (JSON, CSV, logfmt) | Specific products and services (Nginx, AWS ALB, syslog) |
| How it decides | Rule-based probes ("does it start with `{`?") | Score-based regex match against a curated list |
| Where new entries go | `identify.go` — add a `structuredProbe` | `library_curated.go` — add a `KnownPattern` |
| Output style | Often one big `GREEDYDATA` capture (for JSON) or a literal column shape (for CSV) | Many named captures: `%{IPORHOST:client_ip}`, `%{HTTPDATE:timestamp}`, … |
| Speed | Fast (cheap probes) | Medium (regex match against many candidates) |

#### A worked example

Input:

```text
10.0.0.1 - - [15/Jan/2025:10:23:45 +0000] "GET / HTTP/1.1" 200 1024
10.0.0.2 - - [15/Jan/2025:10:23:46 +0000] "POST /api HTTP/1.1" 201 0
```

- **Stage 1** asks "Is this JSON? CSV? logfmt? CEF?" → no probe says yes → Stage 1 produces nothing.
- **Stage 2** scores its catalog → the Nginx Combined / Apache Common pattern wins with 100% coverage → returns it with named captures for `client_ip`, `timestamp`, `method`, `url`, `status`, `bytes`.

Now flip the input:

```text
{"ts":"2025-01-15T10:23:45Z","level":"info","msg":"hi"}
{"ts":"2025-01-15T10:23:46Z","level":"warn","msg":"oops"}
```

- **Stage 1** asks "Is this JSON?" → yes, every line opens with `{` and closes with `}` → Stage 1 wins with a JSON-shaped pattern. Stage 2's vote is no longer needed.

### Concurrency model

The three stages run **in parallel**. Each stage runs in its own goroutine and writes its diagnostic output to a per-stage `bytes.Buffer`. The coordinator reads the result channels in **stage priority order** (structured → library → drain) and short-circuits the moment a higher-priority stage clears its auto-accept threshold:

```
       ┌─ structured goroutine ─┐
input ─┼─ library    goroutine ─┼─► coordinator: read in priority order
       └─ drain      goroutine ─┘                  • return on first auto-accept
                                                   • else pickBetter across all three
```

Auto-accept follows **stage priority, not finish order**. If structured auto-accepts, its result wins even when library and drain finished first; the coordinator simply returns without consuming their channels.

Properties:

- **Wall-clock latency**: in the no-auto-accept path, total time is `max(structured, library, drain)` instead of their sum.
- **Diagnostics ordering**: per-stage buffers are flushed in priority order, so verbose output keeps the historical `stage1 → stage2 → stage3` shape regardless of which goroutine finished first.
- **Determinism**: identical input always yields the same `DiscoveredPattern`. Goroutines do not race on shared state (each writes to its own buffer); the priority-order read is what fixes the result.
- **No mid-flight cancellation**: drain3 has no cancellation hook, so when structured or library auto-accepts, the drain goroutine still finishes its work in the background. Result channels are buffered (capacity 1), so unread goroutines exit cleanly without leaking.

This shape replaced the previous strictly serial `stage1 → stage2 → stage3` short-circuit chain. The public API (`Discover`, `DiscoveredPattern`, `Options`) and selection semantics are unchanged — only the execution overlap changed.

## Build & Test

```sh
make build      # bin/log2grok
make test       # all unit tests
make bench      # golden corpus + per-case benchmarks
make lint       # go vet + gofmt
```
