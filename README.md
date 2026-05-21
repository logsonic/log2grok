# log2grok

One log file in, one Grok pattern out.

```sh
$ log2grok /var/log/nginx/access.log
%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"
# matched 4823 / 4900 lines (98.4%) -- library:Nginx Access Combined
```

For the full product spec, see [`SPEC.md`](SPEC.md). For the implementation walkthrough and how to extend the library, see [`design.md`](design.md).

## Install

As a CLI:

```sh
go install github.com/logsonic/log2grok/cmd/log2grok@latest
```

As a Go library:

```sh
go get github.com/logsonic/log2grok@latest
```

## Use as a Go library

The public API lives under `github.com/logsonic/log2grok/pkg/log2grok`. Pass the
log lines you want to analyze; get back the single best Grok pattern.

```go
package main

import (
	"fmt"
	"log"

	l2g "github.com/logsonic/log2grok/pkg/log2grok"
)

func main() {
	lines := []string{
		`10.0.0.1 - alice [15/Jan/2025:10:23:45 +0000] "GET /index.html HTTP/1.1" 200 1024`,
		`10.0.0.2 - bob   [15/Jan/2025:10:23:46 +0000] "POST /api HTTP/1.1" 201 0`,
	}

	dp, err := l2g.Discover(lines, l2g.Options{})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("grok:    ", dp.Grok)
	fmt.Println("source:  ", dp.Source)
	fmt.Printf("coverage: %.1f%% (%d / %d lines)\n",
		dp.Coverage*100, dp.MatchedCount, dp.TotalLines)

	re, err := l2g.CompileGrok(dp.Grok, dp.CustomPatterns)
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range re.FindStringSubmatch(lines[0]) {
		fmt.Println("  capture:", m)
	}
}
```

Exported surface: `Discover`, `DiscoverMulti`, `DiscoverTopK`, `Options`,
`DiscoveredPattern`, `MultiPatternResult`, `CompileGrok`, `EvaluateCoverage`,
`LibraryDiagnostics`, `LoadConfig`, `ResetConfig`, `DefaultConfigDirName`,
`ErrEmptyInput`. The `internal/pattern` package is intentionally not
importable per Go's [`internal/`](https://go.dev/ref/spec#Package_paths)
rule — `pkg/log2grok` is the stable API boundary.

## Multi-format logs (`DiscoverMulti`)

Some files mix shapes — application lines, access logs, and syslog all in
one stream. A single Grok can't fit them. `DiscoverMulti` returns an
ordered **set** of standalone patterns chosen by greedy set-cover so their
combined coverage reaches a target (`Options.TargetCoverage`, default
`0.90`):

```sh
$ log2grok --multi mixed.log
%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?" %{INT:status} (?:%{INT:bytes}|-)
\{\s*"ts"\s*:\s*%{QUOTEDSTRING:timestamp}\s*,\s*"level"\s*:\s*%{QUOTEDSTRING:level}\s*,\s*"msg"\s*:\s*%{QUOTEDSTRING:message}\s*\}
# [1] 50.0% -- library:Apache Common
# [2] 50.0% -- structured:Zap JSON
# combined 1000000 / 1000000 lines (100.0%) across 2 patterns
```

Each pattern is an independent first-match-wins rule (the shape a
Logstash/Vector grok list expects), ordered so the first explains the most
lines and each subsequent one explains the most of what remained. How it
works:

- **Candidate pool** = specific library matches (named, vendor-recognized)
  + a structured probe if one fits + every drain cluster rendered into its
  own pattern. The library's generic catchall tier is excluded so a
  "timestamp + GREEDYDATA" rule can't win by matching everything.
- **Greedy selection** repeatedly takes the pattern explaining the most
  still-uncovered lines, stopping when the target is met, when a pattern
  would add less than ~1% (so an irreducible long tail of one-off lines
  doesn't spawn a swarm of overfit patterns), or at 10 patterns.
- `CombinedCoverage` reports what the set actually achieves — if the data
  has, say, 20% unstructured noise, you get the major formats and an honest
  80%, not ten throwaway rules chasing the target.

```go
res, err := l2g.DiscoverMulti(lines, l2g.Options{TargetCoverage: 0.90})
for i, p := range res.Patterns {
    fmt.Printf("[%d] %.1f%% %s\n    %s\n", i+1, p.Coverage*100, p.Source, p.Grok)
}
fmt.Printf("combined %.1f%%\n", res.CombinedCoverage*100)
```

`DiscoverMulti` shares the large-input sampling described below, so it
handles 1M+ line multi-format files within the same bounded cost as
`Discover`.

## Discovery Pipeline

`Discover` runs three independent stages and returns the single best Grok pattern across them. The stages are:

| Stage | Source | Auto-accept threshold |
|-------|--------|-----------------------|
| 1. Structured | `tryStructured` — JSON / logfmt / CEF / W3C / CSV / TSV probes | `coverage ≥ LibraryThreshold` *and* a typed capture (skips the keyless JSON skeleton) |
| 2. Library | `tryLibrary` — curated + bundled regex `KnownPatterns`, sample-then-full scoring | `coverage ≥ LibraryThreshold` (default `0.85`) |
| 3. Drain | `deriveFromDrain` — drain3 clustering → token classifier → render. When the dominant cluster is a poor fit but the input is made of fewer than ~10 distinct shapes, the top clusters are unioned into one alternation (`drain:multi(N)`) | `coverage ≥ 0.85` |

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

## Scaling to large inputs (1M+ lines)

`Discover` accepts inputs of any size — there is no line-count cap. To keep
cost bounded, the two stages whose work would otherwise grow with the input
operate on a deterministic representative sample once the input crosses a
threshold:

- **Coverage estimation** (`coverageEvalCap`, 50k): each candidate regex is
  scored against a fixed-size sample rather than every line. Because every
  candidate is scored on the *same* sample, the relative ranking — and thus
  the chosen pattern — is preserved.
- **Drain training** (`drainTrainCap`, 20k): drain3 learns templates from a
  bounded sample; templates converge well before this many lines, so feeding
  more only grows memory and CPU.

The sample is chosen deterministically (`chooseSample`: a fixed head plus a
strided tail), so identical input always yields an identical result.

When sampling is in effect, the result is flagged accordingly:

| Field | Meaning |
|-------|---------|
| `Estimated` | `true` when coverage figures were extrapolated from a sample |
| `EvalLines` | how many lines coverage was actually measured against |
| `Coverage` | the sampled estimate of the true coverage ratio |
| `MatchedCount` | `Coverage` extrapolated to `TotalLines` |
| `Grok` | always exact — only the coverage *figures* are statistical |

The net effect: a 1M-line file costs roughly the same as a 50k-line file in
both time and allocations. Inputs at or below the caps take the exact,
unsampled path and `Estimated` is `false`.

## Build & Test

```sh
make build      # bin/log2grok
make test       # all unit tests
make bench      # golden corpus + per-case benchmarks
make lint       # go vet + gofmt
```
