# Log2Grok CLI — Implementation Specification

**Version:** 0.7 (CLI-only, broad-format optimized)
**Audience:** This spec is written for a junior Go engineer. It explains not just *what* to build but *why* each decision was made. If anything is unclear, the answer is in §17 (Glossary) or §18 (FAQ).

---

## 1. What You're Building

A command-line program. You give it a log file. It prints one Grok pattern that matches most of the lines in that file.

```sh
$ log2grok /var/log/nginx/access.log
%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"
# matched 4823 / 4900 lines (98.4%) — library:Nginx Access Combined
```

That's the whole product. One binary, one file in, one Grok pattern out.

### What is a Grok pattern?
A Grok pattern is a regex with named placeholders. `%{IPV4:client_ip}` is shorthand for "match an IPv4 address and call the captured value `client_ip`." It's the format used by Logstash, Elastic, Vector, and Fluent Bit to extract fields from log lines.

### Why one pattern, not many?
A log file usually has one dominant shape (e.g., every line is an Nginx access log entry). The user wants one pattern that handles that shape. If their file has multiple shapes mixed together, they should split the file. **Do not produce a list of patterns. Do not produce one pattern per cluster. One file in, one pattern out.** This is the single most important property of the program.

---

## 2. Stack

| Tool | Version |
|---|---|
| Go | 1.22+ |
| Standard library | only this and the dependency below |
| `github.com/axiomhq/drain3` | latest, vendored |

That's all. No web server. No frontend. No database. No other dependencies. If you find yourself reaching for a third library, stop and reread this section.

### Module requirement
This project must compile both as:
- a CLI binary (`cmd/log2grok`), and
- an importable Go module package used by external tests/benchmarks.

Do not expose only `internal/...` APIs. External callers cannot import `internal` packages.

---

## 3. How the Program Decides on a Pattern

The program uses a staged matcher. Cheap, high-confidence checks run first; expensive or generic fallbacks run last. The goal is to match the dominant single-line format in real log files with high coverage, usually ≥ 99% for known formats, while still returning one usable pattern for unknown formats.

### Stage 0 — Normalize and Sample
Before matching anything:
- Drop empty lines from matching calculations and coverage reporting; mention skipped blanks only in verbose diagnostics.
- Strip a UTF-8 BOM from the first line.
- Preserve all other bytes. Do not trim normal log lines; leading spaces can be meaningful.
- Build a deterministic sample of at most 4096 non-empty lines: first 1024 lines plus evenly spaced lines from the rest. Use this for cheap candidate scoring, then verify the winner against all lines.

This keeps large files fast without making the result depend only on the beginning of a file.

### Stage 1 — Structured Format Probes
Some formats are not best discovered by Drain or a flat library regex. Detect these by syntax first:
- JSON object logs: Docker JSON, Kubernetes app JSON, Pino/Bunyan, Zap JSON, ECS JSON, CloudTrail, Suricata EVE, auditd JSON.
- `logfmt` / key-value logs: `ts=... level=info msg="..."`.
- CEF and LEEF security events.
- W3C/IIS logs with `#Fields:` headers.
- CSV/TSV logs with stable column counts.

If a probe matches ≥ 90% of the sample, render a Grok pattern from the observed shape and verify it against all lines. Structured probes should prefer extracting common fields (`timestamp`, `level`, `message`, `logger`, `trace_id`, `span_id`, `request_id`, `client_ip`, `method`, `path`, `status`, `duration`) and then use `%{GREEDYDATA:json}`, `%{GREEDYDATA:kvpairs}`, or `%{GREEDYDATA:fields}` for the remainder.

### Stage 2 — Known Pattern Library
The program ships with a broad built-in dictionary of well-known log formats: web servers, syslog variants, container runtimes, cloud logs, databases, language runtimes, security devices, and common application frameworks. Compile these patterns once, score them against the sample, then fully evaluate only the best candidates.

Do not accept the first match blindly. Pick the highest-scoring specific pattern. Tie-break by:
1. Higher full-file coverage.
2. Higher specificity (`Specificity` field, or lower `Priority` if equal).
3. Fewer `%{GREEDYDATA}` captures.
4. Earlier library order.

Catchalls are useful, but they must never beat a source-specific pattern with similar coverage.

### Stage 3 — Derive From Structure (Drain Fallback)
If no structured probe or library pattern clears the threshold, derive a pattern from the file itself. Use Drain to find the dominant shape, classify variable slots with a rich token catalog, and render a Grok pattern around literal text.

This catches custom application logs, internal services, one-off scripts, and new vendor formats.

### Stage 4 — Last-Resort Safe Pattern
If Drain cannot produce a compiling pattern, return the narrowest safe fallback:
- `%{TIMESTAMP_ISO8601:timestamp}\s+%{GREEDYDATA:message}` if most lines begin with ISO timestamps.
- `%{SYSLOGTIMESTAMP:timestamp}\s+%{GREEDYDATA:message}` if most lines begin with RFC3164 timestamps.
- `%{GREEDYDATA:message}` only as the absolute last resort.

The product must always return exactly one Grok pattern for non-empty input.

---

## 4. CLI Interface

### Synopsis
```
log2grok [flags] <input-file>
log2grok [flags] -            # read from stdin
```

### Flags
| Flag | Default | Purpose |
|---|---|---|
| `-threshold <float>` | `0.85` | Minimum match rate (0.0–1.0) for a library pattern to be accepted. |
| `-max-lines <int>` | `100000` | Stop reading after this many lines. |
| `-verbose` | `false` | Print extra diagnostic info to stderr (which library entries were tried, etc.). |
| `-quiet` | `false` | Suppress the trailing `# matched...` comment. Useful in pipelines. |
| `-h, -help` | — | Show usage and exit. |

### Output Contract
- **stdout** receives exactly two things: the Grok pattern on its own line, then (unless `-quiet`) a single comment line of the form `# matched N / M lines (P%) — <source>`. Nothing else. This is so it's easy to pipe the pattern into other tools.
- **stderr** receives errors, and verbose diagnostics if `-verbose` is set.
- **Exit code** is `0` on success, `1` on usage errors (bad flag, missing file), `2` if no pattern could be produced (shouldn't normally happen — there's always a catch-all fallback).

### Examples
```sh
# Basic use
$ log2grok access.log

# Pipe it somewhere
$ log2grok -quiet access.log > pattern.grok

# From stdin
$ kubectl logs my-pod | log2grok -

# More aggressive library matching
$ log2grok -threshold 0.7 weird.log

# See what it tried
$ log2grok -verbose access.log
```

---

## 5. Project Layout

```
log2grok/
├── cmd/
│   ├── buildpacks/
│   │   └── main.go              # Parses bundled pack snapshots and generates Go files.
│   └── log2grok/
│       └── main.go              # Entry point: flag parsing, file reading, calls Discover, prints output.
├── pkg/
│   └── log2grok/
│       └── api.go               # Public importable API; wraps internal/pattern.
├── internal/
│   └── pattern/
│       ├── discovery.go         # The Discover() function. Top-level orchestrator.
│       ├── identify.go          # Cheap syntax probes: JSON, logfmt, CEF, LEEF, W3C, CSV.
│       ├── structured.go        # Render patterns for structured logs.
│       ├── library.go           # The KnownPatterns []KnownPattern dictionary.
│       ├── score.go             # Candidate scoring, sample selection, specificity tie-breaks.
│       ├── primitives.go        # The GrokPrimitives map[string]string (IPV4, NUMBER, etc.).
│       ├── compile.go           # CompileGrok(): Grok pattern → *regexp.Regexp.
│       ├── drain.go             # Drain adapter (fallback path).
│       ├── tokenize.go          # TokenizeWithSpans(): used by drain.go.
│       ├── classify.go          # ClassifySlot(): used by drain.go.
│       ├── render.go            # Render(): builds a Grok string from fields + sample line.
│       ├── coverage.go          # EvaluateCoverage(): runs a regex against all lines.
│       ├── packs_embedded.go    # Bundled standalone public pattern text snapshots.
│       ├── library_bundled.go   # Generated from bundled pattern packs.
│       ├── primitives_bundled.go # Generated primitive map from bundled packs.
│       └── overrides/
│           ├── known/           # Local known-pattern overlays.
│           └── primitives/      # Local primitive overlays.
├── testdata/
│   └── golden/
│       ├── nginx_access_combined/
│       │   ├── input.log
│       │   └── expected.txt     # Expected stdout
│       ├── syslog_rfc3164/
│       └── ... (30+ corpora total)
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

**File-by-file rule of thumb**: keep logic files in `internal/pattern/` and `pkg/log2grok/` small (< 200 lines). `library.go` may be larger because it is data. If any non-library file grows past that, split it. The names tell you what they do — keep them honest.

---

## 6. The Public Go API

These are the function signatures the agent must implement for the **importable module API**. Other helpers can remain internal.

```go
// pkg/log2grok/api.go
package log2grok

// DiscoveredPattern is what Discover returns. ONE per call. Not a list.
type DiscoveredPattern struct {
    Source       string  // "library:Nginx Access Combined", "structured:JSON Object", "drain", or "fallback"
    Grok         string  // the rendered Grok pattern
    Coverage     float64 // 0.0–1.0
    MatchedCount int
    TotalLines   int
}

// Options controls Discover's behavior.
type Options struct {
    LibraryThreshold float64 // default 0.85; specific library patterns must clear this to win
    Verbose          bool    // if true, log diagnostics to the provided writer
}

// Discover returns the single best Grok pattern for the input lines.
// If lines is empty, returns an error.
func Discover(lines []string, opts Options) (*DiscoveredPattern, error)

// CompileGrok expands all %{NAME} references using GrokPrimitives plus any
// extras, then compiles the result as an anchored Go regexp. Anchoring is
// automatic — callers pass the raw Grok pattern, not anchored.
//
//   re, err := CompileGrok(`%{IPV4:ip} %{WORD:verb}`, nil)
func CompileGrok(pattern string, extras map[string]string) (*regexp.Regexp, error)

// EvaluateCoverage runs re against every line; returns count of matches.
func EvaluateCoverage(re *regexp.Regexp, lines []string) int
```

`pkg/log2grok` should delegate to `internal/pattern`, so implementation logic stays internal while the API remains importable from tests and benchmarks in other modules.

Example external import:

```go
import "log2grok/pkg/log2grok"
```

Everything else (the library entries, the Drain adapter internals, the renderer) stays in `internal/pattern`. Junior engineers: keep the export surface small. If something doesn't need to be called from CLI or external tests, lowercase its name.

---

## 7. Algorithm: `Discover` Step by Step

This is the function the whole program revolves around. Read this carefully.

```go
// internal/pattern/discovery.go
func Discover(lines []string, opts Options) (*DiscoveredPattern, error) {
    normalized := normalizeLines(lines)
    if len(normalized.MatchLines) == 0 {
        return nil, errors.New("no input lines")
    }

    threshold := opts.LibraryThreshold
    if threshold <= 0 {
        threshold = 0.85
    }

    sample := chooseSample(normalized.MatchLines, 4096)

    // STEP 1 — Syntax-driven structured probes.
    if dp := tryStructured(sample, normalized.MatchLines); dp != nil && dp.Coverage >= 0.90 {
        return dp, nil
    }

    // STEP 2 — Score the broad known-format library.
    if dp := tryLibrary(sample, normalized.MatchLines, threshold); dp != nil {
        return dp, nil
    }

    // STEP 3 — Fallback: derive from the dominant Drain cluster.
    if dp, err := deriveFromDrain(normalized.MatchLines); err == nil && dp != nil && dp.Grok != "" {
        return dp, nil
    }

    // STEP 4 — Always return one safe pattern.
    return deriveSafeFallback(normalized.MatchLines), nil
}

type normalizedInput struct {
    MatchLines   []string // non-empty lines used for detection
    OriginalSize int      // original len(lines), including blanks
    BlankCount   int
}

func normalizeLines(lines []string) normalizedInput {
    out := normalizedInput{OriginalSize: len(lines)}
    for i, line := range lines {
        if i == 0 {
            line = strings.TrimPrefix(line, "\ufeff")
        }
        if line == "" {
            out.BlankCount++
            continue
        }
        out.MatchLines = append(out.MatchLines, line)
    }
    return out
}

// tryStructured checks JSON, logfmt, CEF, LEEF, W3C/IIS, CSV, and TSV.
// It returns a fully evaluated DiscoveredPattern, never a sample-only result.
func tryStructured(sample, all []string) *DiscoveredPattern {
    for _, probe := range structuredProbes {
        if !probe.Likely(sample) {
            continue
        }
        grok, source, ok := probe.Render(sample)
        if !ok {
            continue
        }
        re, err := CompileGrok(grok, nil)
        if err != nil {
            continue
        }
        matched := EvaluateCoverage(re, all)
        coverage := float64(matched) / float64(len(all))
        if coverage >= 0.90 {
            return &DiscoveredPattern{
                Source:       source,
                Grok:         grok,
                Coverage:     coverage,
                MatchedCount: matched,
                TotalLines:   len(all),
            }
        }
    }
    return nil
}

// tryLibrary compiles the library once, scores every pattern on the sample,
// fully evaluates only plausible winners, and returns the best specific match.
func tryLibrary(sample, all []string, threshold float64) *DiscoveredPattern {
    candidates := scoreLibraryOnSample(sample)
    candidates = keepTopCandidates(candidates, 12)

    var best *candidateResult
    for _, c := range candidates {
        if c.SampleCoverage < 0.50 {
            continue // not plausible enough to spend full-file work on
        }
        matched := EvaluateCoverage(c.Compiled, all)
        fullCoverage := float64(matched) / float64(len(all))
        if fullCoverage < threshold {
            continue
        }
        result := &candidateResult{
            Pattern:      c.Pattern,
            FullCoverage: fullCoverage,
            Matched:      matched,
        }
        if betterCandidate(result, best) {
            best = result
        }
    }
    if best == nil {
        return nil
    }
    return &DiscoveredPattern{
        Source:       "library:" + best.Pattern.Name,
        Grok:         best.Pattern.Pattern,
        Coverage:     best.FullCoverage,
        MatchedCount: best.Matched,
        TotalLines:   len(all),
    }
}

// deriveFromDrain runs Drain, uses the largest cluster as the source of one
// output pattern, classifies slots, renders Grok, and reports full coverage.
func deriveFromDrain(lines []string) (*DiscoveredPattern, error) {
    clusters, err := trainDrain(lines)
    if err != nil {
        return nil, err
    }
    if len(clusters) == 0 {
        return nil, errors.New("drain produced no clusters")
    }
    dominant := clusters[0] // use one dominant shape only

    sample := lines[dominant.SampleLineIdx]
    slots := resolveSlots(dominant, lines)
    fields := autoFieldsFromSlots(slots, sample, dominant)
    grok := Render(sample, fields, slots)

    re, err := CompileGrok(grok, nil)
    if err != nil {
        return nil, fmt.Errorf("rendered pattern failed to compile: %w", err)
    }
    matched := EvaluateCoverage(re, lines)

    return &DiscoveredPattern{
        Source:       "drain",
        Grok:         grok,
        Coverage:     float64(matched) / float64(len(lines)),
        MatchedCount: matched,
        TotalLines:   len(lines),
    }, nil
}

func deriveSafeFallback(lines []string) *DiscoveredPattern {
    candidates := []struct {
        Source string
        Grok   string
    }{
        {"fallback:ISO Timestamp", `%{TIMESTAMP_ISO8601:timestamp}\s+%{GREEDYDATA:message}`},
        {"fallback:Syslog Timestamp", `%{SYSLOGTIMESTAMP:timestamp}\s+%{GREEDYDATA:message}`},
        {"fallback:Message", `%{GREEDYDATA:message}`},
    }
    for _, c := range candidates {
        re, err := CompileGrok(c.Grok, nil)
        if err != nil {
            continue
        }
        matched := EvaluateCoverage(re, lines)
        coverage := float64(matched) / float64(len(lines))
        if coverage >= 0.80 || c.Source == "fallback:Message" {
            return &DiscoveredPattern{
                Source:       c.Source,
                Grok:         c.Grok,
                Coverage:     coverage,
                MatchedCount: matched,
                TotalLines:   len(lines),
            }
        }
    }
    panic("unreachable: fallback message pattern must compile")
}
```

Notice what's preserved:
- `Discover` still returns one `DiscoveredPattern`.
- Structured probes and the library may evaluate many candidates internally, but only one pattern is returned.
- Drain still contributes one dominant-shape pattern, not one pattern per cluster.

### Performance rules
- Compile `KnownPatterns` once with `sync.Once`; never compile every pattern for every `Discover` call.
- Score all library patterns on the bounded sample, then full-evaluate only the top 12 candidates.
- Stop evaluating a candidate early if it cannot mathematically reach the current best coverage.
- Keep regexes anchored through `CompileGrok`; do not put `^` or `$` inside library patterns.
- Avoid pathological `.*` in the middle of patterns. Use `%{DATA}` only for quoted fields or bounded sections, and `%{GREEDYDATA}` only at the natural message tail.
- In verbose mode, print the top rejected candidates with sample coverage and full coverage so bad ordering is easy to debug.

### Candidate scoring
The scoring code lives in `internal/pattern/score.go`.

```go
type compiledPattern struct {
    Pattern KnownPattern
    Regex   *regexp.Regexp
}

var (
    compileOnce sync.Once
    compiledLib []compiledPattern
    compileErr  error
)

func compiledKnownPatterns() []compiledPattern {
    compileOnce.Do(func() {
        compiledLib = make([]compiledPattern, 0, len(KnownPatterns))
        for _, kp := range KnownPatterns {
            re, err := CompileGrok(kp.Pattern, kp.CustomPatterns)
            if err != nil {
                compileErr = err
                continue
            }
            compiledLib = append(compiledLib, compiledPattern{Pattern: kp, Regex: re})
        }
    })
    return compiledLib
}

type candidateResult struct {
    Pattern        KnownPattern
    Compiled       *regexp.Regexp
    SampleCoverage float64
    FullCoverage   float64
    Matched        int
}

func scoreLibraryOnSample(sample []string) []candidateResult {
    compiled := compiledKnownPatterns()
    out := make([]candidateResult, 0, len(compiled))
    for _, cp := range compiled {
        matched := EvaluateCoverage(cp.Regex, sample)
        out = append(out, candidateResult{
            Pattern:        cp.Pattern,
            Compiled:       cp.Regex,
            SampleCoverage: float64(matched) / float64(len(sample)),
            Matched:        matched,
        })
    }
    return out
}

func betterCandidate(next, best *candidateResult) bool {
    if best == nil {
        return true
    }
    if next.FullCoverage != best.FullCoverage {
        return next.FullCoverage > best.FullCoverage
    }
    if next.Pattern.Specificity != best.Pattern.Specificity {
        return next.Pattern.Specificity > best.Pattern.Specificity
    }
    nextGreedy := strings.Count(next.Pattern.Pattern, "%{GREEDYDATA")
    bestGreedy := strings.Count(best.Pattern.Pattern, "%{GREEDYDATA")
    if nextGreedy != bestGreedy {
        return nextGreedy < bestGreedy
    }
    return next.Pattern.Priority < best.Pattern.Priority
}

func keepTopCandidates(in []candidateResult, n int) []candidateResult {
    sort.SliceStable(in, func(i, j int) bool {
        if in[i].SampleCoverage != in[j].SampleCoverage {
            return in[i].SampleCoverage > in[j].SampleCoverage
        }
        if in[i].Pattern.Specificity != in[j].Pattern.Specificity {
            return in[i].Pattern.Specificity > in[j].Pattern.Specificity
        }
        return in[i].Pattern.Priority < in[j].Pattern.Priority
    })
    if len(in) > n {
        return in[:n]
    }
    return in
}

func chooseSample(lines []string, max int) []string {
    if len(lines) <= max {
        return append([]string(nil), lines...)
    }
    out := make([]string, 0, max)
    first := min(1024, max/2)
    out = append(out, lines[:first]...)
    remaining := max - len(out)
    step := float64(len(lines)-first) / float64(remaining)
    for i := 0; i < remaining; i++ {
        idx := first + int(float64(i)*step)
        if idx >= len(lines) {
            idx = len(lines) - 1
        }
        out = append(out, lines[idx])
    }
    return out
}
```

---

## 8. The Library (`KnownPatterns`)

This is the main accuracy lever. Each entry is one well-known log format or one narrow family variant. The broad-format goal is not one magical regex; it is a large, ordered set of source-specific patterns plus good structured probes.

```go
// internal/pattern/library.go
package pattern

type KnownPattern struct {
    Name           string
    Pattern        string            // Grok pattern (full line, will be auto-anchored)
    Priority       int               // lower = evaluated first in ties
    Specificity    int               // higher = prefer over generic patterns at same coverage
    Description    string
    CustomPatterns map[string]string // extra %{NAME} primitives this pattern needs
}
```

### Required bundled source packs
`KnownPatterns` and `GrokPrimitives` must be built from **bundled pattern packs in this repository**, not from runtime URL fetches. The spec must be self-contained.

| Source pack | Location | Must ingest |
|---|---|---|
| `logstash_ecs_v1` snapshot | `internal/pattern/packs/logstash_ecs_v1.pattern` | all primitive and top-level pattern definitions |
| `logstash_legacy` snapshot | `internal/pattern/packs/logstash_legacy.pattern` | all primitive and top-level pattern definitions |
| `vjeantet_core` snapshot | `internal/pattern/packs/vjeantet_core.pattern` | all primitive and top-level pattern definitions |
| `fluentd_grok` snapshot | `internal/pattern/packs/fluentd_grok.pattern` | all bundled definitions if available |
| Local project overlays | `internal/pattern/overrides/primitives/*.pattern`, `internal/pattern/overrides/known/*.pattern` | project-specific fixes and additions only |

Bundled source packs are the default source of truth for publicly available formats. Local overlays are only for:
- RE2 compatibility fixes.
- Field-name normalization (`clientip` -> `client_ip`, etc.).
- Missing modern formats not yet in the bundled snapshots.

### Required v1 catalog (minimum acceptance set)
Even with full bundled-pack ingestion, explicitly test this baseline catalog. A "structured probe" means the format is better parsed by syntax first, but it still needs golden examples and coverage tests.

| Family | Required formats |
|---|---|
| Web access | Nginx combined, Nginx main with request time/upstream time, Apache common, Apache combined, Apache vhost combined, Caddy access, Traefik access, Envoy access, Morgan common/combined/dev |
| Web error | Nginx error, Apache error, Caddy error, Envoy text error |
| Windows/web tabular | IIS/W3C extended with `#Fields:` header, Azure App Gateway W3C |
| Load balancers/CDN | HAProxy HTTP, HAProxy TCP, AWS CLB, AWS ALB, AWS NLB, CloudFront standard, Cloudflare HTTP request, Fastly/Varnish common |
| AWS service logs | VPC Flow Logs v2/v3/v5, Route53 resolver query, S3 server access, WAF JSON, CloudTrail JSON |
| GCP/Azure | GCP Cloud Load Balancing JSON, GCP audit JSON, Azure Activity JSON, Azure Firewall key-value |
| Syslog/system | RFC3164 syslog, RFC5424 syslog, systemd/journald short, dmesg/kernel, sshd, sudo, cron, auditd key-value |
| Containers/Kubernetes | Docker JSON log, CRI/containerd text, Kubernetes klog, Kubernetes audit JSON, ECS/Fargate JSON |
| Java/JVM | Log4j pattern layout, Logback pattern layout, Tomcat/Catalina, Spring Boot default, Elasticsearch, Kafka, ZooKeeper, Cassandra |
| Language/framework | Python logging, Django, Gunicorn access/error, Go standard log, Go `slog` text, Zap console, Serilog text, Rails, Node Pino JSON, Bunyan JSON, Winston text, PM2 |
| Databases/cache | PostgreSQL, MySQL error, MySQL slow query single-line headers, Redis, MongoDB text, MongoDB JSON, SQL Server text export |
| Messaging/infra | RabbitMQ, NATS, Consul, Vault audit JSON, Nomad, Prometheus, Grafana |
| Security/network | CEF, LEEF, Suricata EVE JSON, Zeek TSV, Snort fast alert, Cisco ASA, Palo Alto threat CSV, Fortinet key-value, Juniper SRX, pfSense, iptables/UFW, Squid access |
| Generic structured | JSON object, logfmt, `key=value` pairs, CSV, TSV |
| Generic text | ISO timestamp + level + logger + message, ISO timestamp + level + message, bracketed timestamp, syslog timestamp + message, bare level + message |

### Bundled ingest contract
The library implementation should include a loader/generator that reads bundled pattern text and emits compiled Go tables. Do not hand-maintain hundreds of entries.

```go
// internal/pattern/packs_embedded.go
package pattern

type patternPack struct {
    Name          string
    Flavor        string // "ecs-v1", "legacy", "vjeantet", "fluentd"
    PatternText   string // newline-separated "NAME REGEX" definitions
}

var BuiltinPatternPacks = []patternPack{
    {
        Name:   "logstash_ecs_v1",
        Flavor: "ecs-v1",
        PatternText: `
IPORHOST (?:%{IP}|%{HOSTNAME})
HTTPD_COMMONLOG %{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?" %{INT:status} (?:%{INT:bytes}|-)
HTTPD_COMBINEDLOG %{HTTPD_COMMONLOG} "%{DATA:referrer}" "%{DATA:user_agent}"
HAPROXYHTTP %{IP:client_ip}:%{INT:client_port} \[%{HTTPDATE:timestamp}\] %{NOTSPACE:frontend_name} %{NOTSPACE:backend_name}/%{NOTSPACE:server_name} %{INT:tq}/%{INT:tw}/%{INT:tc}/%{INT:tr}/%{INT:tt} %{INT:status} %{INT:bytes_read} %{NOTSPACE:req_cookie} %{NOTSPACE:res_cookie} %{NOTSPACE:termination_state} %{INT:actconn}/%{INT:feconn}/%{INT:beconn}/%{INT:srvconn}/%{INT:retries} %{INT:srv_queue}/%{INT:backend_queue} "%{DATA:raw_request}"
SYSLOGBASE %{SYSLOGTIMESTAMP:timestamp} %{SYSLOGHOST:hostname} %{PROG:program}(?:\[%{POSINT:pid}\])?:
CISCOFW106001 %{SYSLOGBASE} access-list %{NOTSPACE:acl} %{WORD:action} %{WORD:protocol} %{NOTSPACE:src_interface}/%{IP:src_ip}\(%{INT:src_port}\) -> %{NOTSPACE:dst_interface}/%{IP:dst_ip}\(%{INT:dst_port}\)
MONGO3_LOG %{TIMESTAMP_ISO8601:timestamp} \[%{DATA:component}\] %{GREEDYDATA:message}
RAILS3 %{TIMESTAMP_ISO8601:timestamp} %{LOGLEVEL:level}\s+%{GREEDYDATA:message}
REDISLOG %{INT:pid}:%{WORD:role} %{MONTHDAY} %{MONTH} %{YEAR} %{TIME} %{DATA:level} %{GREEDYDATA:message}
`,
    },
    {
        Name:   "vjeantet_core",
        Flavor: "vjeantet",
        PatternText: `
AWSALB %{WORD:type} %{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:elb_name} %{IP:client_ip}:%{INT:client_port} (?:%{IP:target_ip}:%{INT:target_port}|-) %{NUMBER:req_time} %{NUMBER:target_time} %{NUMBER:resp_time} %{INT:elb_status} (?:%{INT:target_status}|-) %{INT:received_bytes} %{INT:sent_bytes} "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" "%{DATA:user_agent}" %{GREEDYDATA:tail}
SQUID3 %{NUMBER:duration} %{IP:client_ip} %{WORD:cache_result}/%{INT:status} %{INT:bytes} %{WORD:method} %{NOTSPACE:url} %{NOTSPACE:user} %{NOTSPACE:hierarchy}/%{IPORHOST:server} %{NOTSPACE:content_type}
BRO_CONN %{NUMBER:ts}\t%{NOTSPACE:uid}\t%{IP:orig_h}\t%{INT:orig_p}\t%{IP:resp_h}\t%{INT:resp_p}\t%{WORD:proto}\t%{GREEDYDATA:rest}
JUNOSRT_FLOW %{MONTH} %{MONTHDAY} %{TIME} %{HOSTNAME:hostname} %{DATA:process}\[%{INT:pid}\]: %{GREEDYDATA:message}
NAGIOSLOGLINE %{TIMESTAMP_ISO8601:timestamp}\s+\[%{WORD:facility}\]\s+%{GREEDYDATA:message}
`,
    },
}
```

The loader/generator must:
1. Parse each `NAME REGEX` primitive definition.
2. Build a primitive dependency graph.
3. Convert unsupported constructs to RE2-safe equivalents or mark them unsupported.
4. Expand source-specific top-level patterns into `KnownPattern` entries.
5. Deduplicate near-identical patterns by normalized regex and source tag.
6. Emit `GrokPrimitivesBundled` and `KnownPatternsBundled`.

### Representative entries
Use these as style examples and local overrides. The real `KnownPatterns` shipped by the binary should be:
- `KnownPatternsBundled` (all ingested bundled public patterns that compile under RE2),
- then curated project overrides,
- then generic catchalls.

Source-specific entries should appear before generic entries and should avoid `%{GREEDYDATA}` until the natural message tail.

```go
var KnownPatterns = []KnownPattern{
    // Web access logs
    {
        Name:        "Nginx Access Combined",
        Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`,
        Priority:    10,
        Specificity: 90,
    },
    {
        Name:        "Nginx Access Main With Timing",
        Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}" "%{DATA:x_forwarded_for}" rt=%{NUMBER:request_time} uct="%{DATA:upstream_connect_time}" uht="%{DATA:upstream_header_time}" urt="%{DATA:upstream_response_time}"`,
        Priority:    11,
        Specificity: 98,
    },
    {
        Name:        "Nginx Error",
        Pattern:     `%{YEAR}/%{MONTHNUM}/%{MONTHDAY} %{TIME:time} \[%{LOGLEVEL:level}\] %{INT:pid}#%{INT:tid}: (?:\*%{INT:connection} )?%{GREEDYDATA:message}`,
        Priority:    12,
        Specificity: 95,
    },
    {
        Name:        "Apache Combined",
        Pattern:     `%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url}(?: HTTP/%{NUMBER:http_version})?" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"`,
        Priority:    13,
        Specificity: 90,
    },
    {
        Name:        "Apache Error",
        Pattern:     `\[%{APACHE_ERROR_TIME:timestamp}\] \[%{DATA:module}:%{LOGLEVEL:level}\](?: \[pid %{INT:pid}(?::tid %{INT:tid})?\])?(?: \[client %{IPORHOST:client_ip}:%{INT:client_port}\])? %{GREEDYDATA:message}`,
        Priority:    14,
        Specificity: 95,
        CustomPatterns: map[string]string{
            "APACHE_ERROR_TIME": `[A-Za-z]{3} [A-Za-z]{3}\s+\d{1,2} %{TIME}(?:\.\d+)? \d{4}`,
        },
    },

    // System logs
    {
        Name:        "Syslog RFC3164",
        Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} %{PROG:program}(?:\[%{POSINT:pid}\])?: %{GREEDYDATA:message}`,
        Priority:    30,
        Specificity: 75,
    },
    {
        Name:        "Syslog RFC5424",
        Pattern:     `<%{POSINT:priority}>%{NONNEGINT:version} %{TIMESTAMP_ISO8601:timestamp} %{HOSTNAME:hostname} %{NOTSPACE:app_name} %{NOTSPACE:proc_id} %{NOTSPACE:msg_id} (?:-|%{SYSLOG5424SD:structured_data}) ?%{GREEDYDATA:message}`,
        Priority:    31,
        Specificity: 90,
    },
    {
        Name:        "SSH Authentication",
        Pattern:     `%{SYSLOGTIMESTAMP:timestamp} %{HOSTNAME:hostname} sshd\[%{POSINT:pid}\]: %{GREEDYDATA:message}`,
        Priority:    32,
        Specificity: 95,
    },
    {
        Name:        "Auditd Key Value",
        Pattern:     `type=%{WORD:audit_type} msg=audit\(%{NUMBER:audit_epoch}:%{INT:audit_id}\): %{GREEDYDATA:kvpairs}`,
        Priority:    33,
        Specificity: 95,
    },

    // Load balancers and cloud edge
    {
        Name:        "HAProxy HTTP",
        Pattern:     `%{IP:client_ip}:%{INT:client_port} \[%{HTTPDATE:timestamp}\] %{NOTSPACE:frontend_name} %{NOTSPACE:backend_name}/%{NOTSPACE:server_name} %{INT:t_request}/%{INT:t_queue}/%{INT:t_connect}/%{INT:t_response}/%{INT:t_total} %{INT:status} %{INT:bytes_read} %{NOTSPACE:req_cookie} %{NOTSPACE:resp_cookie} %{NOTSPACE:termination_state} %{INT:actconn}/%{INT:feconn}/%{INT:beconn}/%{INT:srvconn}/%{INT:retries} %{INT:srv_queue}/%{INT:backend_queue} "(?:%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}|%{DATA:raw_request})"`,
        Priority:    50,
        Specificity: 98,
    },
    {
        Name:        "AWS ALB",
        Pattern:     `%{WORD:type} %{TIMESTAMP_ISO8601:timestamp} %{NOTSPACE:elb_name} %{IP:client_ip}:%{INT:client_port} (?:%{IP:target_ip}:%{INT:target_port}|-) %{NUMBER:request_processing_time} %{NUMBER:target_processing_time} %{NUMBER:response_processing_time} %{INT:elb_status_code} (?:%{INT:target_status_code}|-) %{INT:received_bytes} %{INT:sent_bytes} "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" "%{DATA:user_agent}" %{NOTSPACE:ssl_cipher} %{NOTSPACE:ssl_protocol} %{NOTSPACE:target_group_arn} "%{DATA:trace_id}" "%{DATA:domain_name}" "%{DATA:chosen_cert_arn}" %{INT:matched_rule_priority} %{TIMESTAMP_ISO8601:request_creation_time} "%{DATA:actions_executed}" "%{DATA:redirect_url}" "%{DATA:error_reason}" "%{DATA:target_port_list}" "%{DATA:target_status_code_list}" "%{DATA:classification}" "%{DATA:classification_reason}"`,
        Priority:    51,
        Specificity: 99,
    },
    {
        Name:        "AWS VPC Flow Logs",
        Pattern:     `%{INT:version} %{NOTSPACE:account_id} %{NOTSPACE:interface_id} %{IP:src_addr} %{IP:dst_addr} %{INT:src_port} %{INT:dst_port} %{INT:protocol} %{INT:packets} %{INT:bytes} %{INT:start} %{INT:end} %{WORD:action} %{WORD:log_status}(?: %{GREEDYDATA:extra_fields})?`,
        Priority:    52,
        Specificity: 98,
    },

    // Application logs
    {
        Name:        "Log4j Logback",
        Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+\[%{DATA:thread}\]\s+%{JAVACLASS:logger}\s+-\s+%{GREEDYDATA:message}`,
        Priority:    80,
        Specificity: 90,
        CustomPatterns: map[string]string{
            "JAVACLASS": `(?:[A-Za-z0-9_$]+\.)*[A-Za-z0-9_$]+`,
        },
    },
    {
        Name:        "Spring Boot Default",
        Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{INT:pid}\s+---\s+\[%{DATA:thread}\]\s+%{JAVACLASS:logger}\s+:\s+%{GREEDYDATA:message}`,
        Priority:    81,
        Specificity: 95,
        CustomPatterns: map[string]string{
            "JAVACLASS": `(?:[A-Za-z0-9_$]+\.)*[A-Za-z0-9_$]+`,
        },
    },
    {
        Name:        "Python Logging",
        Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{NOTSPACE:logger}\s+%{GREEDYDATA:message}`,
        Priority:    82,
        Specificity: 85,
    },

    // Databases
    {
        Name:        "PostgreSQL",
        Pattern:     `%{TIMESTAMP_ISO8601:timestamp}(?: %{TZ:timezone})? \[%{POSINT:pid}\](?: %{DATA:user_db})? %{LOGLEVEL:level}:\s+%{GREEDYDATA:message}`,
        Priority:    110,
        Specificity: 92,
    },
    {
        Name:        "Redis",
        Pattern:     `%{INT:pid}:%{WORD:role} %{MONTHDAY:day} %{MONTH:month} %{YEAR:year} %{TIME:time} %{REDISLEVEL:level} %{GREEDYDATA:message}`,
        Priority:    111,
        Specificity: 90,
        CustomPatterns: map[string]string{
            "REDISLEVEL": `[.\-*#]`,
        },
    },

    // Catchalls — must be last and low-specificity.
    {
        Name:        "Generic ISO Timestamp Level Logger Message",
        Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{NOTSPACE:logger}\s+%{GREEDYDATA:message}`,
        Priority:    900,
        Specificity: 20,
    },
    {
        Name:        "Generic ISO Timestamp Level Message",
        Pattern:     `%{TIMESTAMP_ISO8601:timestamp}\s+%{LOGLEVEL:level}\s+%{GREEDYDATA:message}`,
        Priority:    910,
        Specificity: 10,
    },
    {
        Name:        "Generic Bracketed Timestamp",
        Pattern:     `\[?%{TIMESTAMP_ISO8601:timestamp}\]?\s+%{GREEDYDATA:message}`,
        Priority:    920,
        Specificity: 5,
    },
}
```

Final composition:

```go
var KnownPatterns = append(
    append([]KnownPattern{}, KnownPatternsBundled...),
    KnownPatternsOverrides...,
)
KnownPatterns = append(KnownPatterns, KnownPatternsCatchall...)
sortKnownPatterns(KnownPatterns) // priority asc, specificity desc
```

### Adding new entries
Adding a new format should usually happen through bundled-pack ingestion, not manual editing. Keep entries sorted by `Priority`. Use `Specificity` to prevent generic catchalls from beating source-specific entries at similar coverage. Every new source gets:
- A generated `KnownPattern` entry, or a local `KnownPatternsOverrides` entry, or a structured probe.
- A sample file in `testdata/library_examples/<slug>.txt`.
- A golden corpus if the format is common enough to regress accidentally.

### Why the catalog is broad
Most real logs are not random. They come from a smaller set of emitters: web servers, syslog daemons, cloud services, containers, databases, security products, and language logging libraries. Cover those families explicitly, handle structured syntaxes generically, and let Drain handle the long tail. That combination is what gets close to "works on almost every single-line log file" without returning useless `%{GREEDYDATA}` for everything.

### Structured probes
Structured probes live beside the library because they produce the same output type: one Grok pattern.

```go
type structuredProbe struct {
    Name   string
    Likely func(sample []string) bool
    Render func(sample []string) (grok string, source string, ok bool)
}

var structuredProbes = []structuredProbe{
    dockerJSONProbe,
    pinoJSONProbe,
    bunyanJSONProbe,
    zapJSONProbe,
    ecsJSONProbe,
    cloudTrailJSONProbe,
    suricataEVEProbe,
    kubernetesAuditJSONProbe,
    jsonProbe,
    logfmtProbe,
    cefProbe,
    leefProbe,
    w3cIISProbe,
    csvProbe,
    tsvProbe,
}
```

Rules for probes:
- JSON: if ≥ 90% of sample lines are JSON objects, parse with `encoding/json`. If the same top-level keys appear in ≥ 80% of JSON lines, render a stable JSON skeleton with classified values. If keys vary heavily, return `\{%{GREEDYDATA:json}\}` and source `structured:JSON Object`.
- Docker JSON: detect keys `log`, `stream`, `time`; render `\{"log":%{QUOTEDSTRING:log},"stream":%{QUOTEDSTRING:stream},"time":%{QUOTEDSTRING:timestamp}\}` with optional spacing support if observed.
- Pino/Bunyan/Zap/ECS: detect common keys (`time`, `timestamp`, `level`, `levelname`, `msg`, `message`, `logger`, `trace_id`, `span_id`) and emit captures for those fields, leaving unknown fields inside `%{GREEDYDATA:json_extra}`.
- logfmt/key-value: if ≥ 70% of tokens look like `key=value`, render `%{GREEDYDATA:kvpairs}` unless common leading keys are stable. Prefer captures for `time`, `ts`, `level`, `msg`, `logger`, `trace_id`, `span_id`, `method`, `path`, `status`, `duration`.
- CEF: detect `CEF:<version>|vendor|product|version|signature|name|severity|extension`; render the header fields plus `%{GREEDYDATA:extensions}`.
- LEEF: detect `LEEF:<version>|vendor|product|version|event_id|...`; render the header fields plus `%{GREEDYDATA:extensions}`.
- W3C/IIS: find the most recent `#Fields:` header, map each column to a Grok type, and render one space-separated pattern. Ignore other `#` comment lines for coverage.
- CSV/TSV: require a stable delimiter count in ≥ 95% of sample rows. If there is a header row, use sanitized header names. Otherwise use `field_1`, `field_2`, etc. Quote-aware parsing must use `encoding/csv`, never `strings.Split`.

Do not use ad hoc string parsing for JSON or CSV. Use `encoding/json` and `encoding/csv` from the standard library.

---

## 9. The Primitives Table

`%{IPV4}`, `%{NUMBER}`, etc. — these are the building blocks that library patterns reference. Every entry must compile under Go's RE2 (no PCRE features like atomic groups or backreferences).

For broad public coverage, primitives should be layered:
1. `GrokPrimitivesBundled`: generated from all bundled source packs.
2. `GrokPrimitivesOverrides`: local fixes/aliases for RE2 and naming consistency.
3. `GrokPrimitives`: merged effective table used by `CompileGrok`.

Do not hand-edit generated primitive files.

```go
// internal/pattern/primitives.go
package pattern

var GrokPrimitives = map[string]string{
    // Generic text
    "WORD":              `\w+`,
    "NOTSPACE":          `\S+`,
    "SPACE":             `\s*`,
    "DATA":              `.*?`,
    "GREEDYDATA":        `.*`,
    "QUOTEDSTRING":      `"(?:\\.|[^"\\])*"`,
    "QS":                `%{QUOTEDSTRING}`,

    // Numeric
    "INT":               `[+-]?\d+`,
    "NONNEGINT":         `\d+`,
    "POSINT":            `[1-9]\d*`,
    "NUMBER":            `-?\d+(?:\.\d+)?`,
    "BASE10NUM":         `[+-]?(?:\d+(?:\.\d+)?|\.\d+)`,
    "BASE16NUM":         `(?:0[xX])?[0-9A-Fa-f]+`,
    "FLOAT":             `[+-]?(?:\d+(?:\.\d+)?|\.\d+)(?:[eE][+-]?\d+)?`,
    "BOOLEAN":           `(?i:true|false|yes|no|on|off)`,
    "POSREAL":           `(?:[0-9]*\.[0-9]+|[0-9]+)`,

    // Identifiers & names
    "NOTEMPTY":          `.+`,
    "UUIDURN":           `urn:uuid:%{UUID}`,
    "URN":               `(?:[A-Za-z][A-Za-z0-9-]{0,31}):[^\s]+`,
    "USER":              `[a-zA-Z0-9._-]+`,
    "PROG":              `[A-Za-z0-9._/%-]+`,
    "PROGNAME":          `[A-Za-z0-9._-]+`,
    "PROCID":            `[A-Za-z0-9._-]+`,
    "THREAD":            `[A-Za-z0-9._#-]+`,
    "LOGGER":            `[A-Za-z0-9_.$-]+`,
    "EMAILADDRESS":      `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`,
    "UUID":              `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
    "TRACEID":           `[0-9a-fA-F]{16,32}`,
    "SPANID":            `[0-9a-fA-F]{16}`,

    // Network
    "IPV4":              `(?:(?:25[0-5]|2[0-4]\d|[01]?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d?\d)`,
    "IPV6":              `(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,7}:|(?:[0-9A-Fa-f]{1,4}:){1,6}:[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,5}(?::[0-9A-Fa-f]{1,4}){1,2}|(?:[0-9A-Fa-f]{1,4}:){1,4}(?::[0-9A-Fa-f]{1,4}){1,3}|(?:[0-9A-Fa-f]{1,4}:){1,3}(?::[0-9A-Fa-f]{1,4}){1,4}|(?:[0-9A-Fa-f]{1,4}:){1,2}(?::[0-9A-Fa-f]{1,4}){1,5}|[0-9A-Fa-f]{1,4}:(?:(?::[0-9A-Fa-f]{1,4}){1,6})|:(?:(?::[0-9A-Fa-f]{1,4}){1,7}|:)`,
    "IP":                `(?:%{IPV6}|%{IPV4})`,
    "HOSTNAME":          `[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*`,
    "IPORHOST":          `(?:%{IP}|%{HOSTNAME})`,
    "HOSTPORT":          `%{IPORHOST}:%{POSINT}`,
    "IPV4PORT":          `%{IPV4}:%{POSINT}`,
    "MAC":               `(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}`,

    // URI
    "URIPATHSEGMENT":    `[^\/?#\s]+`,
    "URIQUERY":          `[^#\s]*`,
    "URI_FRAGMENT":      `[^\s]*`,
    "URIPATH":           `/[^\s?#]*`,
    "URIPARAM":          `\?[^\s#]*`,
    "URIPATHPARAM":      `%{URIPATH}(?:%{URIPARAM})?`,
    "URIPROTO":          `[A-Za-z][A-Za-z0-9+\-.]*`,
    "URIHOST":           `%{IPORHOST}(?::%{POSINT})?`,
    "URI":               `%{URIPROTO}://%{URIHOST}%{URIPATHPARAM}?`,
    "URL":               `%{URI}`,
    "HTTPVERSION":       `(?:0\.9|1\.0|1\.1|2(?:\.0)?|3(?:\.0)?)`,
    "HTTPVERB":          `(?:GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|TRACE|CONNECT)`,
    "REQUEST":           `%{HTTPVERB}\s+%{NOTSPACE}(?:\s+HTTP/%{HTTPVERSION})?`,
    "WINPATH":           `(?:[A-Za-z]:|\\\\)[^\s]*`,
    "UNIXPATH":          `/(?:[\w.\-]+/)*[\w.\-]*`,
    "PATH":              `(?:%{WINPATH}|%{UNIXPATH})`,

    // Timestamps
    "YEAR":              `\d{4}`,
    "YEAR2":             `\d{2}`,
    "MONTHNUM":          `(?:0?[1-9]|1[0-2])`,
    "MONTHNUM2":         `(?:0[1-9]|1[0-2])`,
    "MONTHDAY":          `(?:0?[1-9]|[12]\d|3[01])`,
    "MONTHDAY2":         `(?:0[1-9]|[12]\d|3[01])`,
    "MONTH":             `(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)`,
    "DAY":               `(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)`,
    "HOUR":              `(?:2[0-3]|[01]?\d)`,
    "MINUTE":            `[0-5]?\d`,
    "SECOND":            `(?:[0-5]?\d|60)(?:[:.,]\d+)?`,
    "TZ":                `[A-Z]{2,5}|[+-]\d{2}:?\d{2}|Z`,
    "TIMESTAMP_ISO8601": `\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`,
    "HTTPDATE":          `\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4}`,
    "SYSLOGTIMESTAMP":   `(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2} \d{2}:\d{2}:\d{2}`,
    "TIME":              `(?:2[0-3]|[01]?\d):[0-5]\d(?::[0-5]\d(?:\.\d+)?)?`,
    "UNIX":              `\d{10}`,
    "UNIXMS":            `\d{13}`,
    "DURATION":          `\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)`,
    "SYSLOG5424SD":      `\[(?:[A-Za-z0-9@._-]+)(?: [A-Za-z0-9@._-]+=(?:"(?:\\.|[^"\\])*"))*\](?:\[(?:[A-Za-z0-9@._-]+)(?: [A-Za-z0-9@._-]+=(?:"(?:\\.|[^"\\])*"))*\])*`,
    "SYSLOGFACILITY":    `<%{NONNEGINT:facility}.%{NONNEGINT:priority}>`,
    "SYSLOGPROG":        `%{PROG}(?:\[%{POSINT:pid}\])?`,
    "SYSLOGBASE":        `%{SYSLOGTIMESTAMP:timestamp} %{SYSLOGHOST:logsource} %{SYSLOGPROG}:`,
    "SYSLOGHOST":        `%{IPORHOST}`,

    // Log levels
    "LOGLEVEL":          `(?i:trace|debug|info|notice|warn(?:ing)?|err(?:or)?|crit(?:ical)?|fatal|panic|alert|emerg(?:ency)?|verbose)`,
}
```

A required test (§13) compiles every library entry against these primitives. If a library entry references a primitive that doesn't exist here, you'll find out in CI, not in production.

Effective merge contract:

```go
var GrokPrimitives = mergePrimitives(
    GrokPrimitivesBundled,   // all bundled public sources
    GrokPrimitivesOverrides, // local RE2 and naming fixes
)
```

---

## 10. The Grok Compiler

`CompileGrok` turns a Grok string into a Go regexp. It does two jobs: expand `%{NAME}` references and anchor the result.

```go
// internal/pattern/compile.go
package pattern

import (
    "errors"
    "fmt"
    "regexp"
)

var grokRefRe = regexp.MustCompile(`%\{(\w+)(?::(\w+))?\}`)

// CompileGrok expands %{NAME} and %{NAME:field} references recursively, then
// compiles the result as a Go regexp anchored to match a whole line.
//
// The output regex matches "^<expanded pattern>\r?$" so it works on both
// Unix and Windows line endings.
func CompileGrok(pattern string, extras map[string]string) (*regexp.Regexp, error) {
    expanded, err := expandGrok(pattern, extras, 0)
    if err != nil {
        return nil, err
    }
    return regexp.Compile(`^` + expanded + `\r?$`)
}

func expandGrok(s string, extras map[string]string, depth int) (string, error) {
    if depth > 16 {
        return "", errors.New("grok expansion too deep (cycle?)")
    }

    var firstErr error
    out := grokRefRe.ReplaceAllStringFunc(s, func(match string) string {
        sub := grokRefRe.FindStringSubmatch(match)
        name, field := sub[1], sub[2]

        body, ok := extras[name]
        if !ok {
            body, ok = GrokPrimitives[name]
        }
        if !ok {
            if firstErr == nil {
                firstErr = fmt.Errorf("unknown grok primitive %%{%s}", name)
            }
            return match
        }

        // Recursively expand inside the body (e.g. IP references IPV4).
        expanded, err := expandGrok(body, extras, depth+1)
        if err != nil {
            if firstErr == nil { firstErr = err }
            return match
        }

        if field != "" {
            return "(?P<" + field + ">" + expanded + ")"
        }
        return "(?:" + expanded + ")"
    })

    return out, firstErr
}
```

### Why the `(?:...)` and `(?P<...>...)` wrapping?
- `(?P<name>...)` is Go's syntax for a named capture group. Logstash uses `(?<name>...)` but Go regexp wants the `P`. We have to translate.
- `(?:...)` is a non-capturing group. We wrap unnamed primitive expansions in this so that quantifiers and alternation in the surrounding Grok pattern bind correctly. Example: `%{NUMBER}+` should mean "one or more numbers," not "match a number, then a literal `+`."

### Why `\r?$` at the end?
Some log files have Windows line endings (`\r\n`). When you read them line-by-line in Go, the `\r` stays attached to the line. Allowing an optional trailing `\r` means our patterns work on both Unix and Windows logs.

---

## 11. The Drain Fallback

`axiomhq/drain3` clusters log lines by their structure — it figures out that "User X logged in from IP Y" and "User Z logged in from IP W" share the template "User `<*>` logged in from IP `<*>`."

We use it like this:

```go
// internal/pattern/drain.go
package pattern

import (
    "github.com/axiomhq/drain3"
)

type cluster struct {
    ID            int
    TokenCount    int
    Template      []tplPart
    LineCount     int
    SampleLineIdx int  // first line in the input that belongs to this cluster
}

type tplPart struct {
    IsSlot bool   // true: variable position; false: literal token
    Token  string // literal value when !IsSlot
}

func trainDrain(lines []string) ([]cluster, error) {
    cfg := drain3.DefaultConfig()
    cfg.SimilarityThreshold = 0.45  // slightly favors stable templates over over-broad clusters
    cfg.MatchThreshold      = 1.0
    cfg.ExtraDelimiters     = drainExtraDelimiters

    m, err := drain3.TrainWithConfig(lines, cfg)
    if err != nil {
        return nil, err
    }

    tmpls := m.Templates() // sorted by Count desc
    sampleIdx := indexFirstSampleLine(lines, m)

    out := make([]cluster, 0, len(tmpls))
    for _, t := range tmpls {
        out = append(out, cluster{
            ID:            t.ID,
            TokenCount:    t.TokenCount,
            Template:      buildTplParts(t),
            LineCount:     t.Count,
            SampleLineIdx: sampleIdx[t.ID],
        })
    }
    return out, nil
}

// Keep this conservative. Do not add ":" because it breaks timestamps, IPv6,
// host:port values, and Java logger names into fragments.
var drainExtraDelimiters = []string{"=", ",", "|"}

func indexFirstSampleLine(lines []string, m *drain3.Matcher) map[int]int {
    out := make(map[int]int, 32)
    for i, line := range lines {
        if id, ok := m.MatchID(line); ok {
            if _, seen := out[id]; !seen {
                out[id] = i
            }
        }
    }
    return out
}

// buildTplParts walks a Drain template and returns the interleaved sequence of
// literals and slot positions. Drain stores literals densely (in t.Tokens) and
// slot positions in a bitset (t.Params). We "merge" them back into one ordered
// sequence so we can walk it linearly when rendering.
func buildTplParts(t drain3.Template) []tplPart {
    parts := make([]tplPart, 0, t.TokenCount)
    nonParam := 0
    for pos := 0; pos < t.TokenCount; pos++ {
        if t.Params.Test(uint(pos)) {
            parts = append(parts, tplPart{IsSlot: true})
        } else {
            parts = append(parts, tplPart{IsSlot: false, Token: t.Tokens[nonParam]})
            nonParam++
        }
    }
    return parts
}
```

### Tokenization — bridging tokens and characters
Drain operates on tokens (words). The Grok pattern operates on characters. We need to know where each token starts and ends in the original line.

```go
// internal/pattern/tokenize.go
package pattern

import "strings"

type tokenSpan struct {
    Start, End int
    Text       string
}

// tokenizeWithSpans splits a line on whitespace (after substituting Drain's
// ExtraDelimiters with spaces) and records each token's byte range.
//
// IMPORTANT: this MUST behave identically to drain3's internal tokenizer.
// A test (§13) asserts equal token counts on a 1000-line corpus.
func tokenizeWithSpans(line string, extraDelimiters []string) []tokenSpan {
    sub := line
    for _, d := range extraDelimiters {
        sub = strings.ReplaceAll(sub, d, " ")
    }
    sub = strings.TrimSpace(sub)

    out := make([]tokenSpan, 0, 16)
    i, n := 0, len(sub)
    for i < n {
        for i < n && sub[i] == ' ' { i++ }
        if i >= n { break }
        start := i
        for i < n && sub[i] != ' ' { i++ }
        out = append(out, tokenSpan{Start: start, End: i, Text: sub[start:i]})
    }
    return out
}
```

### Slot resolution
For the dominant cluster, work out where each variable slot sits in the sample line, and what values appeared in that slot across all matching lines:

```go
type slotRange struct {
    SlotIndex   int
    Start, End  int
    Values      []string
}

func resolveSlots(c cluster, lines []string) []slotRange {
    sample := lines[c.SampleLineIdx]
    spans := tokenizeWithSpans(sample, drainExtraDelimiters)
    if len(spans) != c.TokenCount {
        return nil // tokenizer disagrees; slot resolution gives up
    }

    var slots []slotRange
    var slotPos []int
    for i, p := range c.Template {
        if p.IsSlot {
            slots = append(slots, slotRange{
                SlotIndex: i,
                Start:     spans[i].Start,
                End:       spans[i].End,
            })
            slotPos = append(slotPos, i)
        }
    }

    // Collect the actual values that appeared in each slot, by re-tokenizing
    // every line that belongs to this cluster.
    for _, line := range lines {
        sp := tokenizeWithSpans(line, drainExtraDelimiters)
        if len(sp) != c.TokenCount { continue }
        for si, pos := range slotPos {
            slots[si].Values = append(slots[si].Values, sp[pos].Text)
        }
    }
    return slots
}
```

### Per-slot classification
For each slot, run a small set of regexes against the values. The first regex that matches ≥ 95% of the values claims the slot.

```go
// internal/pattern/classify.go
package pattern

import "regexp"

type fieldType struct {
    GrokName string
    Regex    *regexp.Regexp
    Priority int
    NameHint string
}

var fieldTypes = []fieldType{
    {GrokName: "TIMESTAMP_ISO8601", Regex: re(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?$`), Priority: 1, NameHint: "timestamp"},
    {GrokName: "SYSLOGTIMESTAMP",   Regex: re(`^(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2} \d{2}:\d{2}:\d{2}$`), Priority: 1, NameHint: "timestamp"},
    {GrokName: "HTTPDATE",          Regex: re(`^\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4}$`), Priority: 1, NameHint: "timestamp"},
    {GrokName: "UNIXMS",            Regex: re(`^\d{13}$`), Priority: 2, NameHint: "timestamp_ms"},
    {GrokName: "UNIX",              Regex: re(`^\d{10}$`), Priority: 2, NameHint: "timestamp"},
    {GrokName: "UUID",              Regex: re(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`), Priority: 3, NameHint: "id"},
    {GrokName: "TRACEID",           Regex: re(`^[0-9a-fA-F]{32}$`), Priority: 3, NameHint: "trace_id"},
    {GrokName: "SPANID",            Regex: re(`^[0-9a-fA-F]{16}$`), Priority: 3, NameHint: "span_id"},
    {GrokName: "MAC",               Regex: re(`^(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$`), Priority: 4, NameHint: "mac"},
    {GrokName: "IPV4",              Regex: re(`^(?:(?:25[0-5]|2[0-4]\d|[01]?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d?\d)$`), Priority: 5, NameHint: "ip"},
    {GrokName: "IPV6",              Regex: re(`^(?:[0-9A-Fa-f]{0,4}:){2,}[0-9A-Fa-f]{0,4}$`), Priority: 5, NameHint: "ip"},
    {GrokName: "HOSTPORT",          Regex: re(`^[A-Za-z0-9_.:-]+:\d+$`), Priority: 6, NameHint: "endpoint"},
    {GrokName: "EMAILADDRESS",      Regex: re(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`), Priority: 7, NameHint: "email"},
    {GrokName: "URI",               Regex: re(`^[A-Za-z][A-Za-z0-9+\-.]*://\S+$`), Priority: 8, NameHint: "url"},
    {GrokName: "URIPATHPARAM",      Regex: re(`^/[^\s#]*(?:\?[^\s#]*)?$`), Priority: 9, NameHint: "path"},
    {GrokName: "URIPATH",           Regex: re(`^/[^\s?#]*$`), Priority: 10, NameHint: "path"},
    {GrokName: "LOGLEVEL",          Regex: re(`^(?i:trace|debug|info|notice|warn(?:ing)?|err(?:or)?|crit(?:ical)?|fatal|panic|alert|emerg(?:ency)?|verbose)$`), Priority: 11, NameHint: "level"},
    {GrokName: "DURATION",          Regex: re(`^\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)$`), Priority: 12, NameHint: "duration"},
    {GrokName: "BOOLEAN",           Regex: re(`^(?i:true|false|yes|no|on|off)$`), Priority: 13, NameHint: "flag"},
    {GrokName: "BASE16NUM",         Regex: re(`^(?:0[xX][0-9A-Fa-f]+|[0-9A-Fa-f]*[A-Fa-f][0-9A-Fa-f]*)$`), Priority: 14, NameHint: "hex"},
    {GrokName: "INT",               Regex: re(`^[+-]?\d+$`), Priority: 15, NameHint: "n"},
    {GrokName: "FLOAT",             Regex: re(`^[+-]?(?:\d+(?:\.\d+)?|\.\d+)(?:[eE][+-]?\d+)?$`), Priority: 16, NameHint: "n"},
    {GrokName: "NUMBER",            Regex: re(`^-?\d+(?:\.\d+)?$`), Priority: 17, NameHint: "n"},
    {GrokName: "QUOTEDSTRING",      Regex: re(`^"(?:\\.|[^"\\])*"$`), Priority: 18, NameHint: "text"},
    {GrokName: "WORD",              Regex: re(`^\w+$`), Priority: 19, NameHint: "word"},
    {GrokName: "NOTSPACE",          Regex: re(`^\S+$`), Priority: 20, NameHint: "value"},
}

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

const slotMatchThreshold = 0.95

func classifySlot(values []string) *fieldType {
    if len(values) == 0 { return nil }
    for _, ft := range fieldTypes {
        hits := 0
        for _, v := range values {
            if ft.Regex.MatchString(v) { hits++ }
        }
        if float64(hits)/float64(len(values)) >= slotMatchThreshold {
            return &ft
        }
    }
    return nil
}
```

### Field naming heuristic
A slot's field name should come from context whenever possible. Prefer stable key names (`request_id=...`, `trace_id:...`, `user: ...`) over generic names. Normalize common aliases to canonical names (`ts` → `timestamp`, `msg` → `message`, `lvl` → `level`, `method` → `http_method`, `path` → `url_path`, `status` → `status_code`). Otherwise use the type's `NameHint`, with a `_2`, `_3` suffix on collisions.

```go
type field struct {
    Start    int
    End      int
    GrokType string
    Name     string
}

func autoFieldsFromSlots(slots []slotRange, sample string, c cluster) []field {
    used := make(map[string]int) // name → count, for collision suffixes
    fields := make([]field, 0, len(slots))

    for _, s := range slots {
        ft := classifySlot(s.Values)
        if ft == nil { continue }

        name := suggestName(c, s, ft)
        if used[name] > 0 {
            name = fmt.Sprintf("%s_%d", name, used[name]+1)
        }
        used[name]++

        fields = append(fields, field{
            Start:    s.Start,
            End:      s.End,
            GrokType: ft.GrokName,
            Name:     name,
        })
    }
    return fields
}

func suggestName(c cluster, s slotRange, ft *fieldType) string {
    // Look at the literal token directly before this slot.
    if s.SlotIndex > 0 {
        prev := c.Template[s.SlotIndex-1]
        if !prev.IsSlot {
            stripped := strings.TrimRight(prev.Token, ":=")
            if isValidName(stripped) {
                return canonicalName(strings.ToLower(stripped))
            }
        }
    }
    return ft.NameHint
}

var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
func isValidName(s string) bool { return nameRe.MatchString(strings.ToLower(s)) }

var canonicalNames = map[string]string{
    "ts": "timestamp", "time": "timestamp", "timestamp": "timestamp",
    "lvl": "level", "levelname": "level", "severity": "level",
    "msg": "message", "message": "message",
    "logger_name": "logger", "log": "logger",
    "method": "http_method", "verb": "http_method",
    "path": "url_path", "uri": "url", "url": "url",
    "status": "status_code", "statuscode": "status_code",
    "latency": "duration", "duration_ms": "duration_ms",
    "trace": "trace_id", "traceid": "trace_id",
    "span": "span_id", "spanid": "span_id",
}

func canonicalName(s string) string {
    s = strings.Trim(s, `"'[](){}<>`)
    s = strings.ToLower(strings.ReplaceAll(s, "-", "_"))
    if mapped, ok := canonicalNames[s]; ok {
        return mapped
    }
    return s
}
```

---

## 12. The Renderer

`Render` walks the sample line one byte at a time. At each position:
1. If a field starts here, emit `%{TYPE:name}` and skip to the end of the field.
2. Else if an uncovered Drain slot starts here, emit `%{NOTSPACE:unparsed_N}` for a token-sized slot, or `%{GREEDYDATA:unparsed_N}` only if the slot can contain spaces.
3. Else emit one regex-escaped character and advance by one byte.

```go
// internal/pattern/render.go
package pattern

import (
    "regexp"
    "sort"
    "strconv"
    "strings"
)

func Render(sample string, fields []field, slots []slotRange) string {
    sorted := append([]field(nil), fields...)
    sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

    // Which slots are already covered by a field?
    covered := make(map[int]bool)
    for _, s := range slots {
        for _, f := range sorted {
            if f.Start <= s.Start && f.End >= s.End {
                covered[s.SlotIndex] = true
                break
            }
        }
    }

    slotByStart := make(map[int]slotRange, len(slots))
    for _, s := range slots { slotByStart[s.Start] = s }

    var b strings.Builder
    cursor, fieldI, unparsedN := 0, 0, 0

    for cursor < len(sample) {
        if fieldI < len(sorted) && sorted[fieldI].Start == cursor {
            f := sorted[fieldI]
            b.WriteString("%{")
            b.WriteString(f.GrokType)
            b.WriteString(":")
            b.WriteString(f.Name)
            b.WriteString("}")
            cursor = f.End
            fieldI++
            continue
        }
        if s, ok := slotByStart[cursor]; ok && !covered[s.SlotIndex] {
            unparsedN++
            b.WriteString("%{NOTSPACE:unparsed_")
            b.WriteString(strconv.Itoa(unparsedN))
            b.WriteString("}")
            cursor = s.End
            continue
        }
        b.WriteString(regexp.QuoteMeta(string(sample[cursor])))
        cursor++
    }
    return b.String()
}
```

`regexp.QuoteMeta` is important: the literal parts of the sample line (spaces, brackets, slashes) might contain regex metacharacters. We need them escaped so the Grok pattern matches them literally.

---

## 13. Tests

A junior engineer should be able to run `make test` and see green before submitting any change. The tests below are required.

### 13.1 `TestLibraryCompiles` — every library entry compiles
Catches typos and missing primitives at CI time, not runtime.

```go
func TestLibraryCompiles(t *testing.T) {
    for _, kp := range KnownPatterns {
        t.Run(kp.Name, func(t *testing.T) {
            _, err := CompileGrok(kp.Pattern, kp.CustomPatterns)
            if err != nil {
                t.Fatalf("library entry %q failed to compile: %v", kp.Name, err)
            }
        })
    }
}
```

### 13.2 `TestLibraryMatchesItsExample` — each library entry matches its own example
Every library entry should have a known sample line in `testdata/library_examples/<name>.txt`. The test compiles the entry's pattern and asserts it matches the sample. If you add a library entry, you also add its example file. No exceptions.

```go
func TestLibraryMatchesItsExample(t *testing.T) {
    for _, kp := range KnownPatterns {
        t.Run(kp.Name, func(t *testing.T) {
            sample, err := os.ReadFile(filepath.Join("testdata/library_examples", slug(kp.Name)+".txt"))
            if err != nil { t.Fatalf("missing example file: %v", err) }

            re, err := CompileGrok(kp.Pattern, kp.CustomPatterns)
            if err != nil { t.Fatal(err) }

            for _, line := range strings.Split(strings.TrimSpace(string(sample)), "\n") {
                if !re.MatchString(line) {
                    t.Errorf("pattern %q did not match its own example line: %s", kp.Name, line)
                }
            }
        })
    }
}
```

### 13.2a `TestPublicModuleAPI` — importable from external test package
Add a test under `test/benchmark` (or another non-`internal` folder) that imports `log2grok/pkg/log2grok` and calls `Discover`, `CompileGrok`, and `EvaluateCoverage`. This prevents regressions where only internal APIs compile.

### 13.2b `TestBundledCoverage` — all bundled packs are represented
Assert that bundled output tables include data from every declared bundled source pack.

```go
func TestBundledCoverage(t *testing.T) {
    if len(BuiltinPatternPacks) == 0 {
        t.Fatal("no bundled pattern packs configured")
    }
    for _, p := range BuiltinPatternPacks {
        if strings.TrimSpace(p.PatternText) == "" {
            t.Fatalf("pack %s has empty pattern text", p.Name)
        }
        if !bundledPackPresent(p.Name) {
            t.Fatalf("pack %s missing in bundled output", p.Name)
        }
    }
    if len(KnownPatternsBundled) < 200 {
        t.Fatalf("bundled known patterns unexpectedly small: %d", len(KnownPatternsBundled))
    }
    if len(GrokPrimitivesBundled) < 250 {
        t.Fatalf("bundled primitives unexpectedly small: %d", len(GrokPrimitivesBundled))
    }
}
```

### 13.3 `TestStructuredProbes` — structured syntaxes are detected before Drain
Create sample corpora for JSON object logs, Docker JSON, Pino/Bunyan, logfmt, CEF, LEEF, IIS/W3C, CSV, and TSV. Assert each one returns `Source` beginning with `structured:` and coverage ≥ 0.95.

```go
func TestStructuredProbes(t *testing.T) {
    for _, tc := range structuredProbeCases(t) {
        t.Run(tc.Name, func(t *testing.T) {
            dp, err := Discover(tc.Lines, Options{})
            if err != nil { t.Fatal(err) }
            if !strings.HasPrefix(dp.Source, "structured:") {
                t.Fatalf("source = %q, want structured:*", dp.Source)
            }
            if dp.Coverage < 0.95 {
                t.Fatalf("coverage = %.3f, want >= 0.95", dp.Coverage)
            }
        })
    }
}
```

### 13.4 `TestLibraryPrefersSpecificPattern` — catchalls do not steal matches
Feed a corpus that matches both a source-specific pattern and a generic timestamp pattern. Assert the source-specific pattern wins.

```go
func TestLibraryPrefersSpecificPattern(t *testing.T) {
    lines := readLines(t, "testdata/golden/spring_boot/input.log")
    dp, err := Discover(lines, Options{})
    if err != nil { t.Fatal(err) }
    if dp.Source != "library:Spring Boot Default" {
        t.Fatalf("source = %q, want Spring Boot Default", dp.Source)
    }
}
```

### 13.5 `TestSinglePattern` — Discover always returns one pattern
Type-level guarantee. Mostly defensive; the function signature already enforces this, but a test makes it explicit.

```go
func TestSinglePattern(t *testing.T) {
    for _, dir := range goldenDirs(t) {
        lines := readLines(t, filepath.Join(dir, "input.log"))
        dp, err := Discover(lines, Options{})
        if err != nil { t.Fatal(err) }
        if dp == nil { t.Fatal("Discover returned nil DiscoveredPattern") }
        if dp.Grok == "" { t.Fatal("empty Grok string") }
    }
}
```

### 13.6 `TestGoldenCorpora` — each corpus produces its expected pattern
The biggest test. For each directory under `testdata/golden/`, run Discover and assert the output matches `expected.txt`.

```go
func TestGoldenCorpora(t *testing.T) {
    for _, dir := range goldenDirs(t) {
        t.Run(filepath.Base(dir), func(t *testing.T) {
            lines := readLines(t, filepath.Join(dir, "input.log"))
            expected := strings.TrimSpace(readFile(t, filepath.Join(dir, "expected.txt")))

            dp, err := Discover(lines, Options{})
            if err != nil { t.Fatal(err) }

            // expected.txt contains: <grok pattern>\n# <source>\n<min_coverage>
            // (see §14 for the format)
            wantGrok, wantSource, wantMinCov := parseExpected(expected)

            if dp.Grok != wantGrok {
                t.Errorf("grok mismatch:\n  got:  %s\n  want: %s", dp.Grok, wantGrok)
            }
            if dp.Source != wantSource {
                t.Errorf("source mismatch: got %q, want %q", dp.Source, wantSource)
            }
            if dp.Coverage < wantMinCov {
                t.Errorf("coverage too low: got %.3f, want ≥ %.3f", dp.Coverage, wantMinCov)
            }
        })
    }
}
```

### 13.7 `TestTokenizerParity` — our tokenizer matches Drain's
On a 1000-line corpus, assert that for every line `len(tokenizeWithSpans(line)) == drain3-internal-token-count`. A mismatch silently breaks slot resolution; this test surfaces it.

### 13.8 `TestCompileGrokRecursion` — circular references don't loop forever
Pass an `extras` map where `A → B → A`. Assert `CompileGrok` returns an error rather than hanging.

### 13.9 `TestCandidateScoring` — source-specific beats generic
Build fake candidate results with identical coverage but different `Specificity`, `Priority`, and `%{GREEDYDATA}` counts. Assert `betterCandidate` chooses the most useful pattern.

### 13.10 Unit tests for utilities
- `tokenizeWithSpans`: known-input/known-output pairs covering leading/trailing space, runs of spaces, lines with `=`.
- `classifySlot`: 95%-threshold edge cases (exactly 95%, just under, mixed).
- `suggestName`: `key=value` → `key`, `key:value` → `key`, no preceding token → fallback to NameHint.
- `Render`: known fields and slots → known output. At minimum: a field that fully covers a slot, a field that's a sub-range of a slot, a slot uncovered by any field.

---

## 14. Golden Corpora

Under `testdata/golden/`, each subdirectory contains:
- `input.log` — 200–5000 representative log lines.
- `expected.txt` — three lines:

```
%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"
# library:Nginx Access Combined
# min_coverage:0.99
```

### Required corpora for the PoC
Start with these 30 corpora. Each source-specific corpus should target coverage ≥ 0.99 unless the real format has optional fields that make ≥ 0.95 more realistic.

1. `nginx_access_combined` — `library:Nginx Access Combined`.
2. `nginx_access_timing` — `library:Nginx Access Main With Timing`.
3. `nginx_error` — `library:Nginx Error`.
4. `apache_combined` — `library:Apache Combined`.
5. `apache_error` — `library:Apache Error`.
6. `iis_w3c` — `structured:W3C IIS`.
7. `syslog_rfc3164` — `library:Syslog RFC3164`.
8. `syslog_rfc5424` — `library:Syslog RFC5424`.
9. `sshd` — `library:SSH Authentication`.
10. `auditd` — `library:Auditd Key Value` or `structured:logfmt`.
11. `haproxy_http` — `library:HAProxy HTTP`.
12. `aws_alb` — `library:AWS ALB`.
13. `aws_vpc_flow` — `library:AWS VPC Flow Logs`.
14. `cloudfront` — source-specific library entry.
15. `docker_json` — `structured:Docker JSON`.
16. `cri_containerd` — source-specific library entry.
17. `kubernetes_klog` — source-specific library entry.
18. `json_app` — `structured:JSON Object`.
19. `logfmt_app` — `structured:logfmt`.
20. `pino_json` — `structured:Pino JSON`.
21. `spring_boot` — `library:Spring Boot Default`.
22. `log4j_logback` — `library:Log4j Logback`.
23. `python_logging` — `library:Python Logging`.
24. `postgres` — `library:PostgreSQL`.
25. `redis` — `library:Redis`.
26. `cef` — `structured:CEF`.
27. `leef` — `structured:LEEF`.
28. `suricata_eve` — `structured:Suricata EVE`.
29. `zeek_tsv` — `structured:TSV` or source-specific Zeek entry.
30. `weird_app` — no library match; must produce `drain`, coverage ≥ 0.85.

After the PoC, add one golden corpus for every row in the required catalog table. The library is only as good as the example coverage that protects it.

---

## 15. The `main.go` Wrapper

This is the only file in `cmd/log2grok/`. Keep it short — it does I/O and flag parsing only.

```go
// cmd/log2grok/main.go
package main

import (
    "bufio"
    "flag"
    "fmt"
    "io"
    "os"

    l2g "log2grok/pkg/log2grok"
)

func main() {
    threshold := flag.Float64("threshold", 0.85, "min library match rate")
    maxLines  := flag.Int("max-lines", 100000, "stop reading after this many lines")
    verbose   := flag.Bool("verbose", false, "log diagnostics to stderr")
    quiet     := flag.Bool("quiet", false, "suppress trailing comment line")
    flag.Parse()

    if flag.NArg() != 1 {
        fmt.Fprintln(os.Stderr, "usage: log2grok [flags] <input-file|->")
        os.Exit(1)
    }

    src, err := openInput(flag.Arg(0))
    if err != nil {
        fmt.Fprintln(os.Stderr, "error:", err)
        os.Exit(1)
    }
    defer src.Close()

    lines, err := readLines(src, *maxLines)
    if err != nil {
        fmt.Fprintln(os.Stderr, "error:", err)
        os.Exit(1)
    }

    dp, err := l2g.Discover(lines, l2g.Options{
        LibraryThreshold: *threshold,
        Verbose:          *verbose,
    })
    if err != nil {
        fmt.Fprintln(os.Stderr, "error:", err)
        os.Exit(2)
    }

    fmt.Println(dp.Grok)
    if !*quiet {
        fmt.Printf("# matched %d / %d lines (%.1f%%) — %s\n",
            dp.MatchedCount, dp.TotalLines, dp.Coverage*100, dp.Source)
    }
}

func openInput(path string) (io.ReadCloser, error) {
    if path == "-" {
        return io.NopCloser(os.Stdin), nil
    }
    return os.Open(path)
}

func readLines(r io.Reader, max int) ([]string, error) {
    s := bufio.NewScanner(r)
    s.Buffer(make([]byte, 1024*1024), 8*1024*1024) // allow long lines (8 MiB max)
    out := make([]string, 0, 1024)
    for s.Scan() {
        out = append(out, s.Text())
        if len(out) >= max { break }
    }
    return out, s.Err()
}
```

That's the entire CLI. Forty-something lines. Resist the urge to add anything else here — keep `main.go` boring and put logic in `internal/pattern/`.

---

## 16. Build, Run, Ship

### `go.mod` (required)
The repository must include a valid module file so external suites can import `pkg/log2grok`.

```go
module log2grok

go 1.22

require github.com/axiomhq/drain3 latest
```

If you publish this repo under GitHub, update `module` to the full path (for example `github.com/<org>/log2grok`) and keep imports consistent.

### Makefile (required targets)
```makefile
.PHONY: buildpacks build test golden lint run

buildpacks:
	go run ./cmd/buildpacks

build:
	go build -trimpath -ldflags="-s -w" -o bin/log2grok ./cmd/log2grok

test: buildpacks
	go test ./...

golden:
	go test -run TestGoldenCorpora ./internal/pattern -v

lint:
	go vet ./...
	gofmt -l . | (! grep .)

run: build
	@echo "Try: ./bin/log2grok testdata/golden/nginx_access_combined/input.log"
```

### A typical session
```sh
$ make build
$ ./bin/log2grok testdata/golden/nginx_access_combined/input.log
%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) ...
# matched 5000 / 5000 lines (100.0%) — library:Nginx Access Combined

$ make test
ok  	log2grok/...	0.142s
```

---

## 17. Glossary

- **Grok pattern** — a regex with named placeholders like `%{IPV4:ip}`. Used by Logstash, Elastic, Vector, Fluent Bit.
- **Primitive** — a named regex building block (`IPV4`, `NUMBER`, `TIMESTAMP_ISO8601`). Defined once, referenced from many patterns.
- **Library / KnownPatterns** — our built-in dictionary of well-known log formats (Nginx, Syslog, etc.).
- **Structured probe** — a detector/renderer for syntax-defined formats such as JSON, logfmt, CEF, LEEF, W3C/IIS, CSV, and TSV.
- **Specificity** — a library tie-break score. A source-specific parser has high specificity; a generic timestamp parser has low specificity.
- **Drain** — an algorithm that clusters log lines by structural similarity. We use the implementation in `axiomhq/drain3`.
- **Cluster / template** — a group of log lines that share the same structure, with variable values replaced by `<*>`. Drain's output.
- **Slot** — a `<*>` position in a Drain template. Each slot maps to a byte range in the sample line.
- **Coverage** — the fraction of input lines that the chosen Grok pattern matches. Reported in the comment after the pattern.
- **Sample line** — one representative line from the input that the chosen pattern is built around. In the library path it's the first line that matches the library pattern; in the Drain path it's the first line that belongs to the dominant cluster.
- **Anchored regex** — a regex that must match the whole string (with `^` at the start and `$` at the end). All our compiled regexes are anchored.

---

## 18. FAQ for the Implementer

### Q: My library pattern doesn't compile. What's wrong?
Probably one of:
- A primitive name that isn't in `GrokPrimitives` (e.g., you wrote `%{TIMSTAMP}` instead of `%{TIMESTAMP_ISO8601}`).
- A PCRE feature Go's RE2 doesn't support: atomic groups `(?>...)`, possessive quantifiers `++` `*+`, lookbehind `(?<=...)`. Rewrite without them.
- An unbalanced bracket. Check carefully — it's almost always this.

### Q: Drain produces a pattern but it's bad. What do I do?
Three options:
1. Lower `-threshold` so the library wins more often (less aggressive Drain).
2. Add a library entry for the format you're seeing.
3. Tune `cfg.SimilarityThreshold` in `trainDrain` (lower = more clusters; higher = fewer, broader clusters).

### Q: A library pattern matches some lines but not others. Why?
Because the threshold isn't met. Check `-verbose` to see what coverage each library entry got. If your file has multiple log shapes mixed, no single library entry will match all of them, and that's expected. Either filter the file or accept the Drain fallback.

### Q: Can this really match 99% of all logs?
It should match ≥ 99% of lines for common, single-format corpora that are covered by the library or structured probes. It cannot guarantee 99% for files that mix unrelated formats, contain multi-line records, or use source-specific options we have never seen. For those, the requirement is still useful output: one pattern, truthful coverage, and a clear source label (`library:*`, `structured:*`, `drain`, or `fallback`).

### Q: Why not just return `%{GREEDYDATA:message}` and get 100%?
Because matching is not the same as parsing. `%{GREEDYDATA}` is only allowed as the final fallback or as a message tail after useful fields have already been extracted.

### Q: Should I add multi-line log support?
No. Out of scope for v1. Java stack traces, Python tracebacks, etc., need a pre-stitching step that combines continuation lines into single logical lines. Defer.

### Q: Should I support YAML/JSON config for the library?
No. The library is Go code. It's compiled in. Adding new entries requires a recompile. This is a feature, not a limitation — it makes the binary self-contained.

### Q: Which package should external tests import?
Import `log2grok/pkg/log2grok`. Do not import `internal/pattern` from external suites; Go forbids that by design.

### Q: My golden test fails because the expected pattern doesn't match what Discover returned. Which is right?
Read both carefully. If the pattern Discover produced is genuinely correct (it matches the input file with high coverage and extracts useful field names), update `expected.txt`. If Discover produced something worse than the expected pattern, fix the bug in Discover. The golden tests are the authoritative answer to "what should this look like" — but they're updated when the answer changes for good reasons.

### Q: How do I add a new library entry?
1. Prefer adding it to bundled source packs so generation picks it up automatically.
2. If it is project-specific, add a `KnownPattern{}` override in `internal/pattern/overrides/known/`.
3. Pick a `Priority` that puts it in the right spot and a `Specificity` that beats generic catchalls when coverage ties.
4. Add a sample line in `testdata/library_examples/<slug>.txt`.
5. Run `make test`. If `TestLibraryCompiles` and `TestLibraryMatchesItsExample` both pass, you're done.

### Q: How do I add another public pattern source?
1. Add its pattern text snapshot to `internal/pattern/packs/<name>.pattern`.
2. Register the pack in `BuiltinPatternPacks` with `PatternText`.
3. Update `cmd/buildpacks` parser rules if the source format differs.
4. Run `make buildpacks`.
5. Run `make test` and add/refresh golden corpora for impacted formats.

### Q: How do I add a new primitive?
1. Add it to `GrokPrimitives` in `primitives.go`. Use only RE2-compatible regex.
2. Done. `TestLibraryCompiles` will tell you if any library entry now references it incorrectly.

### Q: What if `Discover` returns the wrong pattern entirely?
Run with `-verbose` to see what was tried. If a library entry that *should* have matched didn't, check whether its primitives are too strict. If Drain produced something weird, look at `cfg.SimilarityThreshold`. Failing all that, add the format to the library — it's the most predictable fix.

---

## 19. Out of Scope (For Now)

These are explicit non-goals for this version. Don't add them.

- A web UI.
- Multi-line log support.
- Live log tailing.
- Multiple output patterns per file.
- Authentication, deployment, persistence.
- A YAML/JSON config for the library (the library is Go code).
- Automatic learning of new patterns from user feedback.
- Confidence scores beyond "coverage."

When this CLI exists and works well, the web UI from earlier specs becomes a thin wrapper over it. That's the point of starting here.
