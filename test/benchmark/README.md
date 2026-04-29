# log2grok Benchmark Suite

Correctness + benchmark suite for `log2grok`. Cases live under `cases/`, each a folder with `input.log`, `expected.grok`, and `meta.json`. Edit cases in place — there is no generator.

## Run correctness suite

```bash
go test ./test/benchmark -run TestDiscoverCorrectnessSuite -v
```

## Run benchmarks

```bash
go test ./test/benchmark -bench BenchmarkDiscover -benchmem
```
