package main

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// newMux builds every route. maxBody bounds the /api/discover request body.
func newMux(maxBody int64) *http.ServeMux {
	sub, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/api/discover", func(w http.ResponseWriter, r *http.Request) {
		handleDiscover(w, r, maxBody)
	})
	mux.HandleFunc("/", handleIndex)
	return mux
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// handleDiscover is implemented in Task 2.
func handleDiscover(w http.ResponseWriter, _ *http.Request, _ int64) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
