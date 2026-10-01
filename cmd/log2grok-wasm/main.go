//go:build js && wasm

// Command log2grok-wasm exposes the same discovery core as log2grok-web to
// JavaScript, so a page can run entirely in the browser with no server.
//
// It registers one global, log2grokDiscover(logs: string): string, which
// returns the JSON body that POST /api/discover would have returned. The
// embedded pattern library is used as-is; LoadConfig is deliberately not
// called because there is no filesystem to seed. After registering, it calls
// globalThis.log2grokReady() if the host defined one.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/logsonic/log2grok/internal/webapi"
)

func discover(_ js.Value, args []js.Value) any {
	var body any
	if len(args) < 1 || args[0].Type() != js.TypeString {
		_, body = webapi.Fail(webapi.ErrNoContent)
	} else if resp, err := webapi.Run(args[0].String()); err != nil {
		_, body = webapi.Fail(err)
	} else {
		body = resp
	}
	out, err := json.Marshal(body)
	if err != nil {
		return `{"ok":false,"error":{"code":"internal","message":"encode failed"}}`
	}
	return string(out)
}

func main() {
	js.Global().Set("log2grokDiscover", js.FuncOf(discover))
	if ready := js.Global().Get("log2grokReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {} // keep the Go runtime alive for callbacks
}
