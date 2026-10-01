#!/bin/sh
# Builds the standalone, server-free site into dist/ for static hosting
# (Cloudflare Pages: build command `make site`, output directory `dist`).
set -eu
cd "$(dirname "$0")/.."

SRC=cmd/log2grok-web/static
OUT=dist
rm -rf "$OUT"
mkdir -p "$OUT/static"

GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$OUT/static/log2grok.wasm" ./cmd/log2grok-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$OUT/static/wasm_exec.js"
cp "$SRC/app.js" "$SRC/styles.css" "$SRC/worker.js" "$OUT/static/"

# One content hash versions every asset URL (matching the Go server's scheme).
VER=$(cat "$OUT"/static/* | shasum -a 256 | cut -c1-10)
sed "s/{{v}}/$VER/g" "$SRC/index.html" > "$OUT/index.html"
sed "s/{{v}}/$VER/g" "$SRC/404.html" > "$OUT/404.html"
cp deploy/cloudflare/_headers "$OUT/_headers"

echo "site built in $OUT (version $VER)"
ls -l "$OUT/static" | awk 'NR>1 {print "  " $5 "\t" $9}'
