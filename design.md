# log2grok — Design & Extension Guide

Companion to `SPEC.md`. SPEC describes *what* the tool does. This doc describes *how* the code does it and *where* to edit when you add a new log format.

## 1. Top-Level Flow

Entry: `pkg/log2grok/api.go` → `internal/pattern.Discover()` (`internal/pattern/discovery.go:35`).

`Discover` runs three stages concurrently, read in priority order. Each stage may auto-accept (return immediately on high coverage), otherwise it contributes a candidate and the best across stages wins. Final fallback is a fixed safe pattern.

```
input lines
   │
   ▼
normalizeLines  ── strip BOM (byte order marks), drop blanks
   │
   ▼
chooseSample    ── first 1024 + stratified sample, cap 4096
   │
   ├─► tryStructured  (probes: JSON variants, logfmt, CEF/LEEF, W3C/IIS, CSV, TSV)
   │       coverage ≥ threshold  → return
   │
   ├─► tryLibrary     (regex KnownPatterns, scored on sample then full)
   │       coverage ≥ threshold (default 0.85) → return
   │
   ├─► tryTiling + tryTextEnvelope  (from-scratch inference for unknown formats)
   │       coverage ≥ threshold → return
   │
   └─► best-of-stages, else deriveSafeFallback
```

Cross-stage tie-break: `pickBetter` (`discovery.go`) — match count, then typed-capture count (non-`GREEDYDATA`, non-blank field), then family rank (`library < structured < inferred < fallback`).

## 2. The Three Stages in Detail

### 2.1 Structured (`internal/pattern/identify.go`)

Each `structuredProbe` has a `Likely` predicate and a `Render` function. Probes run in declaration order. CSV / TSV emit literal-format expressions without named captures (matches the golden corpus shape `[^\t]*\t[^\t]*…`). JSON-family probes emit `\{%{GREEDYDATA:json}\}` — the JSON body itself is left to downstream consumers.

Why no field captures for CSV/TSV: column semantics aren't recoverable from delimiter-separated data alone, and the golden tests expect a literal shape that the user can decorate.

### 2.2 Library (`library.go`, `library_curated.go`, `score.go`)

`KnownPatterns` is the deduped, sorted view of `KnownPatternsLibrary`, which is loaded verbatim from `internal/pattern/embedded/patterns.json` at init time (or replaced from `~/.log2grok/patterns.json` when `LoadConfig` is called). Composition lives in `composeKnownPatterns` (`library.go`):

```
KnownPatternsLibrary  (single ordered list; tier encoded by Priority)
    → dedup → sort
```

Tier convention inside `patterns.json` (set by `Priority`):

- `1–4` — corpus-derived "golden" entries; must win against generic catchalls.
- `10–199` — curated source-family entries (web access ~10–19, system ~30–39, app ~80–99, db ~110–119, messaging ~130–139, security ~150–159).
- `900–999` — generic catchalls; must remain last and low-specificity.

Sort order: `Priority ASC`, then `Specificity DESC`, then `Name`.

Scoring (`scoreLibraryOnSample` + `betterCandidate`, `score.go`):

1. `EvaluateCoverage` against sample — rejects entries that don't actually match.
2. Top 12 by sample coverage compete on the **full** input.
3. Winner: highest matched count → highest specificity → fewest `GREEDYDATA` → lowest priority number.

Each `KnownPattern` may declare `CustomPatterns` (a private primitive map). `Discover` propagates these to the returned `DiscoveredPattern`; callers pass them to `CompileGrok`.

### 2.3 Inferred (`tiler.go`, `text_envelope.go`)

The tiler is the from-scratch inference engine for formats the library does
not know. Its core principle: **structure comes from one template line;
semantics come from the population.**

1. **Segment** a representative line into units: literal delimiter runs,
   quoted regions, multi-token composite timestamps (`tileComposites` — each
   is a hand recogniser plus the Grok expression rendered for the span), and
   plain tokens.
2. **Probe** the whole sample with a relaxed alignment regex (every token
   becomes `(\S+)`, every quoted region `"([^"]*)"`) and collect each slot's
   observed value set.
3. **Type** each slot by voting the most specific primitive
   (`tileSingleSpecs`, derived from the real Grok primitives so classifier
   and renderer can never disagree) that matches *all* observed values.
   Constant keywords demote to literals; constant *data* does not — a slot
   that matches a high-confidence (Promote) type, or whose values share an
   internal `:/​#@` separator signature with at least one high-confidence
   column (`192.0.1.1:443`, `0/0/1/2/3`, `HTTP/1.1`), stays typed. A slot
   that is sometimes `-` gets a dash alternation `(?:%{INT:bytes}|-)`.
   Quoted regions are tiled recursively when their inner structure is stable
   across the sample, else they become `%{DATA}`. A grammar rule types the
   token after a leading RFC3164/RFC5424 timestamp as `%{HOSTNAME:hostname}`.
4. **Brute-force** over up to 16 template lines (deduped by char-class
   skeleton), score each tiling on the full input, keep the best — then try
   relaxing the tail into `%{GREEDYDATA:message}` at successive field
   boundaries (`relaxTail`) and keep the relaxation only when it buys real
   coverage (varied message tails).

Honesty gates (`tilingMinLines`, `tilingMinCoverage`, `tilingMinPromoted`)
keep the tiler from overfitting tiny or heterogeneous inputs.
`TestFieldRecoveryHarness` (`recovery_harness_test.go`) measures the engine:
with library and structured probes disabled, it scores how much of the
expert-written golden grok's field structure the engine reconstructs
(LCS over high-information field classes). Inspect individual cases with
`TILE_DUMP=case1,case2 go test -run TestTileDump -v`.

`tryTextEnvelope` is a cheap fixed-shape probe (timestamp + level +
component + message) kept alongside the tiler; the better of the two is the
stage candidate.

### 2.4 Drain (removed)

The original stage 4 — `drain3` clustering with per-token slot typing — was
removed after the tiler made it redundant: on the golden corpus the tiler
recovers 0.999 of the experts' field structure where drain managed 0.238
(134 wins, 0 losses, 1 tie), and the tiler's per-shape probes subsume
drain's clustering in both `Discover` (shape unions) and `DiscoverMulti`
(one candidate per shape). The `axiomhq/drain3` dependency is gone.

## 3. The Pattern DSL

`CompileGrok(pattern, extras)` (`compile.go:24`) expands `%{NAME}`, `%{NAME:field}`, `%{NAME:field:type}` references into RE2, anchors with `^…\r?$`, and compiles. Resolution order for `NAME`: `extras` → `GrokPrimitives` (loaded from `internal/pattern/embedded/primitives.json`).

Quirks worth remembering:

- `:type` (the Logstash type-cast) is parsed and **ignored** — kept for compatibility.
- `(?<name>…)` (Logstash named-group syntax) is rewritten to Go's `(?P<name>…)` before expansion.
- Colliding capture names are auto-renamed `name_2`, `name_3`, … so a primitive that internally captures `pid` can be referenced twice in one pattern.
- Field names get sanitised — non-alphanumeric/underscore becomes `_`, leading-digit gets `_` prefix.

## 4. Adding a New Log Format

Pick the lowest-cost lever. In rough order of preference:

### 4.1 New Library Entry (most common)

Most "support format X" requests reduce to: write one regex, drop it as a new JSON object in `internal/pattern/embedded/patterns.json`. Steps:

1. Pick a representative input. Write the Grok by hand:
   ```json
   {
     "name": "Vector Access",
     "pattern": "\\[%{TIMESTAMP_ISO8601:timestamp}\\] %{LOGLEVEL:level} %{NOTSPACE:source} %{GREEDYDATA:message}",
     "priority": 50,
     "specificity": 92
   }
   ```

2. Specificity guide:
   - **99**: corpus-exact match, only one shape.
   - **90-95**: source-specific reference.
   - **70-85**: generic shape covering many sources.
   - **≤30**: catchall.

3. Priority decides the tier and is a tie-breaker after specificity. Lower wins. Use the existing band for the family: golden ~1–4, web access ~10–19, system ~30–39, app ~80–99, db ~110–119, messaging ~130–139, security ~150–159, generic catchall ~900–930.

4. If the pattern needs a primitive that doesn't exist, see §4.2. If the primitive is private to this entry only, attach it via `customPatterns`:
   ```json
   {
     "name": "MyVendor Format",
     "pattern": "%{MYVENDOR_TS:timestamp} %{GREEDYDATA:message}",
     "priority": 80,
     "specificity": 85,
     "customPatterns": {
       "MYVENDOR_TS": "\\d{4}\\.\\d{3}T\\d{2}:\\d{2}:\\d{2}"
     }
   }
   ```

5. Order in the JSON file matters only for dedup tie-breaking — when two entries normalize to the same regex + field-name set, the first one wins. Otherwise the runtime sort by `priority`/`specificity` decides matching order.

6. Add a corpus case (§5) to lock in behaviour.

### 4.2 New Primitive

Edit `internal/pattern/embedded/primitives.json`. This is the only primitive table — it's the sole input to `CompileGrok` for `%{NAME}` lookup. Keep regex RE2-safe (no lookaround, no backrefs, no possessive quantifiers).

Test impact: extending `LOGLEVEL` or `TIMESTAMP_ISO8601` *broadens* what every dependent pattern matches. Run `make bench` before/after.

### 4.3 New Structured Probe

Edit `internal/pattern/identify.go`. Add a `structuredProbe{Name, Likely, Render}` and append to `structuredProbes`. Order matters — first-likely wins within stage 1.

`Likely` should be cheap. `Render` returns `(grok, source, ok)`. If `Render` returns `ok=false`, the probe is skipped.

Use this lever when the format is **schema-driven** (header line, key-value, fixed delimiter) — not when it's "yet another regex."

## 5. Golden Test Corpus

`test/benchmark/cases/<name>/` holds:

```
input.log         — sample lines
expected.grok     — exact Grok string Discover should return
meta.json         — case_name, expected_grok (escaped), expected_source, family
```

The test (`test/benchmark/benchmark_test.go`) runs `Discover` and asserts:

1. `dp.Grok == expected` (exact string).
2. The compiled regex matches **100%** of `input.log`.

Adding a case:

```
mkdir test/benchmark/cases/myformat
echo "...sample line 1..." >  test/benchmark/cases/myformat/input.log
echo "...sample line 2..." >> test/benchmark/cases/myformat/input.log
echo '<exact grok>'        >  test/benchmark/cases/myformat/expected.grok
cat > test/benchmark/cases/myformat/meta.json <<EOF
{"case_name":"myformat","expected_grok":"<escaped>","expected_source":"library:My Format","family":"myformat"}
EOF
make bench
```

`expected_source` is informational — the test does not enforce it. The grok-string match does.

Cases are committed source-of-truth. Edit them directly — no generator. A hand-written 16-line input is enough.

## 6. Where Things Live

| File | Role |
|------|------|
| `pkg/log2grok/api.go` | Public API. Re-exports `Discover`, `CompileGrok`, `EvaluateCoverage`. |
| `internal/pattern/discovery.go` | Stage orchestration. `DiscoveredPattern` definition. |
| `internal/pattern/identify.go` | Structured probes (JSON, logfmt, CEF, W3C, CSV, TSV). |
| `internal/pattern/library.go` | `KnownPattern` type, dedup, sort, `composeKnownPatterns`. |
| `internal/pattern/library_curated.go` | `KnownPatternsLibrary` declaration (loaded from `embedded/patterns.json`). |
| `internal/pattern/score.go` | Sample-then-full scoring; `betterCandidate`. |
| `internal/pattern/embedded.go` | `//go:embed` of `embedded/*.json` + `RefreshLibrary`. |
| `internal/pattern/embedded/patterns.json` | Single source-of-truth for built-in pattern entries. |
| `internal/pattern/embedded/primitives.json` | Single source-of-truth for `GrokPrimitives`. |
| `internal/pattern/config.go` | `LoadConfig`/`ResetConfig` — disk overlay seed/load/recover. |
| `internal/pattern/primitives.go` | `GrokPrimitives` declaration (loaded from `embedded/primitives.json`). |
| `internal/pattern/compile.go` | `CompileGrok` — `%{}` expansion + anchored RE2 compile. |
| `internal/pattern/tiler.go` | From-scratch inference: segment → probe → population-vote typing → render. |
| `internal/pattern/text_envelope.go` | Fixed-shape timestamp/level/component probe. |
| `internal/pattern/recovery_harness_test.go` | Field-recovery measurement vs the expert golden groks. |
| `internal/pattern/naming.go` | Field-name canonicalisation shared by the inference engine. |
| `internal/pattern/coverage.go` | `EvaluateCoverage` — anchored regex against full input. |
| `test/benchmark/` | Golden corpus + benchmarks. |
| `cmd/log2grok/` | CLI entrypoint. |
| `cmd/buildpacks/` | Library health-check stub (prints library/primitive counts). |

## 7. Common Failure Modes

- **Pattern built but doesn't match**: most often `TIMESTAMP_ISO8601` or `HTTPDATE` not consuming a trailing token. Inspect the literal regex with `CompileGrok(pattern, nil).String()` and walk it against an input line.
- **Library entry never wins**: another entry has same/higher specificity *and* matches the same lines. Either bump your specificity or narrow the competitor's regex.
- **Tiler output regresses after adding a primitive**: extending a primitive (e.g. `LOGLEVEL`) widens every entry and classifier that uses it. Run `make bench` and `go test ./...` after primitive edits.
- **`make lint` fails on `gofmt`**: run `gofmt -w internal/pattern/<file>.go`.
- **Build error in `test/benchmark`**: the test imports `internal/pattern` directly; struct shape changes there propagate. Don't rename exported fields without updating both.

## 8. Make Targets

| Target | Purpose |
|--------|---------|
| `make build` | Build the CLI to `bin/log2grok`. |
| `make test` | All unit tests + golden corpus. |
| `make bench` | Golden corpus correctness suite + per-case benchmarks. |
| `make golden` | Just the curated golden tests. |
| `make lint` | `go vet` + `gofmt` check. |
| `make buildpacks` | Library health-check (prints library/primitive counts; fails if the embedded library is broken). |

## 9. Decision Tree for New Format Requests

```
Is it JSON / TSV / CSV / fixed-delimiter / has a header line?
   yes → §4.3 (structured probe)
   no  ↓

Is the regex composable from existing primitives?
   yes → §4.1 (library entry, no new primitive)
   no  ↓

Is the new building block reusable across ≥2 formats?
   yes → §4.2 (new primitive) + §4.1
   no  → §4.1 with private CustomPatterns

Always: add a corpus case (§5) before merging.
```
