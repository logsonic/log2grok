# Log2Grok CLI — Implementation Specification

**Version:** 0.8 (CLI-only, broad-format optimized)
**Audience:** This spec is written for a junior Go engineer. It explains not just *what* to build but *why* each decision was made. If anything is unclear, the answer is in §17 (Glossary) or §18 (FAQ).

### Changelog vs 0.7
- §3/§7: stages now carry a *best-so-far* result forward instead of dropping mid-coverage candidates. Hard thresholds become "auto-accept" gates rather than mandatory minima.
- §6: `DiscoveredPattern` gains `SourceFamily`, `SampleLine`, and `Truncated`.
- §7: `chooseSample` no longer divides by zero and matches the prose.
- §7: `betterCandidate` compares integer match counts, not floats; library compile errors surface through `LibraryDiagnostics()`.
- §8: `KnownPatterns` is composed in `init()` via an explicit `sortKnownPatterns`; the curated literal block was renamed `KnownPatternsCurated`. Dedup rules made explicit. CSV/TSV limitations documented.
- §9: `HOSTNAME` allows underscores; `HTTPVERB` is case-insensitive; `URN` renamed to `COLONURI`.
- §10: `%{NAME:field:type}` (Logstash type-cast) is parsed and ignored; `expandGrok` deduplicates colliding named captures by appending `_2`, `_3`, …; renderer iterates by rune for UTF-8 safety.
- §11: drain3 access is wrapped in an internal `drainBackend` adapter interface; tokenization comes from the adapter, removing the parity assertion that couldn't be written against drain3 internals.
- §13: `TestBundledCoverage` checks named-pattern presence instead of magic count thresholds; `TestLibraryMatchesItsExample` is tiered into *required* (curated) and *opportunistic* (bundled).
- §15/§16: empty-input maps to exit code 1; stdin TTY check; `go.mod` shows a real pinned version syntax.

---

## 1. What You're Building

A command-line program. You give it a log file. It prints one Grok pattern that matches most of the lines in that file.

```sh
$ log2grok /var/log/nginx/access.log
%{IPORHOST:client_ip} %{USER:ident} %{USER:auth} \[%{HTTPDATE:timestamp}\] "%{WORD:method} %{NOTSPACE:url} HTTP/%{NUMBER:http_version}" %{INT:status} (?:%{INT:bytes}|-) "%{DATA:referrer}" "%{DATA:user_agent}"
# matched 4823 / 4900 lines (98.4%) -- library:Nginx Access Combined
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

### Access constraints (prevent context overflow)
When following this spec, do not open or analyze any repository content under:
- `test/` (including `testdata/`)
- `tools/`

Instead, reason solely from:
- this spec, and
- production code under `cmd/log2grok`, `pkg/log2grok`, and `internal/`.

---

## 3. How the Program Decides on a Pattern

The program uses a staged matcher. Cheap, high-confidence checks run first; expensive or generic fallbacks run last. The goal is to match the dominant single-line format in real log files with high coverage, usually ≥ 99% for known formats, while still returning one usable pattern for unknown formats.

### Best-so-far semantics
Every stage produces (at most) one candidate `DiscoveredPattern`. Stages do **not** drop a candidate just because it didn't clear an "auto-accept" threshold; they hand it forward as the current best-so-far. Only when a stage produces a *decisively* good result (see thresholds below) does `Discover` short-circuit and return immediately. Otherwise it runs all stages and returns the best candidate by `(coverage desc, useful_capture_count desc, source priority asc)`. This avoids the failure mode where a 0.88 structured probe and a 0.83 library hit are both rejected in favour of a 0.40 Drain pattern.

### Stage 0 — Normalize and Sample
Before matching anything:
- Drop empty lines from matching calculations and coverage reporting; mention skipped blanks only in verbose diagnostics.
- Strip a UTF-8 BOM from the first line.
- Preserve all other bytes. Do not trim normal log lines; leading spaces can be meaningful.
- Build a deterministic sample of at most 4096 non-empty lines: the first `min(1024, len(lines))` lines plus evenly spaced lines drawn from the remainder. Use this for cheap candidate scoring, then verify the winner against all lines.

This keeps large files fast without making the result depend only on the beginning of a file.

### Stage 1 — Structured Format Probes
Some formats are not best discovered by Drain or a flat library regex. Detect these by syntax first:
- JSON object logs: Docker JSON, Kubernetes app JSON, Pino/Bunyan, Zap JSON, ECS JSON, CloudTrail, Suricata EVE, auditd JSON.
- `logfmt` / key-value logs: `ts=... level=info msg="..."`.
- CEF and LEEF security events.
- W3C/IIS logs with `#Fields:` headers.
- CSV/TSV logs with stable column counts.

For each probe, render a Grok pattern from the observed shape and verify it against **all** lines (not the sample). If full-file coverage ≥ 0.90 the probe auto-accepts and `Discover` returns. Otherwise the result is recorded as best-so-far if it beats whatever was there before, and matching continues. Structured probes should prefer extracting common fields (`timestamp`, `level`, `message`, `logger`, `trace_id`, `span_id`, `request_id`, `client_ip`, `method`, `path`, `status`, `duration`) and then use `%{GREEDYDATA:json}`, `%{GREEDYDATA:kvpairs}`, or `%{GREEDYDATA:fields}` for the remainder.

### Stage 2 — Known Pattern Library
The program ships with a broad built-in dictionary of well-known log formats: web servers, syslog variants, container runtimes, cloud logs, databases, language runtimes, security devices, and common application frameworks. Compile these patterns once, score them against the sample, then fully evaluate only the best candidates.

Do not accept the first match blindly. Pick the highest-scoring specific pattern. Tie-break by:
1. Higher integer match count over the full file.
2. Higher specificity (`Specificity` field, or lower `Priority` if equal).
3. More typed captures (i.e. fewer `%{GREEDYDATA}` and fewer `%{NOTSPACE:unparsed_*}`).
4. Earlier library order.

Catchalls are useful, but they must never beat a source-specific pattern with similar coverage. The library auto-accepts at coverage ≥ `LibraryThreshold` (default 0.85); below that, the best library hit is still passed forward as best-so-far.

### Stage 3 — Derive From Structure (Drain Fallback)
If no earlier stage auto-accepted, derive a pattern from the file itself. Use Drain to find the dominant shape, classify variable slots with a rich token catalog, and render a Grok pattern around literal text. The Drain result is recorded as best-so-far if it beats the current best.

This catches custom application logs, internal services, one-off scripts, and new vendor formats.

### Stage 4 — Last-Resort Safe Pattern
If neither earlier stages nor Drain produced anything usable, return the narrowest safe fallback:
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
| `-threshold <float>` | `0.85` | Auto-accept threshold (0.0–1.0) for a library pattern. Below it, the library result is still considered as best-so-far. |
| `-max-lines <int>` | `100000` | Stop reading after this many lines. If the input is longer, the result is computed over the truncated set and the comment line shows `truncated`. |
| `-verbose` | `false` | Print extra diagnostic info to stderr (top candidate scores, rejected entries, blank-line counts, truncation, etc.). |
| `-quiet` | `false` | Suppress the trailing `# matched...` comment on stdout. Independent of `-verbose`. |
| `-h, -help` | — | Show usage and exit. |

`-quiet` and `-verbose` are independent: `-quiet` only affects stdout, `-verbose` only affects stderr. Both may be set together (suppress stdout comment but keep stderr diagnostics).

### Output Contract
- **stdout** receives exactly two things: the Grok pattern on its own line, then (unless `-quiet`) a single comment line of the form `# matched N / M lines (P%) -- <source>` (use `--` rather than an em-dash so the line is ASCII-safe in pipes). When the input was truncated by `-max-lines`, the comment ends with ` (truncated at <max-lines>)`. Nothing else goes to stdout. This makes piping into other tools straightforward.
- **stderr** receives errors, and verbose diagnostics if `-verbose` is set. Verbose output includes the top-K candidates with sample/full coverage, blank-line counts, truncation, and the chosen stage.
- **Exit codes**:
  - `0` — success (a pattern was produced).
  - `1` — usage error (bad flag, missing file, unreadable file, empty input, stdin attached to a TTY with no piped data).
  - `2` — internal error (e.g. the bundled pack failed to load; should not occur in a released build).

  Note vs 0.7: empty input now exits `1`, not `2`. There is *always* a fallback for non-empty input, so exit code `2` is reserved for genuine internal failure.

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
│       ├── drain.go             # drainBackend interface and trainDrain orchestration.
│       ├── drain_backend.go     # Default drainBackend implementation against axiomhq/drain3.
│       ├── tokenize.go          # tokenSpansOf(): locates backend tokens in source line.
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
    Source       string  // "library:Nginx Access Combined", "structured:JSON Object", "drain", or "fallback:*"
    SourceFamily string  // "library" | "structured" | "drain" | "fallback" — machine-friendly slug
    Grok         string  // the rendered Grok pattern (raw, unanchored)
    Coverage     float64 // 0.0–1.0; equals MatchedCount / TotalLines
    MatchedCount int
    TotalLines   int     // count of non-empty lines considered (may be < len(input) if truncated)
    SampleLine   string  // the representative line the pattern was built around (empty for library hits)
    Truncated    bool    // true when input was cut by Options.MaxLines
}

// Options controls Discover's behavior.
type Options struct {
    LibraryThreshold float64   // default 0.85; library auto-accepts at >=, otherwise carried forward as best-so-far
    MaxLines         int       // if > 0, limits how many lines Discover considers; sets Truncated when applied
    Verbose          bool      // if true, write diagnostics to Diagnostics
    Diagnostics      io.Writer // optional; defaults to io.Discard. Receives verbose output.
}

// Discover returns the single best Grok pattern for the input lines.
// If lines contains no non-empty lines, returns an error wrapping ErrEmptyInput.
func Discover(lines []string, opts Options) (*DiscoveredPattern, error)

// ErrEmptyInput is returned when no non-empty lines are available.
var ErrEmptyInput = errors.New("log2grok: no non-empty input lines")

// CompileGrok expands all %{NAME}, %{NAME:field}, and %{NAME:field:type}
// references using GrokPrimitives plus any extras, then compiles the result
// as an anchored Go regexp. The optional `:type` (Logstash type-cast hint
// such as :int or :float) is parsed for compatibility but ignored.
// Anchoring is automatic — callers pass the raw Grok pattern, not anchored.
//
//   re, err := CompileGrok(`%{IPV4:ip} %{WORD:verb}`, nil)
//
// Duplicate capture names that arise from recursive expansion (e.g. when a
// referenced primitive contains a named subgroup whose name collides with
// another field) are deduplicated by appending _2, _3, etc.
func CompileGrok(pattern string, extras map[string]string) (*regexp.Regexp, error)

// EvaluateCoverage runs re against every line; returns count of matches.
func EvaluateCoverage(re *regexp.Regexp, lines []string) int

// LibraryDiagnostics returns any errors that occurred while compiling the
// built-in library at startup. Callers (especially tests) should fail loudly
// if this is non-empty in CI.
func LibraryDiagnostics() []error
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

`Discover` walks the four stages, but never *discards* a working candidate just because it didn't auto-accept. Each stage feeds into a single `bestSoFar` slot; only an auto-accept short-circuits.

```go
// internal/pattern/discovery.go
func Discover(lines []string, opts Options) (*DiscoveredPattern, error) {
    normalized := normalizeLines(lines)
    if len(normalized.MatchLines) == 0 {
        return nil, ErrEmptyInput
    }

    threshold := opts.LibraryThreshold
    if threshold <= 0 {
        threshold = 0.85
    }
    diag := opts.Diagnostics
    if diag == nil {
        diag = io.Discard
    }

    sample := chooseSample(normalized.MatchLines, 4096)

    var best *DiscoveredPattern

    // STAGE 1 — Syntax-driven structured probes. Auto-accept at >=0.90.
    if dp := tryStructured(sample, normalized.MatchLines, diag); dp != nil {
        if dp.Coverage >= 0.90 {
            return dp, nil
        }
        best = pickBetter(best, dp)
    }

    // STAGE 2 — Broad known-format library. Auto-accept at >= threshold.
    if dp := tryLibrary(sample, normalized.MatchLines, threshold, diag); dp != nil {
        if dp.Coverage >= threshold {
            return dp, nil
        }
        best = pickBetter(best, dp)
    }

    // STAGE 3 — Drain fallback. Auto-accept at >= 0.85, otherwise carry forward.
    if dp, err := deriveFromDrain(normalized.MatchLines, diag); err == nil && dp != nil && dp.Grok != "" {
        if dp.Coverage >= 0.85 {
            return dp, nil
        }
        best = pickBetter(best, dp)
    }

    if best != nil {
        return best, nil
    }

    // STAGE 4 — Always return one safe pattern.
    return deriveSafeFallback(normalized.MatchLines), nil
}

// pickBetter compares two candidates by coverage (integer match count),
// then specificity (typed captures), then source-family priority.
func pickBetter(a, b *DiscoveredPattern) *DiscoveredPattern {
    switch {
    case a == nil:
        return b
    case b == nil:
        return a
    case b.MatchedCount != a.MatchedCount:
        if b.MatchedCount > a.MatchedCount { return b }
        return a
    case typedCaptureCount(b.Grok) != typedCaptureCount(a.Grok):
        if typedCaptureCount(b.Grok) > typedCaptureCount(a.Grok) { return b }
        return a
    case familyRank(b.SourceFamily) < familyRank(a.SourceFamily):
        return b
    default:
        return a
    }
}

func familyRank(family string) int {
    switch family {
    case "library":    return 0
    case "structured": return 1
    case "drain":      return 2
    case "fallback":   return 3
    default:           return 4
    }
}

// typedCaptureCount counts %{TYPE:name} captures, excluding GREEDYDATA and
// the auto-generated unparsed_* slots.
func typedCaptureCount(grok string) int {
    n := 0
    for _, m := range grokRefRe.FindAllStringSubmatch(grok, -1) {
        name := m[1]
        field := m[2]
        if field == "" || strings.HasPrefix(field, "unparsed_") || name == "GREEDYDATA" {
            continue
        }
        n++
    }
    return n
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
// It returns the best probe-derived candidate it finds (verified against all
// lines), regardless of whether it cleared 0.90. Discover decides whether to
// auto-accept.
func tryStructured(sample, all []string, diag io.Writer) *DiscoveredPattern {
    var best *DiscoveredPattern
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
            fmt.Fprintf(diag, "structured probe %s: compile failed: %v\n", probe.Name, err)
            continue
        }
        matched := EvaluateCoverage(re, all)
        dp := &DiscoveredPattern{
            Source:       source,
            SourceFamily: "structured",
            Grok:         grok,
            Coverage:     ratio(matched, len(all)),
            MatchedCount: matched,
            TotalLines:   len(all),
        }
        fmt.Fprintf(diag, "structured probe %s: matched=%d/%d\n", probe.Name, matched, len(all))
        best = pickBetter(best, dp)
    }
    return best
}

// tryLibrary compiles the library once, scores every pattern on the sample,
// fully evaluates only plausible winners, and returns the best library hit.
// Returns the best regardless of threshold; Discover decides on auto-accept.
func tryLibrary(sample, all []string, threshold float64, diag io.Writer) *DiscoveredPattern {
    candidates := scoreLibraryOnSample(sample)
    candidates = keepTopCandidates(candidates, 12)

    var best *candidateResult
    for _, c := range candidates {
        if c.SampleCoverage < 0.50 {
            continue // not plausible enough to spend full-file work on
        }
        matched := EvaluateCoverage(c.Compiled, all)
        result := &candidateResult{
            Pattern:      c.Pattern,
            Compiled:     c.Compiled,
            Matched:      matched,
            FullTotal:    len(all),
        }
        fmt.Fprintf(diag, "library %s: sample=%.3f full=%d/%d\n",
            c.Pattern.Name, c.SampleCoverage, matched, len(all))
        if betterCandidate(result, best) {
            best = result
        }
    }
    if best == nil {
        return nil
    }
    return &DiscoveredPattern{
        Source:       "library:" + best.Pattern.Name,
        SourceFamily: "library",
        Grok:         best.Pattern.Pattern,
        Coverage:     ratio(best.Matched, best.FullTotal),
        MatchedCount: best.Matched,
        TotalLines:   best.FullTotal,
    }
}

// deriveFromDrain runs Drain, uses the largest cluster as the source of one
// output pattern, classifies slots, renders Grok, and reports full coverage.
func deriveFromDrain(lines []string, diag io.Writer) (*DiscoveredPattern, error) {
    clusters, err := trainDrain(lines)
    if err != nil {
        return nil, err
    }
    if len(clusters) == 0 {
        return nil, errors.New("drain produced no clusters")
    }
    dominant := clusters[0] // use one dominant shape only
    if dominant.SampleLineIdx < 0 || dominant.SampleLineIdx >= len(lines) {
        return nil, errors.New("drain dominant cluster has no representative line")
    }

    sample := lines[dominant.SampleLineIdx]
    slots := resolveSlots(dominant, lines)
    fields := autoFieldsFromSlots(slots, sample, dominant)
    grok := Render(sample, fields, slots)

    re, err := CompileGrok(grok, nil)
    if err != nil {
        return nil, fmt.Errorf("rendered pattern failed to compile: %w", err)
    }
    matched := EvaluateCoverage(re, lines)
    fmt.Fprintf(diag, "drain: cluster=%d matched=%d/%d\n", dominant.ID, matched, len(lines))

    return &DiscoveredPattern{
        Source:       "drain",
        SourceFamily: "drain",
        Grok:         grok,
        Coverage:     ratio(matched, len(lines)),
        MatchedCount: matched,
        TotalLines:   len(lines),
        SampleLine:   sample,
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
    var best *DiscoveredPattern
    for _, c := range candidates {
        re, err := CompileGrok(c.Grok, nil)
        if err != nil {
            continue
        }
        matched := EvaluateCoverage(re, lines)
        dp := &DiscoveredPattern{
            Source:       c.Source,
            SourceFamily: "fallback",
            Grok:         c.Grok,
            Coverage:     ratio(matched, len(lines)),
            MatchedCount: matched,
            TotalLines:   len(lines),
        }
        if c.Source == "fallback:Message" {
            // The message catch-all always matches; use it if we got nothing better.
            if best == nil { best = dp }
            break
        }
        if dp.Coverage >= 0.80 {
            return dp
        }
        best = pickBetter(best, dp)
    }
    if best == nil {
        // Should be unreachable: %{GREEDYDATA:message} compiles and matches everything.
        panic("log2grok: fallback message pattern failed to compile or match")
    }
    return best
}

// ratio guards against div-by-zero. Discover already rejects empty input,
// but other callers may not.
func ratio(num, denom int) float64 {
    if denom == 0 { return 0 }
    return float64(num) / float64(denom)
}
```

Notice what's preserved:
- `Discover` still returns one `DiscoveredPattern`.
- Structured probes and the library may evaluate many candidates internally, but only one pattern is returned.
- Drain still contributes one dominant-shape pattern, not one pattern per cluster.

### Performance rules
- Compile `KnownPatterns` once with `sync.Once`; never compile every pattern for every `Discover` call.
- Score all library patterns on the bounded sample, then full-evaluate only the top 12 candidates.
- Stop evaluating a candidate early if it cannot mathematically reach the current best `Matched` count (i.e. when `matched + remaining < best.Matched`).
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
    compileOnce      sync.Once
    compiledLib      []compiledPattern
    libraryDiagErrs  []error
)

// compiledKnownPatterns returns all library entries that compiled cleanly.
// Entries that fail to compile are recorded in libraryDiagErrs and exposed
// via LibraryDiagnostics() so CI / tests can fail loudly.
func compiledKnownPatterns() []compiledPattern {
    compileOnce.Do(func() {
        compiledLib = make([]compiledPattern, 0, len(KnownPatterns))
        for _, kp := range KnownPatterns {
            re, err := CompileGrok(kp.Pattern, kp.CustomPatterns)
            if err != nil {
                libraryDiagErrs = append(libraryDiagErrs,
                    fmt.Errorf("library %q: %w", kp.Name, err))
                continue
            }
            compiledLib = append(compiledLib, compiledPattern{Pattern: kp, Regex: re})
        }
    })
    return compiledLib
}

// LibraryDiagnostics is exported via pkg/log2grok and used by TestLibraryCompiles.
func LibraryDiagnostics() []error {
    compiledKnownPatterns()
    return append([]error(nil), libraryDiagErrs...)
}

type candidateResult struct {
    Pattern        KnownPattern
    Compiled       *regexp.Regexp
    SampleCoverage float64
    Matched        int
    FullTotal      int
}

func scoreLibraryOnSample(sample []string) []candidateResult {
    compiled := compiledKnownPatterns()
    out := make([]candidateResult, 0, len(compiled))
    for _, cp := range compiled {
        matched := EvaluateCoverage(cp.Regex, sample)
        out = append(out, candidateResult{
            Pattern:        cp.Pattern,
            Compiled:       cp.Regex,
            SampleCoverage: ratio(matched, len(sample)),
            Matched:        matched,
        })
    }
    return out
}

// betterCandidate compares by integer match count first (avoiding float
// equality checks), then specificity, then typed-capture density (fewer
// GREEDYDATA), then declaration priority.
func betterCandidate(next, best *candidateResult) bool {
    if best == nil {
        return true
    }
    if next.Matched != best.Matched {
        return next.Matched > best.Matched
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
        if in[i].Matched != in[j].Matched {
            return in[i].Matched > in[j].Matched
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

// chooseSample returns up to `max` deterministic representatives of `lines`:
// the first min(1024, max, len(lines)) lines plus evenly-spaced samples from
// the rest. Safe for any non-negative `max` and any input length.
func chooseSample(lines []string, max int) []string {
    if max <= 0 || len(lines) == 0 {
        return nil
    }
    if len(lines) <= max {
        return append([]string(nil), lines...)
    }
    first := 1024
    if first > max { first = max }
    if first > len(lines) { first = len(lines) }

    out := make([]string, 0, max)
    out = append(out, lines[:first]...)

    remaining := max - len(out)
    if remaining <= 0 {
        return out
    }
    rest := len(lines) - first
    if rest <= 0 {
        return out
    }
    step := float64(rest) / float64(remaining)
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
3. Convert unsupported constructs to RE2-safe equivalents (atomic groups → non-capturing; possessive quantifiers → standard; `(?<name>…)` → `(?P<name>…)`; `:type` modifiers preserved as-is — `CompileGrok` will accept and ignore them) or, if not convertible, drop the entry and record it in `BundledLoadDiagnostics`.
4. Expand source-specific top-level patterns into `KnownPattern` entries.
5. Deduplicate by `(normalizeForDedup(regex), sorted field-name set)` (see §8 final composition). The first occurrence wins.
6. Emit `GrokPrimitivesBundled` and `KnownPatternsBundled`. Both must be deterministic across runs (sort by name).

### Representative entries
Use these as style examples and local overrides. The real `KnownPatterns` shipped by the binary is composed in `init()` from three sources:
- `KnownPatternsBundled` (all ingested bundled public patterns that compile under RE2),
- `KnownPatternsCurated` (the curated overrides + source-specific examples below),
- `KnownPatternsCatchall` (the generic timestamp/level fallbacks at the bottom of the table).

Source-specific entries should appear before generic entries and should avoid `%{GREEDYDATA}` until the natural message tail.

```go
var KnownPatternsCurated = []KnownPattern{
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
}

// KnownPatternsCatchall must remain last and low-specificity. These should
// only win when nothing source-specific matched.
var KnownPatternsCatchall = []KnownPattern{
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

Final composition (executed in `init()` because it's not a single expression):

```go
// internal/pattern/library.go
var KnownPatterns []KnownPattern

func init() {
    KnownPatterns = make([]KnownPattern, 0,
        len(KnownPatternsBundled)+len(KnownPatternsCurated)+len(KnownPatternsCatchall))
    KnownPatterns = append(KnownPatterns, KnownPatternsBundled...)
    KnownPatterns = append(KnownPatterns, KnownPatternsCurated...)
    KnownPatterns = append(KnownPatterns, KnownPatternsCatchall...)
    KnownPatterns = dedupKnownPatterns(KnownPatterns)
    sortKnownPatterns(KnownPatterns)
}

// sortKnownPatterns orders by Priority ascending, then Specificity descending,
// then Name ascending for determinism. Catchalls sink to the bottom because
// their Priority is high.
func sortKnownPatterns(in []KnownPattern) {
    sort.SliceStable(in, func(i, j int) bool {
        if in[i].Priority != in[j].Priority {
            return in[i].Priority < in[j].Priority
        }
        if in[i].Specificity != in[j].Specificity {
            return in[i].Specificity > in[j].Specificity
        }
        return in[i].Name < in[j].Name
    })
}

// dedupKnownPatterns removes entries whose (normalized regex, sorted field
// names) tuple matches an earlier entry. The earlier entry wins, so curated
// overrides placed *after* bundled entries do NOT override them — to override
// a bundled entry, ship the override in KnownPatternsCurated *and* delete the
// bundled equivalent from the pack snapshot or rename the override.
func dedupKnownPatterns(in []KnownPattern) []KnownPattern {
    seen := make(map[string]struct{}, len(in))
    out := make([]KnownPattern, 0, len(in))
    for _, kp := range in {
        key := normalizeForDedup(kp.Pattern) + "|" + sortedFieldNames(kp.Pattern)
        if _, dup := seen[key]; dup {
            continue
        }
        seen[key] = struct{}{}
        out = append(out, kp)
    }
    return out
}
```

`normalizeForDedup` should collapse runs of whitespace, strip line anchors, and unify `(?<name>...)` vs `(?P<name>...)` forms. Field-name set is part of the key so that two regexes with the same skeleton but different capture names (one extracting more fields) survive as separate entries.

### Adding new entries
Adding a new format should usually happen through bundled-pack ingestion, not manual editing. Keep entries sorted by `Priority`. Use `Specificity` to prevent generic catchalls from beating source-specific entries at similar coverage. Every new source gets:
- A generated `KnownPattern` entry, or a local `KnownPatternsCurated` entry, or a structured probe.
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
- CSV/TSV: require a stable delimiter count in ≥ 95% of sample rows, computed with `encoding/csv` (never `strings.Split` — CSV with embedded commas inside quoted fields would otherwise be miscounted). If there is a header row, use sanitized header names. Otherwise use `field_1`, `field_2`, etc.
  - Probe-side detection uses `encoding/csv` to handle quoting correctly.
  - The rendered Grok pattern must use a quote-aware element per column rather than `[^,]*` because Grok output is a regex evaluated at use-time without a CSV parser. Use `(?:"(?:""|[^"])*"|[^,]*)` for CSV columns and `[^\t]*` for TSV columns. If a CSV file mixes quoted and unquoted forms unevenly, the probe should still emit the quote-aware form.
  - This is a known fidelity limit: a pattern produced by the CSV probe will match well-formed lines but may not extract fields cleanly when a quoted field itself contains an unescaped quote. Document this in the probe's verbose output.

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
    "COLONURI":          `(?:[A-Za-z][A-Za-z0-9-]{0,31}):[^\s]+`, // generic <scheme>:<value>; not a true URN matcher
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
    // HOSTNAME admits underscores so that Kubernetes pod names, Consul service
    // IDs, and similar internal naming schemes match. Strict RFC 1123 hostnames
    // remain a subset.
    "HOSTNAME":          `[A-Za-z0-9_](?:[A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?(?:\.[A-Za-z0-9_](?:[A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?)*`,
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
    "HTTPVERB":          `(?i:GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|TRACE|CONNECT)`,
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

`CompileGrok` turns a Grok string into a Go regexp. It does three jobs: expand `%{NAME}` references, deduplicate colliding capture names, and anchor the result.

```go
// internal/pattern/compile.go
package pattern

import (
    "errors"
    "fmt"
    "regexp"
    "strings"
)

// grokRefRe matches %{NAME}, %{NAME:field}, and %{NAME:field:type}.
// The optional :type tail (Logstash type-cast) is parsed for compatibility
// and ignored at compile time.
var grokRefRe = regexp.MustCompile(`%\{(\w+)(?::(\w+)(?::(\w+))?)?\}`)

// goNamedGroupRe matches Go's (?P<name>...) named-group opener inside an
// already-expanded body. We use it to detect, count, and rename collisions.
var goNamedGroupRe = regexp.MustCompile(`\(\?P<(\w+)>`)

// logstashNamedGroupRe matches Logstash's (?<name>...) named-group opener,
// which we translate to Go's (?P<name>...) form during expansion.
var logstashNamedGroupRe = regexp.MustCompile(`\(\?<(\w+)>`)

// CompileGrok expands %{NAME}, %{NAME:field}, and %{NAME:field:type} references
// recursively, then compiles the result as a Go regexp anchored to match a
// whole line.
//
// The output regex matches "^<expanded pattern>\r?$" so it works on both
// Unix and Windows line endings.
//
// Capture-name collisions that arise from recursive expansion are rewritten
// to <name>_2, <name>_3, etc. so Go's regexp engine accepts the result.
func CompileGrok(pattern string, extras map[string]string) (*regexp.Regexp, error) {
    used := make(map[string]int)
    expanded, err := expandGrok(pattern, extras, used, 0)
    if err != nil {
        return nil, err
    }
    return regexp.Compile(`^` + expanded + `\r?$`)
}

// expandGrok walks the pattern, replacing %{...} references with their
// expanded bodies. `used` tracks every named-capture group already emitted
// so we can deduplicate colliding names.
func expandGrok(s string, extras map[string]string, used map[string]int, depth int) (string, error) {
    if depth > 16 {
        return "", errors.New("grok expansion too deep (cycle?)")
    }

    // Translate Logstash-style (?<name>...) to Go-style (?P<name>...) before
    // we start matching. The bundled-pack loader normalizes most of these,
    // but be defensive — user-supplied extras may contain either form.
    s = logstashNamedGroupRe.ReplaceAllString(s, `(?P<$1>`)

    var firstErr error
    out := grokRefRe.ReplaceAllStringFunc(s, func(match string) string {
        sub := grokRefRe.FindStringSubmatch(match)
        name, field := sub[1], sub[2]
        // sub[3] is the optional :type modifier — accepted for compatibility
        // but ignored. Downstream tools may parse it from the original Grok
        // string if they want.
        _ = sub[3]

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

        // Recursively expand the body so that, e.g., IP -> (?:IPV6|IPV4) -> ...
        expanded, err := expandGrok(body, extras, used, depth+1)
        if err != nil {
            if firstErr == nil { firstErr = err }
            return match
        }

        // Deduplicate any named groups that already appeared. This handles the
        // cases where:
        //   - the outer reference is %{NAME:field} and `field` was used elsewhere,
        //   - the body itself contains named captures (e.g. SYSLOGFACILITY,
        //     SYSLOGBASE, SYSLOGPROG) that collide with earlier names.
        expanded = dedupNamedGroups(expanded, used)

        if field != "" {
            unique := uniqueName(field, used)
            return "(?P<" + unique + ">" + expanded + ")"
        }
        return "(?:" + expanded + ")"
    })

    return out, firstErr
}

func uniqueName(field string, used map[string]int) string {
    if used[field] == 0 {
        used[field] = 1
        return field
    }
    used[field]++
    return fmt.Sprintf("%s_%d", field, used[field])
}

// dedupNamedGroups walks an expanded body and rewrites every (?P<name>...)
// opener so that names collide neither within the body nor with earlier
// captures. The first time a name appears it is kept as-is; subsequent
// occurrences become name_2, name_3, etc.
func dedupNamedGroups(body string, used map[string]int) string {
    return goNamedGroupRe.ReplaceAllStringFunc(body, func(open string) string {
        m := goNamedGroupRe.FindStringSubmatch(open)
        unique := uniqueName(m[1], used)
        return "(?P<" + unique + ">"
    })
}
```

### Why the `(?:...)` and `(?P<...>...)` wrapping?
- `(?P<name>...)` is Go's syntax for a named capture group. Logstash uses `(?<name>...)`. `expandGrok` translates one to the other up front.
- `(?:...)` is a non-capturing group. We wrap unnamed primitive expansions in this so that quantifiers and alternation in the surrounding Grok pattern bind correctly. Example: `%{NUMBER}+` should mean "one or more numbers," not "match a number, then a literal `+`."

### Why deduplicate names instead of stripping them?
Logstash semantics promote nested named captures to top-level fields (`%{SYSLOGBASE}` exposes `timestamp`, `logsource`, `pid`). Go's regexp engine refuses to compile a regex with duplicate group names. Stripping nested names would silently lose useful captures. Renaming collisions to `pid_2` preserves the data and lets Go compile the result.

### Why `\r?$` at the end?
Some log files have Windows line endings (`\r\n`). When you read them line-by-line in Go, the `\r` stays attached to the line. Allowing an optional trailing `\r` means our patterns work on both Unix and Windows logs.

### UTF-8 safety
The Grok primitives in §9 are all ASCII regexes, but the *literal* portions of patterns produced by the Drain/Render path come straight from sample log lines, which may be UTF-8. The renderer (§12) iterates by rune and quotes whole runes via `regexp.QuoteMeta`, never by byte. Do not split a multi-byte rune across `regexp.QuoteMeta` calls — that produces a regex that compiles but never matches.

---

## 11. The Drain Fallback

`axiomhq/drain3` clusters log lines by their structure — it figures out that "User X logged in from IP Y" and "User Z logged in from IP W" share the template "User `<*>` logged in from IP `<*>`."

### Adapter interface
We do not call `axiomhq/drain3` directly from `discovery.go`. The exact shape of the upstream API has changed between versions and not all of its fields are part of the public surface. Instead, `internal/pattern/drain.go` defines a small adapter and an implementation file for the chosen drain3 version. This isolates upstream churn behind one file and makes the rest of the package testable with a mock.

```go
// internal/pattern/drain.go
package pattern

// drainBackend is the only abstraction the rest of the package depends on.
// The default implementation, defaultDrainBackend, calls into axiomhq/drain3
// and lives in drain_backend.go. Tests substitute a mock.
type drainBackend interface {
    // Train ingests every line and builds clusters.
    Train(lines []string) error

    // Templates returns clusters sorted by descending support
    // (largest cluster first).
    Templates() []drainTemplate

    // ClusterIDOf returns the ID of the cluster that `line` was assigned to,
    // and false if the backend cannot classify it.
    ClusterIDOf(line string) (int, bool)

    // Tokenize returns the same tokens drain3 used internally to score `line`.
    // Returning these directly removes the parity problem of reimplementing
    // drain3's tokenizer in our own code.
    Tokenize(line string) []string
}

// drainTemplate is the post-clustering shape we consume. It is intentionally
// flatter than drain3's Template so that the rest of the package never imports
// drain3 types.
type drainTemplate struct {
    ID         int
    Tokens     []string // each entry is either a literal token or "<*>" for a slot
    LineCount  int
}

var defaultBackend drainBackend = newAxiomDrain3Backend()

type cluster struct {
    ID            int
    Template      []tplPart
    TokenCount    int
    LineCount     int
    SampleLineIdx int  // first line in the input that belongs to this cluster
}

type tplPart struct {
    IsSlot bool   // true: variable position; false: literal token
    Token  string // literal value when !IsSlot
}

func trainDrain(lines []string) ([]cluster, error) {
    return trainDrainWith(defaultBackend, lines)
}

func trainDrainWith(b drainBackend, lines []string) ([]cluster, error) {
    if err := b.Train(lines); err != nil {
        return nil, err
    }
    tmpls := b.Templates()
    sampleIdx := indexFirstSampleLine(lines, b)

    out := make([]cluster, 0, len(tmpls))
    for _, t := range tmpls {
        idx, ok := sampleIdx[t.ID]
        if !ok {
            idx = -1
        }
        out = append(out, cluster{
            ID:            t.ID,
            Template:      buildTplParts(t),
            TokenCount:    len(t.Tokens),
            LineCount:     t.LineCount,
            SampleLineIdx: idx,
        })
    }
    return out, nil
}

// drainExtraDelimiters is passed into the backend's configuration. Keep it
// conservative. Do not add ":" because it breaks timestamps, IPv6,
// host:port values, and Java logger names into fragments.
var drainExtraDelimiters = []string{"=", ",", "|"}

func indexFirstSampleLine(lines []string, b drainBackend) map[int]int {
    out := make(map[int]int, 32)
    for i, line := range lines {
        if id, ok := b.ClusterIDOf(line); ok {
            if _, seen := out[id]; !seen {
                out[id] = i
            }
        }
    }
    return out
}

// buildTplParts converts a drainTemplate's Tokens slice into the interleaved
// literal/slot sequence the renderer wants.
func buildTplParts(t drainTemplate) []tplPart {
    parts := make([]tplPart, 0, len(t.Tokens))
    for _, tok := range t.Tokens {
        if tok == "<*>" {
            parts = append(parts, tplPart{IsSlot: true})
        } else {
            parts = append(parts, tplPart{IsSlot: false, Token: tok})
        }
    }
    return parts
}
```

`drain_backend.go` is where the actual drain3 calls live. Its job is small: call drain3 with the configured similarity threshold and `drainExtraDelimiters`, then translate each upstream template into a `drainTemplate`, including a `Tokens []string` whose length equals `t.TokenCount` and whose `<*>` markers stand in for slot positions. Pin the drain3 dependency in `go.mod` and update only this file when bumping versions.

### Tokenization — bridging tokens and characters
Drain operates on tokens (words). The Grok pattern operates on characters. We need to know where each token starts and ends in the original line. Because `drainBackend.Tokenize` returns the exact sequence drain3 used, the only thing we have to do is locate each token in the source line — we don't need a parallel tokenizer.

```go
// internal/pattern/tokenize.go
package pattern

import "strings"

type tokenSpan struct {
    Start, End int
    Text       string
}

// tokenSpansOf locates each `tokens[i]` inside `line` in order. It walks the
// line forward, using the next token as a needle, so two equal tokens in a
// line still get distinct spans.
//
// If a token cannot be located (e.g. drain3 normalized it before clustering),
// the corresponding span is the empty range and the caller falls back to
// re-tokenizing.
func tokenSpansOf(line string, tokens []string) []tokenSpan {
    out := make([]tokenSpan, 0, len(tokens))
    cursor := 0
    for _, tok := range tokens {
        if tok == "<*>" || tok == "" {
            out = append(out, tokenSpan{Start: cursor, End: cursor})
            continue
        }
        idx := strings.Index(line[cursor:], tok)
        if idx < 0 {
            out = append(out, tokenSpan{Start: cursor, End: cursor})
            continue
        }
        start := cursor + idx
        end := start + len(tok)
        out = append(out, tokenSpan{Start: start, End: end, Text: tok})
        cursor = end
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
    if c.SampleLineIdx < 0 || c.SampleLineIdx >= len(lines) {
        return nil
    }
    sample := lines[c.SampleLineIdx]
    sampleTokens := defaultBackend.Tokenize(sample)
    if len(sampleTokens) != c.TokenCount {
        return nil // backend disagrees with itself; bail
    }
    spans := tokenSpansOf(sample, sampleTokens)

    var slots []slotRange
    var slotPos []int
    for i, p := range c.Template {
        if !p.IsSlot {
            continue
        }
        if i >= len(spans) {
            return nil
        }
        slots = append(slots, slotRange{
            SlotIndex: i,
            Start:     spans[i].Start,
            End:       spans[i].End,
        })
        slotPos = append(slotPos, i)
    }

    // Collect the actual values that appeared in each slot, by re-tokenizing
    // every line that belongs to this cluster.
    for _, line := range lines {
        toks := defaultBackend.Tokenize(line)
        if len(toks) != c.TokenCount { continue }
        for si, pos := range slotPos {
            if pos >= len(toks) { continue }
            slots[si].Values = append(slots[si].Values, toks[pos])
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

// canonicalNames is intentionally conservative. It aliases obvious synonyms
// for fields whose meaning is the same regardless of context (`ts` → timestamp,
// `lvl` → level). It does NOT canonicalize ambiguous names like `method` or
// `path`, which mean different things in HTTP, RPC, and database contexts;
// those keep their original token names.
var canonicalNames = map[string]string{
    "ts": "timestamp", "time": "timestamp", "timestamp": "timestamp",
    "lvl": "level", "levelname": "level", "severity": "level",
    "msg": "message", "message": "message",
    "logger_name": "logger", "log": "logger",
    "statuscode": "status_code",
    "latency": "duration",
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

`Render` walks the sample line one rune at a time (not byte — see UTF-8 note below). At each position:
1. If a field starts here, emit `%{TYPE:name}` and skip to the end of the field.
2. Else if an uncovered Drain slot starts here, emit `%{NOTSPACE:unparsed_N}`. (The current pipeline only produces token-sized slots — no slot ever spans whitespace — so a `%{GREEDYDATA:unparsed_N}` form is intentionally not used. Bringing back multi-token slots is future work.)
3. Else emit one regex-escaped rune and advance.

Each `field.Start/End` is built directly from a slot's range in `autoFieldsFromSlots`, so a field always exactly covers its slot. The renderer therefore matches by exact range, not by sub-range. (Earlier drafts of this spec described a sub-range case; the renderer never supported it and the test description was out of date.)

```go
// internal/pattern/render.go
package pattern

import (
    "regexp"
    "sort"
    "strconv"
    "strings"
    "unicode/utf8"
)

func Render(sample string, fields []field, slots []slotRange) string {
    sorted := append([]field(nil), fields...)
    sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

    // Which slots are already covered by a field?
    covered := make(map[int]bool)
    for _, s := range slots {
        for _, f := range sorted {
            if f.Start == s.Start && f.End == s.End {
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
        // Advance one whole rune at a time so multi-byte UTF-8 sequences are
        // quoted as a single character. Quoting per byte would split a rune
        // across two QuoteMeta calls and produce a regex that compiles but
        // never matches.
        r, size := utf8.DecodeRuneInString(sample[cursor:])
        if r == utf8.RuneError && size <= 1 {
            // Invalid byte — preserve it as-is via QuoteMeta so we don't lose
            // alignment with the original line, even if it can never match.
            b.WriteString(regexp.QuoteMeta(sample[cursor : cursor+1]))
            cursor++
            continue
        }
        b.WriteString(regexp.QuoteMeta(sample[cursor : cursor+size]))
        cursor += size
    }
    return b.String()
}
```

`regexp.QuoteMeta` is important: the literal parts of the sample line (spaces, brackets, slashes) might contain regex metacharacters. We need them escaped so the Grok pattern matches them literally.

---

## 13. Tests

A junior engineer should be able to run `make test` and see green before submitting any change. The tests below are required.

### 13.1 `TestLibraryCompiles` — every library entry compiles, and library diagnostics is empty
Catches typos and missing primitives at CI time, not runtime. Uses `LibraryDiagnostics` so a single failure surfaces every broken entry instead of just the first.

```go
func TestLibraryCompiles(t *testing.T) {
    if errs := LibraryDiagnostics(); len(errs) > 0 {
        for _, e := range errs {
            t.Errorf("%v", e)
        }
        t.Fatalf("%d library entries failed to compile", len(errs))
    }
    for _, kp := range KnownPatterns {
        t.Run(kp.Name, func(t *testing.T) {
            if _, err := CompileGrok(kp.Pattern, kp.CustomPatterns); err != nil {
                t.Fatalf("library entry %q failed to compile: %v", kp.Name, err)
            }
        })
    }
}
```

### 13.2 `TestLibraryMatchesItsExample` — required examples for curated, opportunistic for bundled
Curated entries (everything in `KnownPatternsCurated` and `KnownPatternsCatchall`) MUST ship with `testdata/library_examples/<slug>.txt`. Bundled entries (entries that come from `KnownPatternsBundled`) may ship one; if they do, the test runs against it; if they don't, that entry is skipped. This keeps the example-file count manageable for the 100s of bundled entries while still gating the curated catalog tightly.

```go
func TestLibraryMatchesItsExample(t *testing.T) {
    bundledNames := nameSetOf(KnownPatternsBundled)
    for _, kp := range KnownPatterns {
        t.Run(kp.Name, func(t *testing.T) {
            path := filepath.Join("testdata/library_examples", slug(kp.Name)+".txt")
            sample, err := os.ReadFile(path)
            if err != nil {
                if bundledNames[kp.Name] {
                    t.Skipf("no example file for bundled entry %s (optional)", kp.Name)
                }
                t.Fatalf("missing example file %s: %v", path, err)
            }

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
Add a test under `test/benchmark` (or another non-`internal` folder) that imports `log2grok/pkg/log2grok` and calls `Discover`, `CompileGrok`, `EvaluateCoverage`, and `LibraryDiagnostics`. This prevents regressions where only internal APIs compile.

### 13.2b `TestBundledCoverage` — all bundled packs contribute, anchored to named patterns
Assert that the bundled output is non-empty *and* contains a fixed set of named patterns/primitives. Hard-coded count thresholds (`>= 200` etc.) are too brittle when the loader filters RE2-incompatible entries — anchor on patterns that the corpora rely on instead.

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

    requiredPrimitives := []string{
        "IPV4", "IPV6", "IPORHOST", "HTTPDATE", "TIMESTAMP_ISO8601",
        "SYSLOGTIMESTAMP", "LOGLEVEL", "QUOTEDSTRING", "UUID", "NUMBER",
    }
    for _, name := range requiredPrimitives {
        if _, ok := GrokPrimitives[name]; !ok {
            t.Errorf("required primitive %q missing from merged GrokPrimitives", name)
        }
    }

    requiredPatterns := []string{
        "Nginx Access Combined", "Apache Combined", "Syslog RFC3164",
        "Syslog RFC5424", "HAProxy HTTP", "AWS ALB",
    }
    have := make(map[string]bool, len(KnownPatterns))
    for _, kp := range KnownPatterns {
        have[kp.Name] = true
    }
    for _, name := range requiredPatterns {
        if !have[name] {
            t.Errorf("required pattern %q missing from KnownPatterns", name)
        }
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

### 13.7 `TestDrainBackendSelfConsistent` — backend Tokenize/Templates agree
On a 1000-line corpus, train the default `drainBackend`, then for every cluster assert that `len(b.Tokenize(line)) == len(template.Tokens)` for every line that `ClusterIDOf` assigns to that cluster. This is the parity check that actually matters: it guarantees the tokenization used for clustering is the same one we'll use to locate slot byte ranges. There is no need to reimplement drain3's tokenizer.

### 13.8 `TestCompileGrokRecursion` — circular references don't loop forever
Pass an `extras` map where `A → B → A`. Assert `CompileGrok` returns an error rather than hanging.

### 13.9 `TestCompileGrokDuplicateNames` — deduplicates colliding captures
Build a pattern that references a primitive whose body has a named capture (e.g. `SYSLOGFACILITY`) and uses the same name elsewhere. Assert `CompileGrok` succeeds and the resulting regex's `SubexpNames()` contains both `priority` and `priority_2`.

### 13.10 `TestCompileGrokTypeModifier` — accepts `:type` modifier
Compile `%{NUMBER:duration:float}` and assert it is equivalent to `%{NUMBER:duration}`. This guarantees bundled Logstash patterns with type modifiers don't break ingest.

### 13.11 `TestCandidateScoring` — source-specific beats generic
Build fake candidate results with identical match counts but different `Specificity`, `Priority`, and `%{GREEDYDATA}` counts. Assert `betterCandidate` chooses the most useful pattern.

### 13.12 Unit tests for utilities
- `tokenSpansOf`: known-input/known-output pairs covering leading/trailing space, runs of spaces, repeated tokens, `<*>` placeholders.
- `classifySlot`: 95%-threshold edge cases (exactly 95%, just under, mixed).
- `suggestName`: `key=value` → `key`, `key:value` → `key`, no preceding token → fallback to NameHint.
- `Render`: known fields and slots → known output. At minimum: a field that exactly covers a slot, a slot uncovered by any field, a multi-byte UTF-8 literal between two slots (must round-trip without producing an invalid regex).
- `chooseSample`: `max=0`, `len(lines)=0`, `len(lines)<=max`, `max < 1024`, `max=4096` with millions of lines — assert no panic and stable output length.

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
    "errors"
    "flag"
    "fmt"
    "io"
    "os"

    l2g "log2grok/pkg/log2grok"
)

func main() {
    threshold := flag.Float64("threshold", 0.85, "library auto-accept threshold (0.0-1.0)")
    maxLines  := flag.Int("max-lines", 100000, "stop reading after this many lines (0 = unlimited)")
    verbose   := flag.Bool("verbose", false, "log diagnostics to stderr")
    quiet     := flag.Bool("quiet", false, "suppress trailing comment line on stdout")
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

    lines, truncated, err := readLines(src, *maxLines)
    if err != nil {
        fmt.Fprintln(os.Stderr, "error:", err)
        os.Exit(1)
    }

    var diag io.Writer
    if *verbose {
        diag = os.Stderr
    }
    dp, err := l2g.Discover(lines, l2g.Options{
        LibraryThreshold: *threshold,
        MaxLines:         *maxLines,
        Verbose:          *verbose,
        Diagnostics:      diag,
    })
    if err != nil {
        // Empty input is a usage error, not an internal error.
        if errors.Is(err, l2g.ErrEmptyInput) {
            fmt.Fprintln(os.Stderr, "error: input has no non-empty lines")
            os.Exit(1)
        }
        fmt.Fprintln(os.Stderr, "error:", err)
        os.Exit(2)
    }

    fmt.Println(dp.Grok)
    if !*quiet {
        suffix := ""
        if truncated {
            suffix = fmt.Sprintf(" (truncated at %d)", *maxLines)
        }
        fmt.Printf("# matched %d / %d lines (%.1f%%) -- %s%s\n",
            dp.MatchedCount, dp.TotalLines, dp.Coverage*100, dp.Source, suffix)
    }
}

// openInput returns stdin for "-" (only when stdin has piped data) or opens
// the file otherwise. When stdin is attached to a TTY with no piped input,
// log2grok would otherwise block forever waiting for typed input — fail fast.
func openInput(path string) (io.ReadCloser, error) {
    if path == "-" {
        info, err := os.Stdin.Stat()
        if err != nil {
            return nil, err
        }
        if (info.Mode() & os.ModeCharDevice) != 0 {
            return nil, errors.New("stdin is a TTY; pipe data into log2grok or pass a filename")
        }
        return io.NopCloser(os.Stdin), nil
    }
    return os.Open(path)
}

// readLines reads up to `max` lines (0 = unlimited). Truncated is true when
// the input had more lines than max so the caller can flag the output.
func readLines(r io.Reader, max int) (lines []string, truncated bool, err error) {
    s := bufio.NewScanner(r)
    s.Buffer(make([]byte, 1024*1024), 8*1024*1024) // allow long lines (8 MiB max)
    lines = make([]string, 0, 1024)
    for s.Scan() {
        lines = append(lines, s.Text())
        if max > 0 && len(lines) >= max {
            truncated = s.Scan() // true if there's at least one more line
            break
        }
    }
    return lines, truncated, s.Err()
}
```

That's the entire CLI. Around fifty lines of substance. Resist the urge to add anything else here — keep `main.go` boring and put logic in `internal/pattern/`.

---

## 16. Build, Run, Ship

### `go.mod` (required)
The repository must include a valid module file so external suites can import `pkg/log2grok`. `go.mod` requires a real semver — `latest` is not legal syntax. Pin a specific version of drain3, then bump it when needed via `go get -u github.com/axiomhq/drain3@<version>`.

```
module log2grok

go 1.22

require github.com/axiomhq/drain3 v0.0.0-20240501000000-000000000000
```

Replace the `v0.0.0-...` placeholder with whatever `go mod tidy` resolves (typically a `v0.x.y` tag or a pseudo-version of the latest commit). Verify in CI by running `go mod verify` and `go build ./...` against a clean module cache.

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
# matched 5000 / 5000 lines (100.0%) -- library:Nginx Access Combined

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
- **Sample line** — one representative line from the input that the chosen pattern is built around. In the library path the field is left empty (the pattern wasn't built from a specific line). In the Drain path it's the first line that belongs to the dominant cluster, exposed on `DiscoveredPattern.SampleLine`.
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
3. Tune the similarity threshold in `drain_backend.go` (lower = more clusters; higher = fewer, broader clusters).

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
Run with `-verbose` to see what was tried. If a library entry that *should* have matched didn't, check whether its primitives are too strict. If Drain produced something weird, look at the similarity threshold in `drain_backend.go`. Failing all that, add the format to the library — it's the most predictable fix.

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
