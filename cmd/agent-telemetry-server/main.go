// agent-telemetry-server is the dumb ingest layer for agent-telemetry.
// It receives append-only events from clients as OTLP/HTTP Logs on
// /v1/logs and appends them (INSERT OR IGNORE) into a shared SQLite DB.
// sessions / transcript_stats / pr_metrics are derived VIEWs over events
// for Grafana to read. Aggregation lives entirely in those VIEWs; the
// server only shares the schema DDL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/serverpipe"
)

// version is overwritten at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "migrate-to-events" {
		return runMigrate(args[1:])
	}
	fs := flag.NewFlagSet("agent-telemetry-server", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "/var/lib/agent-telemetry", "directory holding agent-telemetry.db and collisions.log")
	listen := fs.String("listen", "127.0.0.1:8443", "HTTP listen address (defaults to loopback; /v1/logs has no auth — front it with a TLS-terminating proxy before exposing beyond loopback)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Printf("agent-telemetry-server %s\n", version)
		return nil
	}

	// The ingest endpoint has no application-level auth: the trust boundary
	// is network reachability (loopback default) plus a fronting proxy for
	// public exposure (issue 0057). Warn loudly if bound beyond loopback so
	// an operator can't silently expose an unauthenticated /v1/logs.
	if host, _, err := net.SplitHostPort(*listen); err == nil && !isLoopbackHost(host) {
		log.Printf("WARNING: listening on %s — /v1/logs accepts unauthenticated writes; "+
			"restrict network reach and front it with a TLS-terminating proxy (see issue 0057)", *listen)
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(*dataDir, "agent-telemetry.db")
	db, err := serverpipe.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	handler := serverpipe.NewHandler(db, *dataDir)
	defer handler.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	handler.Routes(mux)

	// Bound read/write/idle so a slow-body or slow-loris client can't pin a
	// connection indefinitely (issue 0057, "安価に閉じられる範囲"). Real OTLP
	// batches are tiny; 60s read comfortably covers a legit gzip payload under
	// the 50 MB cap even on a slow link. Heavier rate limiting stays a proxy
	// responsibility.
	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("agent-telemetry-server %s listening on %s (db=%s)", version, *listen, dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// isLoopbackHost reports whether a listen host stays on the local machine.
// An empty host (e.g. ":8443") means all interfaces, which is NOT loopback.
func isLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("agent-telemetry-server migrate-to-events", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "/var/lib/agent-telemetry", "directory holding agent-telemetry.db")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(*dataDir, "agent-telemetry.db")
	db, err := serverpipe.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	log.Printf("migrate-to-events: ensured events schema at %s", dbPath)
	return nil
}
