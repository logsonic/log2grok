.PHONY: buildpacks build test golden lint run bench

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

bench:
	go test -bench=. -benchmem ./test/benchmark/
