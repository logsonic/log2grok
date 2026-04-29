# log2grok Benchmark Suite

This folder contains a large synthetic correctness + benchmark suite for `log2grok`.

## Generate benchmark corpus

Run:

```bash
go run ./tools/gen_bench_suite
```

This generates `test/benchmark/cases/` with **120 case folders** (12 log families x 10 variants), each containing:

- `input.log`
- `expected.grok`
- `meta.json`

Total generated files: **360**.

## Run correctness suite

```bash
go test ./test/benchmark -run TestDiscoverCorrectnessSuite -v
```

## Run benchmarks

```bash
go test ./test/benchmark -bench BenchmarkDiscover -benchmem
```
