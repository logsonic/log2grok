"use strict";

// Runs the Go discovery engine (compiled to WASM) off the main thread so a
// large paste never freezes the page. Loaded with ?v=<hash>; the same hash
// versions the engine files next to it.
const version = new URLSearchParams(self.location.search).get("v") || "";
const q = version ? "?v=" + encodeURIComponent(version) : "";

importScripts("/static/wasm_exec.js" + q);

const go = new Go();
const ready = new Promise((resolve, reject) => {
  self.log2grokReady = resolve;
  WebAssembly.instantiateStreaming(fetch("/static/log2grok.wasm" + q), go.importObject)
    .then(({ instance }) => go.run(instance))
    .catch(reject);
});

ready.then(
  () => postMessage({ type: "ready" }),
  (err) => postMessage({ type: "failed", message: String(err) })
);

onmessage = async (event) => {
  const { id, logs } = event.data;
  try {
    await ready;
    postMessage({ type: "result", id, json: self.log2grokDiscover(logs) });
  } catch (err) {
    postMessage({ type: "error", id, message: String(err) });
  }
};
