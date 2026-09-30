package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	l2g "github.com/logsonic/log2grok/pkg/log2grok"
)

const maxBodyBytes = int64(8 << 20) // 8 MiB

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	configDir := flag.String("config-dir", "", "externalized pattern library dir (default: embedded)")
	flag.Parse()

	if err := l2g.LoadConfig(*configDir, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           newMux(maxBodyBytes),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("log2grok web UI listening on http://%s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
